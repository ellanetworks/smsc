package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"path/filepath"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
)

func clusterCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "recover" {
		return errors.New("usage: smsc cluster recover [--config smsc.yaml] [--apply]")
	}

	fs := flag.NewFlagSet("smsc cluster recover", flag.ContinueOnError)
	configPath := fs.String("config", "smsc.yaml", "path to the configuration file")
	apply := fs.Bool("apply", false, "make this node the only voter of its cluster")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	ctx := context.Background()

	d, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	defer func() { _ = d.Close() }()

	clusterCfg := cluster.Config{Dir: filepath.Join(filepath.Dir(cfg.DB.Path), "raft")}

	if c := cfg.Cluster; c != nil {
		clusterCfg.Address = netip.AddrPortFrom(c.Address, uint16(c.Port)).String()
		clusterCfg.Advertise = c.AdvertiseAddress()
	}

	if *apply {
		return cluster.Recover(ctx, clusterCfg, d, out)
	}

	st, err := cluster.Inspect(ctx, clusterCfg, d)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "node:          %s\nlast log:      index %d, term %d\napplied index: %d\nconfiguration:\n",
		st.NodeID, st.LastIndex, st.LastTerm, st.AppliedIndex)

	for _, s := range st.Configuration.Servers {
		_, _ = fmt.Fprintf(out, "  %s %s %s\n", s.ID, s.Suffrage, s.Address)
	}

	_, _ = fmt.Fprintln(out, "\nRun this on every surviving node, and recover the one with the highest last log term, then index, "+
		"with --apply, while all nodes are stopped.")

	return nil
}
