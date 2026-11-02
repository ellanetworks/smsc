package ratelimit

import (
	"net/netip"
	"sync"
	"time"
)

const MaxKeys = 10_000

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

func New(limit int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{limit: limit, window: window, now: now, windows: map[string]*rateWindow{}}
}

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

func (l *Limiter) Exceeded(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.current(key)

	return w == nil || w.count >= l.limit
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.windows, key)
}

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

func Key(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}

	return netip.PrefixFrom(addr, 64).Masked().String()
}
