package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/hashicorp/raft"
)

func freeAddress(t testing.TB) string {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = ln.Close() }()

	return ln.Addr().String()
}

func openClustered(t testing.TB, dir, address string) *env {
	t.Helper()

	ctx := context.Background()

	d, err := db.Open(ctx, filepath.Join(dir, "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := Open(ctx, Config{Dir: filepath.Join(dir, "raft"), Address: address, Version: "test"}, d)
	if err != nil {
		_ = d.Close()

		t.Fatal(err)
	}

	d.SetProposer(n)

	return &env{dir: dir, db: d, node: n}
}

type testCluster struct {
	t     testing.TB
	nodes []*env
	addrs []string
}

func newCluster(t testing.TB, size int) *testCluster {
	t.Helper()

	c := &testCluster{t: t}

	for i := range size {
		c.addrs = append(c.addrs, freeAddress(t))
		c.nodes = append(c.nodes, openClustered(t, t.TempDir(), c.addrs[i]))
	}

	t.Cleanup(func() {
		for _, e := range c.nodes {
			if e != nil {
				_ = e.node.Close()
				_ = e.db.Close()
			}
		}
	})

	waitLeader(t, c.nodes[0].node)
	waitMember(t, c.nodes[0], c.nodes[0].node.ID())

	for _, e := range c.nodes[1:] {
		waitLeader(t, e.node)

		token, err := c.nodes[0].node.MintToken(timeout(t), time.Hour)
		if err != nil {
			t.Fatal(err)
		}

		if err := e.node.Join(timeout(t), token.Token); err != nil {
			t.Fatal(err)
		}
	}

	for _, e := range c.nodes {
		for _, other := range c.nodes {
			waitMember(t, e, other.node.ID())
		}
	}

	return c
}

func waitMember(t testing.TB, e *env, id string) {
	t.Helper()

	eventually(t, fmt.Sprintf("member %s on %s", id, e.node.ID()), func() error {
		_, err := e.db.Member(context.Background(), id)
		return err
	})
}

func eventually(t testing.TB, what string, f func() error) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)

	var err error

	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s: %v", what, err)
}

func (c *testCluster) leader() *env {
	c.t.Helper()

	var leader *env

	eventually(c.t, "a leader", func() error {
		for _, e := range c.nodes {
			if e == nil {
				continue
			}

			if ok, _ := e.node.Leader(); ok {
				leader = e
				return nil
			}
		}

		return errors.New("no leader")
	})

	return leader
}

func (c *testCluster) follower() *env {
	leader := c.leader()

	for _, e := range c.nodes {
		if e != nil && e != leader {
			return e
		}
	}

	c.t.Fatal("no follower")

	return nil
}

