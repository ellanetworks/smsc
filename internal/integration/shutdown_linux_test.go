//go:build integration && linux

package integration

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestStoppingLeaderHandsOverBeforeItsDeliveriesEnd(t *testing.T) {
	cl := newCluster(t)
	c := newCore(t, cl.nodes)
	leader := cl.leader()
	via := cl.followers(leader)[0]

	c.delay.Store(int64(5 * time.Second))

	slow, err := c.submit(via, 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	eventually(t, "the leader delivering to the first recipient", func() error {
		if _, ok := c.firstDelivery(leader, 2); !ok {
			return errors.New("not yet")
		}

		return nil
	})

	exited := make(chan time.Time, 1)

	go func() {
		<-leader.exited

		exited <- time.Now()
	}()

	stopping := time.Now()

	_ = leader.cmd.Process.Signal(syscall.SIGTERM)

	c.delay.Store(0)

	var fast int64

	eventually(t, "a message accepted during the shutdown", func() error {
		fast, err = c.submit(via, 3, 4)
		return err
	})

	var served time.Time

	eventually(t, "a new leader delivering to the second recipient", func() error {
		for _, n := range cl.nodes {
			if n == leader {
				continue
			}

			if at, ok := c.firstDelivery(n, 4); ok {
				served = at
				return nil
			}
		}

		return errors.New("not yet")
	})

	gone := <-exited

	t.Logf("new leader delivered %s after SIGTERM; old leader exited %s after SIGTERM",
		served.Sub(stopping).Round(time.Millisecond), gone.Sub(stopping).Round(time.Millisecond))

	if !served.Before(gone) {
		t.Fatal("the new leader only delivered after the old leader exited")
	}

	eventually(t, "both messages delivered", func() error { return c.deliveredAll([]int64{slow, fast}) })

	if o := c.overlaps(); len(o) > 0 {
		t.Fatalf("two SMS-DELIVERs outstanding at once to %v", o)
	}
}
