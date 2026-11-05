package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/hashicorp/raft"
)

const (
	maxBatchCommands = 256
	maxBatchBytes    = 512 * 1024
	maxInFlight      = 64
	queueLength      = 4096
	applyTimeout     = 30 * time.Second
	linger           = 2 * time.Millisecond
)

var ErrTooLarge = errors.New("command too large")

type request struct {
	cmds    []db.Command
	size    int
	results []db.Result
	err     error
	done    chan struct{}
}

type batcher struct {
	raft  *raft.Raft
	now   func() time.Time
	queue chan *request
	slots chan struct{}
	stop  chan struct{}
	wg    sync.WaitGroup
	last  int64
}

func newBatcher(r *raft.Raft, now func() time.Time) *batcher {
	b := &batcher{
		raft:  r,
		now:   now,
		queue: make(chan *request, queueLength),
		slots: make(chan struct{}, maxInFlight),
		stop:  make(chan struct{}),
	}

	b.wg.Add(1)

	go b.run()

	return b
}

func (b *batcher) propose(ctx context.Context, cmds []db.Command) ([]db.Result, error) {
	size := 0
	for _, c := range cmds {
		size += len(c.Payload) + 64
	}

	if size > maxBatchBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}

	req := &request{cmds: cmds, size: size, done: make(chan struct{})}

	select {
	case b.queue <- req:
	default:
		return nil, db.ErrBusy
	}

	select {
	case <-req.done:
		return req.results, req.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *batcher) close() {
	close(b.stop)
	b.wg.Wait()
}

func (b *batcher) run() {
	defer b.wg.Done()

	for {
		var first *request

		select {
		case <-b.stop:
			return
		case first = <-b.queue:
		}

		batch := []*request{first}
		count, size := len(first.cmds), first.size

		var wait <-chan time.Time

	fill:
		for count < maxBatchCommands {
			var next *request

			if wait == nil {
				select {
				case next = <-b.queue:
					wait = time.After(linger)
				default:
					break fill
				}
			} else {
				select {
				case next = <-b.queue:
				case <-wait:
					break fill
				case <-b.stop:
					break fill
				}
			}

			if count+len(next.cmds) > maxBatchCommands || size+next.size > maxBatchBytes {
				b.send(batch)
				batch, count, size = nil, 0, 0
			}

			batch = append(batch, next)
			count += len(next.cmds)
			size += next.size
		}

		b.send(batch)
	}
}

func (b *batcher) send(batch []*request) {
	if len(batch) == 0 {
		return
	}

	b.slots <- struct{}{}

	b.last = max(b.last+1, b.now().UnixNano())

	var cmds []db.Command
	for _, r := range batch {
		cmds = append(cmds, r.cmds...)
	}

	entry, err := db.EncodeBatch(db.Batch{LeaderTime: b.last, Commands: cmds})
	if err != nil {
		<-b.slots

		finish(batch, nil, err)

		return
	}

	future := b.raft.Apply(entry, applyTimeout)

	b.wg.Add(1)

	go func() {
		defer b.wg.Done()
		defer func() { <-b.slots }()

		if err := future.Error(); err != nil {
			finish(batch, nil, err)
			return
		}

		results, ok := future.Response().([]db.Result)
		if !ok || len(results) != len(cmds) {
			finish(batch, nil, fmt.Errorf("unexpected apply response %T", future.Response()))
			return
		}

		finish(batch, results, nil)
	}()
}

func finish(batch []*request, results []db.Result, err error) {
	offset := 0

	for _, r := range batch {
		if err != nil {
			r.err = err
		} else {
			r.results = results[offset : offset+len(r.cmds)]
		}

		offset += len(r.cmds)

		close(r.done)
	}
}
