package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

const (
	logCacheSize     = 512
	logStoreTimeout  = 10 * time.Second
	snapshotInterval = 2 * time.Minute
	trailingLogs     = 10240
	snapshotEntries  = 8192
	soleVoterTimeout = 100 * time.Millisecond
)

var ErrNotLeader = errors.New("not the leader")

type Config struct {
	Dir                   string
	Clustered             bool
	PerformanceMultiplier int
	Now                   func() time.Time
	Logger                *slog.Logger
}

type Node struct {
	cfg     Config
	id      string
	db      *db.DB
	raft    *raft.Raft
	store   *raftboltdb.BoltStore
	batcher *batcher
	notify  chan bool

	mu      sync.Mutex
	term    uint64
	ready   chan struct{}
	lead    []func(ctx context.Context, term uint64)
	leading context.Context
	cancel  context.CancelFunc
	runs    sync.WaitGroup

	checkpointed bool
	done         chan struct{}
	stopped      chan struct{}
}

func Open(ctx context.Context, cfg Config, d *db.DB) (*Node, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if cfg.PerformanceMultiplier < 1 {
		cfg.PerformanceMultiplier = 1
	}

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the raft directory: %w", err)
	}

	id, err := nodeID(cfg.Dir)
	if err != nil {
		return nil, err
	}

	store, err := raftboltdb.New(raftboltdb.Options{Path: filepath.Join(cfg.Dir, "raft.db")})
	if err != nil {
		return nil, fmt.Errorf("open the raft log: %w", err)
	}

	n := &Node{
		cfg: cfg, id: id, db: d, store: store, notify: make(chan bool, 16), ready: make(chan struct{}),
		done: make(chan struct{}), stopped: make(chan struct{}),
	}

	if err := n.start(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}

	return n, nil
}

func (n *Node) start(ctx context.Context) error {
	snapshots, err := newSnapshotStore(n.db, filepath.Join(n.cfg.Dir, "snapshots"))
	if err != nil {
		return err
	}

	logs, err := raft.NewLogCache(logCacheSize, timeoutLogStore{LogStore: n.store, timeout: logStoreTimeout})
	if err != nil {
		return fmt.Errorf("open the raft log: %w", err)
	}

	existing, err := raft.HasExistingState(logs, n.store, raft.NewInmemSnapshotStore())
	if err != nil {
		return fmt.Errorf("read the raft state: %w", err)
	}

	if !existing {
		if err := n.db.ResetPosition(ctx); err != nil {
			return err
		}
	}

	conf := raft.DefaultConfig()
	m := time.Duration(n.cfg.PerformanceMultiplier)
	conf.LocalID = raft.ServerID(n.id)
	conf.HeartbeatTimeout *= m
	conf.ElectionTimeout *= m
	conf.LeaderLeaseTimeout *= m

	if !n.cfg.Clustered {
		conf.HeartbeatTimeout, conf.ElectionTimeout, conf.LeaderLeaseTimeout = soleVoterTimeout, soleVoterTimeout,
			soleVoterTimeout/2
	}

	conf.BatchApplyCh = true
	conf.MaxAppendEntries = 64
	conf.NoSnapshotRestoreOnStart = true
	conf.SnapshotInterval = snapshotInterval
	conf.SnapshotThreshold = snapshotEntries
	conf.TrailingLogs = trailingLogs
	conf.NotifyCh = n.notify
	conf.Logger = hclog.FromStandardLogger(slog.NewLogLogger(n.cfg.Logger.Handler(), slog.LevelWarn),
		&hclog.LoggerOptions{Name: "raft", Level: hclog.Warn})

	addr, transport := raft.NewInmemTransport(raft.ServerAddress(n.id))

	r, err := raft.NewRaft(conf, &fsm{db: n.db, dir: filepath.Join(n.cfg.Dir, "snapshots")}, logs, n.store, snapshots,
		transport)
	if err != nil {
		return fmt.Errorf("start raft: %w", err)
	}

	n.raft = r

	if !existing {
		err := r.BootstrapCluster(raft.Configuration{Servers: []raft.Server{
			{Suffrage: raft.Voter, ID: conf.LocalID, Address: addr},
		}}).Error()
		if err != nil {
			_ = r.Shutdown().Error()
			return fmt.Errorf("bootstrap: %w", err)
		}
	}

	n.batcher = newBatcher(r, n.cfg.Now)

	go n.watch()

	return nil
}

