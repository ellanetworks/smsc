package ratelimit

import (
	"container/list"
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
	windows map[string]*list.Element
	order   *list.List
}

type rateWindow struct {
	key   string
	start time.Time
	count int
}

func New(limit int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{limit: limit, window: window, now: now, windows: map[string]*list.Element{}, order: list.New()}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	e, ok := l.windows[key]
	if !ok {
		if l.order.Len() >= MaxKeys {
			l.remove(l.order.Front())
		}

		e = l.order.PushBack(&rateWindow{key: key, start: now})
		l.windows[key] = e
	}

	w := e.Value.(*rateWindow)
	w.count++

	return w.count <= l.limit
}

func (l *Limiter) Exceeded(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.windows[key]
	if !ok {
		return false
	}

	w := e.Value.(*rateWindow)

	return l.now().Sub(w.start) < l.window && w.count >= l.limit
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.windows[key]; ok {
		l.remove(e)
	}
}

func (l *Limiter) sweep(now time.Time) {
	for e := l.order.Front(); e != nil && now.Sub(e.Value.(*rateWindow).start) >= l.window; e = l.order.Front() {
		l.remove(e)
	}
}

func (l *Limiter) remove(e *list.Element) {
	delete(l.windows, e.Value.(*rateWindow).key)
	l.order.Remove(e)
}

func Key(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}

	return netip.PrefixFrom(addr, 64).Masked().String()
}
