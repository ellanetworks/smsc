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

	if l.Allow("one more") || !l.Exceeded("one more") {
		t.Fatal("a key past the bound was allowed")
	}

	if l.Allow("0") {
		t.Fatal("a tracked key past its limit was allowed")
	}

	now = now.Add(time.Minute)

	if !l.Allow("one more") {
		t.Fatal("a new key was refused after the windows were over")
	}
}
