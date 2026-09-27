package diameter

import (
	"strings"
	"sync"
	"time"
)

const duplicateWindow = 4 * time.Minute

type duplicateKey struct {
	originHost string
	endToEndID uint32
}

type duplicateEntry struct {
	done    chan struct{}
	answer  *Message
	expires time.Time
}

type duplicateCache struct {
	mu        sync.Mutex
	entries   map[duplicateKey]*duplicateEntry
	lastSweep time.Time
}

func (d *duplicateCache) begin(req *Message) (duplicateKey, *duplicateEntry, bool) {
	host, _ := req.Find(AVPOriginHost, 0)
	key := duplicateKey{originHost: strings.ToLower(host.String()), endToEndID: req.EndToEndID}
	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.entries == nil {
		d.entries = make(map[duplicateKey]*duplicateEntry)
	}

	if now.Sub(d.lastSweep) > time.Minute {
		for k, e := range d.entries {
			if !e.expires.IsZero() && now.After(e.expires) {
				delete(d.entries, k)
			}
		}

		d.lastSweep = now
	}

	if e, ok := d.entries[key]; ok && (e.expires.IsZero() || now.Before(e.expires)) {
		return key, e, false
	}

	e := &duplicateEntry{done: make(chan struct{})}
	d.entries[key] = e

	return key, e, true
}

func (d *duplicateCache) finish(key duplicateKey, e *duplicateEntry, answer *Message) {
	d.mu.Lock()
	e.answer = answer
	e.expires = time.Now().Add(duplicateWindow)
	d.entries[key] = e
	d.mu.Unlock()

	close(e.done)
}

func (e *duplicateEntry) answerFor(req *Message) *Message {
	ans := *e.answer
	ans.HopByHopID = req.HopByHopID

	return &ans
}
