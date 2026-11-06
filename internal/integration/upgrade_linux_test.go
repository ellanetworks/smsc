//go:build integration && linux

package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

const upgradeSettle = 13 * time.Second

type traffic struct {
	core  *core
	mu    sync.Mutex
	up    []*node
	down  map[*node]bool
	ids   []int64
	sent  int
	last  time.Time
	gap   time.Duration
	quit  chan struct{}
	wg    sync.WaitGroup
	email int
}

func startTraffic(c *core, nodes []*node) *traffic {
	tr := &traffic{core: c, up: nodes, down: map[*node]bool{}, last: time.Now(), quit: make(chan struct{})}

	tr.wg.Go(tr.submit)
	tr.wg.Go(tr.write)

	return tr
}

func (tr *traffic) pick(i int) *node {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	var live []*node

	for _, n := range tr.up {
		if !tr.down[n] {
			live = append(live, n)
		}
	}

	if len(live) == 0 {
		return nil
	}

	return live[i%len(live)]
}

func (tr *traffic) set(n *node, up bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	tr.down[n] = !up
}

func (tr *traffic) submit() {
	var wg sync.WaitGroup

	defer wg.Wait()

	for i := 0; ; i++ {
		select {
		case <-tr.quit:
			return
		case <-time.After(25 * time.Millisecond):
		}

		via := tr.pick(i)
		if via == nil {
			continue
		}

		tr.mu.Lock()
		tr.sent++
		tr.mu.Unlock()

		wg.Go(func() {
			if id, err := tr.core.submit(via, i%50, 50+i%50); err == nil {
				tr.mu.Lock()
				tr.ids = append(tr.ids, id)
				tr.mu.Unlock()
			}
		})
	}
}

func (tr *traffic) write() {
	for i := 0; ; i++ {
		select {
		case <-tr.quit:
			return
		case <-time.After(250 * time.Millisecond):
		}

		via := tr.pick(i)
		if via == nil {
			continue
		}

		email := fmt.Sprintf("writer%d@example.com", i)

		if _, err := via.request(http.MethodPost, "/api/v1/users",
			map[string]any{"email": email, "password": "correct horse", "role_id": 4}, nil); err != nil {
			continue
		}

		tr.mu.Lock()

		if gap := time.Since(tr.last); gap > tr.gap {
			tr.gap = gap
		}

		tr.last = time.Now()
		tr.email = i
		tr.mu.Unlock()
	}
}

func (tr *traffic) finish() ([]int64, int, time.Duration) {
	close(tr.quit)
	tr.wg.Wait()

	tr.mu.Lock()
	defer tr.mu.Unlock()

	return tr.ids, tr.sent, tr.gap
}

func (c *cluster) drain(n *node) {
	c.t.Helper()

	via := c.otherThan(n)
	via.must(http.MethodPost, "/api/v1/cluster/members/"+n.id+"/drain", nil, nil)

	eventually(c.t, n.name+" drained", func() error {
		st, err := via.status()
		if err != nil {
			return err
		}

		for _, m := range st.Members {
			if m.NodeID == n.id && m.DrainState != "drained" {
				return fmt.Errorf("%s", m.DrainState)
			}
		}

		return nil
	})
}

func (c *cluster) resume(n *node) {
	c.t.Helper()

	eventually(c.t, n.name+" resumed", func() error {
		_, err := n.request(http.MethodPost, "/api/v1/cluster/members/"+n.id+"/resume", nil, nil)
		return err
	})
}

func (c *cluster) otherThan(n *node) *node {
	c.t.Helper()

	for _, o := range c.nodes {
		if o != n && o.running() && o.token() != "" {
			return o
		}
	}

	c.t.Fatal("no other live node")

	return nil
}

func (c *cluster) schemas() map[string]int {
	out := map[string]int{}

	for _, n := range c.nodes {
		if !n.running() {
			continue
		}

		if st, err := n.status(); err == nil {
			out[n.name] = st.Schema
		} else {
			out[n.name] = -1
		}
	}

	return out
}

