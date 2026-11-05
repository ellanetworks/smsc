package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

type RecoveryState struct {
	NodeID        string
	LastIndex     uint64
	LastTerm      uint64
	AppliedIndex  uint64
	Configuration raft.Configuration
}

func Inspect(ctx context.Context, cfg Config, d *db.DB) (RecoveryState, error) {
	id, store, err := openOffline(cfg)
	if err != nil {
		return RecoveryState{}, err
	}

	defer func() { _ = store.Close() }()

	st := RecoveryState{NodeID: id, AppliedIndex: d.AppliedIndex()}

	if st.LastIndex, err = store.LastIndex(); err != nil {
		return RecoveryState{}, err
	}

	if st.LastIndex > 0 {
		var l raft.Log
		if err := store.GetLog(st.LastIndex, &l); err != nil {
			return RecoveryState{}, err
		}

		st.LastTerm = l.Term
	}

	p, err := d.Position(ctx)
	if err != nil {
		return RecoveryState{}, err
	}

	if p.Configuration != nil {
		st.Configuration = raft.DecodeConfiguration(p.Configuration)
	}

	if st.LastTerm == 0 {
		st.LastIndex, st.LastTerm = p.Index, p.Term
	}

	if latest, ok, err := latestConfiguration(store, p.ConfigurationIndex); err != nil {
		return RecoveryState{}, err
	} else if ok {
		st.Configuration = latest
	}

	return st, nil
}

func latestConfiguration(store *raftboltdb.BoltStore, after uint64) (raft.Configuration, bool, error) {
	first, err := store.FirstIndex()
	if err != nil {
		return raft.Configuration{}, false, err
	}

	last, err := store.LastIndex()
	if err != nil {
		return raft.Configuration{}, false, err
	}

	var (
		found  bool
		latest raft.Configuration
	)

	for i := max(first, after+1); i <= last && i > 0; i++ {
		var l raft.Log
		if err := store.GetLog(i, &l); err != nil {
			return raft.Configuration{}, false, err
		}

		if l.Type == raft.LogConfiguration {
			latest, found = raft.DecodeConfiguration(l.Data), true
		}
	}

	return latest, found, nil
}

func openOffline(cfg Config) (string, *raftboltdb.BoltStore, error) {
	if _, err := os.Stat(filepath.Join(cfg.Dir, removedFile)); err == nil {
		return "", nil, ErrRemoved
	}

	id, err := nodeID(cfg.Dir)
	if err != nil {
		return "", nil, err
	}

	if _, err := os.Stat(filepath.Join(cfg.Dir, logFile)); err != nil {
		return "", nil, fmt.Errorf("no raft log in %s: %w", cfg.Dir, err)
	}

	store, err := raftboltdb.New(raftboltdb.Options{Path: filepath.Join(cfg.Dir, logFile)})
	if err != nil {
		return "", nil, fmt.Errorf("open the raft log: %w", err)
	}

	return id, store, nil
}

func Recover(ctx context.Context, cfg Config, d *db.DB, w io.Writer) error {
	id, store, err := openOffline(cfg)
	if err != nil {
		return err
	}

	defer func() { _ = store.Close() }()

	snapshots, err := newSnapshotStore(d, filepath.Join(cfg.Dir, "snapshots"))
	if err != nil {
		return err
	}

	addr := raft.ServerAddress(id)
	if cfg.Address != "" {
		addr = raft.ServerAddress(cfg.Advertise)
		if addr == "" {
			addr = raft.ServerAddress(cfg.Address)
		}
	}

	conf := raft.DefaultConfig()
	conf.LocalID = raft.ServerID(id)
	conf.Logger = hclog.NewNullLogger()

	_, transport := raft.NewInmemTransport(addr)

	configuration := raft.Configuration{Servers: []raft.Server{{Suffrage: raft.Voter, ID: conf.LocalID, Address: addr}}}

	err = raft.RecoverCluster(conf, &fsm{db: d, dir: filepath.Join(cfg.Dir, "snapshots")}, store, store, snapshots,
		transport, configuration)
	if err != nil {
		return fmt.Errorf("recover: %w", err)
	}

	if err := d.ForceConfiguration(ctx, raft.EncodeConfiguration(configuration), id); err != nil {
		return err
	}

	if err := os.Remove(filepath.Join(cfg.Dir, pinsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	_, _ = fmt.Fprintf(w, "node %s is now the only voter of its cluster, at index %d\n", id, d.AppliedIndex())

	return nil
}