func TestJoinReplicatesAndForwards(t *testing.T) {
	c := newCluster(t, 3)
	ctx := timeout(t)

	follower := c.follower()

	id, err := follower.db.CreateUser(ctx, db.NewUser{Email: "a@example.com", Role: db.RoleAdmin, PasswordHash: "h"}, 10)
	if err != nil {
		t.Fatal(err)
	}

	if u, err := follower.db.GetUserByID(ctx, id); err != nil || u.Email != "a@example.com" {
		t.Fatalf("read your write on the follower: %+v, %v", u, err)
	}

	for _, e := range c.nodes {
		eventually(t, "the user on "+e.node.ID(), func() error {
			_, err := e.db.GetUserByID(ctx, id)
			return err
		})
	}

	hashes := map[string]bool{}

	eventually(t, "equal hashes", func() error {
		clear(hashes)

		index := c.leader().db.AppliedIndex()

		for _, e := range c.nodes {
			h, at, err := e.db.Hash(ctx)
			if err != nil {
				return err
			}

			if at != index {
				return fmt.Errorf("%s at %d, leader at %d", e.node.ID(), at, index)
			}

			hashes[string(h)] = true
		}

		if len(hashes) != 1 {
			return fmt.Errorf("%d different hashes", len(hashes))
		}

		return nil
	})

	labels := map[string]bool{}

	members, err := follower.db.Members(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range members {
		labels[m.DiameterLabel] = true
	}

	if len(members) != 3 || !labels["smsc"] || !labels["smsc2"] || !labels["smsc3"] {
		t.Fatalf("members = %+v", members)
	}
}

func TestTokenIsSingleUse(t *testing.T) {
	c := newCluster(t, 2)

	token, err := c.leader().node.MintToken(timeout(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	joiner := openClustered(t, t.TempDir(), freeAddress(t))
	t.Cleanup(func() { joiner.close(t) })
	waitLeader(t, joiner.node)

	if err := joiner.node.Join(timeout(t), token.Token); err != nil {
		t.Fatal(err)
	}

	again := openClustered(t, t.TempDir(), freeAddress(t))
	t.Cleanup(func() { again.close(t) })
	waitLeader(t, again.node)

	if err := again.node.Join(timeout(t), token.Token); !errors.Is(err, db.ErrTokenUsed) {
		t.Fatalf("second join = %v, want %v", err, db.ErrTokenUsed)
	}
}

func TestPromotesToThreeVoters(t *testing.T) {
	waitVoters(t, newCluster(t, 3), 3)
}

func TestLeaderFailover(t *testing.T) {
	c := newCluster(t, 3)

	waitVoters(t, c, 3)

	leader := c.leader()

	for i, e := range c.nodes {
		if e == leader {
			_ = e.node.Close()
			_ = e.db.Close()
			c.nodes[i] = nil
		}
	}

	survivor := c.leader()

	if _, err := survivor.db.CreateUser(timeout(t), db.NewUser{Email: "b@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	for _, e := range c.nodes {
		if e != nil && e != survivor {
			if _, err := e.db.CreateUser(timeout(t), db.NewUser{
				Email: "c" + e.node.ID() + "@example.com",
				Role:  db.RoleAdmin,
			}, 10); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func waitVoters(t testing.TB, c *testCluster, want int) {
	t.Helper()

	eventually(t, fmt.Sprintf("%d voters", want), func() error {
		r, _ := c.leader().node.current()

		future := r.GetConfiguration()
		if err := future.Error(); err != nil {
			return err
		}

		voters := 0

		for _, s := range future.Configuration().Servers {
			if s.Suffrage == raft.Voter {
				voters++
			}
		}

		if voters != want {
			return fmt.Errorf("%d voters", voters)
		}

		return nil
	})
}

func TestRemoveMember(t *testing.T) {
	c := newCluster(t, 3)
	follower := c.follower()

	if err := c.leader().node.RemoveMember(timeout(t), follower.node.ID()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-follower.node.Removed():
	case <-time.After(10 * time.Second):
		t.Fatal("the removed node did not notice")
	}

	if _, err := follower.db.CreateUser(timeout(t), db.NewUser{Email: "x@example.com", Role: db.RoleAdmin}, 10); !errors.Is(err, ErrRemoved) {
		t.Fatalf("write on a removed node = %v, want %v", err, ErrRemoved)
	}

	for _, e := range c.nodes {
		if e == follower {
			continue
		}

		eventually(t, "removal on "+e.node.ID(), func() error {
			if _, err := e.db.Member(context.Background(), follower.node.ID()); !errors.Is(err, db.ErrNotFound) {
				return fmt.Errorf("still a member: %v", err)
			}

			return nil
		})
	}
}

func TestRecoverAfterLosingQuorum(t *testing.T) {
	c := newCluster(t, 3)
	waitVoters(t, c, 3)

	ctx := timeout(t)

	if _, err := c.leader().db.CreateUser(ctx, db.NewUser{Email: "kept@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	survivor := c.nodes[1]

	eventually(t, "the user on the survivor", func() error {
		_, err := survivor.db.GetUser(ctx, "kept@example.com")
		return err
	})

	var lost []string

	for i, e := range c.nodes {
		if e != survivor {
			lost = append(lost, e.node.ID())
		}

		_ = e.node.Close()
		_ = e.db.Close()
		c.nodes[i] = nil
	}

	d, err := db.Open(ctx, filepath.Join(survivor.dir, "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{Dir: filepath.Join(survivor.dir, "raft"), Address: c.addrs[1]}

	st, err := Inspect(ctx, cfg, d)
	if err != nil || len(st.Configuration.Servers) != 3 || st.LastIndex == 0 {
		t.Fatalf("inspect = %+v, %v", st, err)
	}

	if err := Recover(ctx, cfg, d, io.Discard); err != nil {
		t.Fatal(err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	e := openClustered(t, survivor.dir, c.addrs[1])
	c.nodes[1] = e

	waitLeader(t, e.node)

	if _, err := e.db.GetUser(ctx, "kept@example.com"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.db.CreateUser(ctx, db.NewUser{Email: "after@example.com", Role: db.RoleAdmin}, 10); err != nil {
		t.Fatal(err)
	}

	members, err := e.db.Members(ctx)
	if err != nil || len(members) != 1 || members[0].NodeID != e.node.ID() || len(lost) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
}