func (c *cluster) expectSchema(want int) error {
	for name, s := range c.schemas() {
		if s != want {
			return fmt.Errorf("%s runs schema %d, want %d", name, s, want)
		}
	}

	return nil
}

func (c *cluster) holdsSchema(want int, d time.Duration) {
	c.t.Helper()

	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(time.Second) {
		if err := c.expectSchema(want); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *cluster) upgrade(n *node) {
	c.t.Helper()

	n.stop()

	n.bin = upgraded
	n.start()
	n.logIn()
}

func TestRollingUpgradeUnderLoad(t *testing.T) {
	cl := newCluster(t)
	c := newCore(t, cl.nodes)

	st, err := cl.leader().status()
	if err != nil {
		t.Fatal(err)
	}

	from := st.Schema

	tr := startTraffic(c, cl.nodes)

	time.Sleep(2 * time.Second)

	leader := cl.leader()
	order := append(cl.followers(leader), leader)

	for i, n := range order {
		tr.set(n, false)
		cl.drain(n)

		if i == len(order)-1 {
			n.stop()
			cl.holdsSchema(from, upgradeSettle)

			n.bin = upgraded
			n.start()
			n.logIn()
		} else {
			cl.upgrade(n)
		}

		cl.resume(n)
		cl.waitStable()
		c.waitOpen(n)
		tr.set(n, true)

		if i < len(order)-1 {
			cl.holdsSchema(from, 2*time.Second)
		}
	}

	eventually(t, "the cluster on the next schema", func() error { return cl.expectSchema(from + 1) })

	time.Sleep(2 * time.Second)

	ids, sent, gap := tr.finish()

	t.Logf("%d of %d messages accepted, longest write gap %s", len(ids), sent, gap)

	if len(ids) < sent*3/4 {
		t.Fatalf("only %d of %d messages accepted during the upgrade", len(ids), sent)
	}

	if gap > 15*time.Second {
		t.Fatalf("the cluster took no write for %s", gap)
	}

	eventually(t, "every accepted message delivered", func() error { return c.deliveredAll(ids) })

	if o := c.overlaps(); len(o) > 0 {
		t.Fatalf("two SMS-DELIVERs outstanding at once to %v", o)
	}

	for _, n := range cl.nodes {
		eventually(t, n.name+" to see the last write", func() error {
			_, err := n.request(http.MethodGet, fmt.Sprintf("/api/v1/users/writer%d@example.com", tr.email), nil, nil)
			return err
		})
	}

	old := cl.nodes[0]
	old.stop()
	old.bin = binary
	old.launch()

	select {
	case <-old.exited:
	case <-time.After(waitTimeout):
		t.Fatal("the previous binary started on a database of the next schema")
	}

	old.bin = upgraded
	old.start()
	old.logIn()
	cl.waitStable()
}

func TestSchemaWaitsForADownMemberUntilRemoved(t *testing.T) {
	cl := newCluster(t)

	st, err := cl.leader().status()
	if err != nil {
		t.Fatal(err)
	}

	from := st.Schema
	leader := cl.leader()
	followers := cl.followers(leader)
	behind := followers[1]

	cl.upgrade(followers[0])
	cl.waitStable()

	cl.drain(leader)
	cl.upgrade(leader)
	cl.resume(leader)
	cl.waitStable()

	behind.kill()
	cl.holdsSchema(from, upgradeSettle)

	cl.otherThan(behind).must(http.MethodDelete, "/api/v1/cluster/members/"+behind.id+"?force=true", nil, nil)

	eventually(t, "the cluster on the next schema", func() error { return cl.expectSchema(from + 1) })

	cl.leader().must(http.MethodPost, "/api/v1/users",
		map[string]any{"email": "after@example.com", "password": "correct horse", "role_id": 4}, nil)
}
