package ratelimit

import (
	"strconv"
	"testing"
	"time"
)

func TestRateLimiterIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := New(1, time.Minute, func() time.Time { return now })

	for i := range MaxKeys {
		if !l.Allow(strconv.Itoa(i)) {
			t.Fatalf("key %d refused", i)
		}
	}

	if l.Allow("1") {
		t.Fatal("a tracked key past its limit was allowed")
	}

	if !l.Allow("one more") {
		t.Fatal("a new key was refused when the limiter was full")
	}

	if len(l.windows) != MaxKeys || l.order.Len() != MaxKeys {
		t.Fatalf("limiter holds %d keys, want %d", len(l.windows), MaxKeys)
	}

	if _, ok := l.windows["0"]; ok {
		t.Fatal("the oldest key was not evicted")
	}

	if l.Allow("1") {
		t.Fatal("a tracked key was reset by the eviction of another")
	}
}

func TestRateLimiterSweepsExpiredKeys(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := New(1, time.Minute, func() time.Time { return now })

	l.Allow("a")

	now = now.Add(30 * time.Second)

	l.Allow("b")

	now = now.Add(30 * time.Second)

	if !l.Allow("a") {
		t.Fatal("a key was refused after its window was over")
	}

	if _, ok := l.windows["b"]; !ok || len(l.windows) != 2 {
		t.Fatalf("limiter holds %d keys, want a and b", len(l.windows))
	}

	if l.Allow("b") {
		t.Fatal("a key was allowed past its limit within its window")
	}
}

func TestExceededDoesNotCreateState(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := New(2, time.Hour, func() time.Time { return now })

	for i := range 2 * MaxKeys {
		if l.Exceeded(strconv.Itoa(i)) {
			t.Fatalf("key %d with no failures is exceeded", i)
		}
	}

	if len(l.windows) != 0 {
		t.Fatalf("Exceeded created %d keys", len(l.windows))
	}

	l.Allow("a")

	if l.Exceeded("a") {
		t.Fatal("exceeded below the limit")
	}

	l.Allow("a")

	if !l.Exceeded("a") {
		t.Fatal("not exceeded at the limit")
	}

	now = now.Add(time.Hour)

	if l.Exceeded("a") {
		t.Fatal("exceeded after the window")
	}
}

func TestFullLimiterAdmitsNewKeys(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l := New(100, time.Hour, func() time.Time { return now })

	for i := range 3 * MaxKeys {
		if !l.Allow("junk" + strconv.Itoa(i)) {
			t.Fatalf("junk key %d refused", i)
		}

		if i%MaxKeys == 0 {
			now = now.Add(time.Minute)
		}
	}

	if l.Exceeded("admin@example.com") || !l.Allow("admin@example.com") {
		t.Fatal("a new key was refused when the limiter was full")
	}
}
