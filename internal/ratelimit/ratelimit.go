// Package ratelimit limits events per key in fixed windows, such as logins per client of the API and binds per
// client of the SMPP listener.
package ratelimit

import (
	"net/netip"
	"sync"
	"time"
)

// MaxKeys bounds the clients that a rate limiter tracks. Once it tracks that many in their windows, it refuses
// new ones, rather than grow without bound.
const MaxKeys = 10_000

// Limiter allows each key at most limit events per fixed window. It forgets the windows that are over once per
// window, as it is called, so that it needs no goroutine.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]*rateWindow
	swept   time.Time
}

type rateWindow struct {
	start time.Time
	count int
}

// New returns a Limiter of limit events per window.
func New(limit int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{limit: limit, window: window, now: now, windows: map[string]*rateWindow{}}
}

// Allow counts an event of key, and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.current(key)
	if w == nil {
		return false
	}

	w.count++

	return w.count <= l.limit
}

// Exceeded reports whether key has reached its limit, without counting an event.
func (l *Limiter) Exceeded(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.current(key)

	return w == nil || w.count >= l.limit
}

// Reset forgets the events of key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.windows, key)
}

// current returns the window of key, a new one if its last is over, or nil when it tracks too many keys to add it.
func (l *Limiter) current(key string) *rateWindow {
	now := l.now()

	if now.Sub(l.swept) >= l.window {
		for k, w := range l.windows {
			if now.Sub(w.start) >= l.window {
				delete(l.windows, k)
			}
		}

		l.swept = now
	}

	if w, ok := l.windows[key]; ok && now.Sub(w.start) < l.window {
		return w
	}

	if _, ok := l.windows[key]; !ok && len(l.windows) >= MaxKeys {
		return nil
	}

	w := &rateWindow{start: now}
	l.windows[key] = w

	return w
}

// Key is the key of a client address for rate limiting: the IPv4 address, or the /64 of an IPv6 address, since a
// single host commonly has a whole /64.
func Key(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}

	return netip.PrefixFrom(addr, 64).Masked().String()
}