func nodeID(dir string) (string, error) {
	path := filepath.Join(dir, "node-id")

	raw, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(raw)), nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read the node ID: %w", err)
	}

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)

	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write the node ID: %w", err)
	}

	return id, nil
}

type batcherProposer struct{ b *batcher }

func (p batcherProposer) Propose(ctx context.Context, cmds []db.Command) ([]db.Result, error) {
	return p.b.propose(ctx, cmds)
}

func (n *Node) ID() string {
	return n.id
}

func (n *Node) Propose(ctx context.Context, cmds []db.Command) ([]db.Result, error) {
	n.mu.Lock()
	ready := n.ready
	n.mu.Unlock()

	select {
	case <-ready:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrNotLeader, ctx.Err())
	case <-n.done:
		return nil, raft.ErrRaftShutdown
	}

	return n.batcher.propose(ctx, cmds)
}

func (n *Node) Lead(f func(ctx context.Context, term uint64)) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.lead = append(n.lead, f)

	select {
	case <-n.ready:
		n.run(n.leading, n.term, f)
	default:
	}
}

func (n *Node) run(ctx context.Context, term uint64, f func(ctx context.Context, term uint64)) {
	n.runs.Add(1)

	go func() {
		defer n.runs.Done()

		f(ctx, term)
	}()
}

func (n *Node) watch() {
	defer close(n.stopped)

	for {
		select {
		case <-n.done:
			n.stepDown()
			return
		case leader := <-n.notify:
			n.stepDown()

			if leader {
				n.becomeLeader()
			}
		}
	}
}

func (n *Node) becomeLeader() {
	ctx, cancel := context.WithCancel(context.Background())

	n.mu.Lock()
	n.cancel = cancel
	n.mu.Unlock()

	n.runs.Add(1)

	go func() {
		defer n.runs.Done()

		term, err := n.establish(ctx)
		if err != nil {
			if ctx.Err() == nil {
				n.cfg.Logger.ErrorContext(ctx, "failed to establish leadership, handing it over", slog.Any("error", err))

				_ = n.raft.LeadershipTransfer().Error()
			}

			return
		}

		n.mu.Lock()
		defer n.mu.Unlock()

		if ctx.Err() != nil {
			return
		}

		n.term, n.leading = term, ctx
		close(n.ready)

		for _, f := range n.lead {
			n.run(ctx, term, f)
		}
	}()
}

func (n *Node) establish(ctx context.Context) (uint64, error) {
	for {
		term, err := n.db.LeaderStart(ctx, batcherProposer{n.batcher}, n.id)
		if err == nil {
			if err := n.checkpoint(); err != nil {
				return 0, err
			}

			return term, nil
		}

		if ctx.Err() != nil || errors.Is(err, raft.ErrNotLeader) || errors.Is(err, raft.ErrLeadershipLost) {
			return 0, err
		}

		n.cfg.Logger.WarnContext(ctx, "failed to start the leader term, retrying", slog.Any("error", err))

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (n *Node) checkpoint() error {
	if n.checkpointed {
		return nil
	}

	future := n.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		return fmt.Errorf("read the raft configuration: %w", err)
	}

	if servers := future.Configuration().Servers; len(servers) != 1 || servers[0].ID != raft.ServerID(n.id) {
		n.checkpointed = true
		return nil
	}

	retention := n.raft.ReloadableConfig()
	compacting := retention
	compacting.TrailingLogs = 0

	if err := n.raft.ReloadConfig(compacting); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}

	snapErr := n.raft.Snapshot().Error()

	if err := n.raft.ReloadConfig(retention); err != nil {
		return errors.Join(fmt.Errorf("checkpoint: %w", err), snapErr)
	}

	if snapErr != nil {
		return fmt.Errorf("checkpoint: %w", snapErr)
	}

	n.checkpointed = true

	return nil
}

func (n *Node) stepDown() {
	n.mu.Lock()
	cancel := n.cancel
	n.cancel = nil

	select {
	case <-n.ready:
		n.ready = make(chan struct{})
	default:
	}

	n.term = 0
	n.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	n.runs.Wait()
}

func (n *Node) Leader() (bool, uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()

	select {
	case <-n.ready:
		return true, n.term
	default:
		return false, 0
	}
}

func (n *Node) Snapshot() error {
	return n.raft.Snapshot().Error()
}

func (n *Node) Close() error {
	close(n.done)
	<-n.stopped

	n.batcher.close()

	return errors.Join(n.raft.Shutdown().Error(), n.store.Close())
}
