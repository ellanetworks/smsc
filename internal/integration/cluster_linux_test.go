//go:build integration && linux

package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func submitLoad(t *testing.T, c *core, nodes func() []*node, count int, every time.Duration) []int64 {
	t.Helper()

	var (
		mu  sync.Mutex
		ids []int64
		wg  sync.WaitGroup
	)

	for i := range count {
		targets := nodes()
		via := targets[i%len(targets)]

		wg.Go(func() {
			if id, err := c.submit(via, i%50, 50+i%50); err == nil {
				mu.Lock()

				ids = append(ids, id)

				mu.Unlock()
			}
		})

		time.Sleep(every)
	}

	wg.Wait()

	return ids
}

func TestLeaderCrashLosesNoAcceptedMessage(t *testing.T) {
	cl := newCluster(t)
	c := newCore(t, cl.nodes)
	leader := cl.leader()

	done := make(chan []int64)

	go func() {
		done <- submitLoad(t, c, func() []*node { return cl.followers(nil) }, 300, 20*time.Millisecond)
	}()

	time.Sleep(2 * time.Second)
	leader.kill()

	ids := <-done

	if len(ids) < 200 {
		t.Fatalf("only %d of 300 messages accepted around the crash", len(ids))
	}

	eventually(t, "every accepted message delivered", func() error { return c.deliveredAll(ids) })

	if o := c.overlaps(); len(o) > 0 {
		t.Fatalf("two SMS-DELIVERs outstanding at once to %v", o)
	}
}

// TS 23.040 clause 6.2
func TestFrozenLeaderNeverOverlapsDeliveries(t *testing.T) {
	cl := newCluster(t)
	c := newCore(t, cl.nodes)
	leader := cl.leader()
	followers := cl.followers(leader)

	c.delay.Store(int64(4 * time.Second))

	var ids []int64

	for i := range 10 {
		id, err := c.submit(followers[0], 100+i, i)
		if err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	time.Sleep(time.Second)
	leader.freeze()

	survivors := &cluster{t: t, nodes: followers}
	survivors.leader()

	c.delay.Store(0)

	for i := range 10 {
		id, err := c.submit(followers[1], 200+i, i)
		if err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	time.Sleep(2 * time.Second)
	leader.thaw()

	eventually(t, "every message delivered", func() error { return c.deliveredAll(ids) })

	if o := c.overlaps(); len(o) > 0 {
		t.Fatalf("two SMS-DELIVERs outstanding at once to %v", o)
	}
}

func TestRemoveAndRejoin(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	gone := cl.followers(leader)[0]
	other := cl.followers(leader)[1]

	if code, _ := leader.request(http.MethodDelete, "/api/v1/cluster/members/"+gone.id, nil, nil); code != http.StatusConflict {
		t.Fatalf("removing an active member = %d, want %d", code, http.StatusConflict)
	}

	leader.must(http.MethodPost, "/api/v1/cluster/members/"+gone.id+"/drain", nil, nil)

	eventually(t, "the member drained", func() error {
		st, err := leader.status()
		if err != nil {
			return err
		}

		for _, m := range st.Members {
			if m.NodeID == gone.id && m.DrainState != "drained" {
				return fmt.Errorf("%s", m.DrainState)
			}
		}

		return nil
	})

	code, err := other.request(http.MethodDelete, "/api/v1/cluster/members/"+gone.id, nil, nil)
	if code != http.StatusMisdirectedRequest {
		t.Fatalf("removing a member on a follower = %d, want %d: %v", code, http.StatusMisdirectedRequest, err)
	}

	if got := leaderOf(err); got != leader.apiAddress() {
		t.Fatalf("a follower points at %q, want the leader %q", got, leader.apiAddress())
	}

	leader.must(http.MethodDelete, "/api/v1/cluster/members/"+gone.id, nil, nil)

	eventually(t, "the removed node to fail readiness", func() error {
		if code, _ := gone.request(http.MethodGet, "/api/v1/readyz", nil, nil); code != http.StatusServiceUnavailable {
			return fmt.Errorf("readyz = %d", code)
		}

		return nil
	})

	gone.kill()

	replacement := newNode(t, "replacement")
	cl.nodes = append(cl.followers(nil), replacement)
	cl.join(replacement)

	st, err := replacement.status()
	if err != nil {
		t.Fatal(err)
	}

	replacement.id = st.NodeID

	cl.waitStable()

	leader.must(http.MethodPost, "/api/v1/users",
		map[string]any{"email": "ops@example.com", "password": "correct horse", "role_id": 2}, nil)

	eventually(t, "the user on the replacement", func() error {
		_, err := replacement.request(http.MethodGet, "/api/v1/users/ops@example.com", nil, nil)
		return err
	})
}
