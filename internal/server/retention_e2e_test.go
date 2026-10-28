package server_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/server"
	"github.com/ellanetworks/smsc/internal/settings"
)

func TestRetentionPurgesFinishedMessages(t *testing.T) {
	core := newFakeCore(t, alice, bob)

	var ahead atomic.Int64

	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.PurgeNow = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	cfg.PurgeInterval = 10 * time.Millisecond

	s := startSMSC(t, cfg)
	core.connect(s)

	core.submit(alice, bob, "hi bob")
	s.waitForStatus(t, 1, db.StatusDelivered)

	// Passes within the retention keep the message.
	time.Sleep(50 * time.Millisecond)
	s.message(t, 1)

	ahead.Store(int64(31 * 24 * time.Hour))

	eventually(t, "the message to be purged", func() bool {
		return s.api(t, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", 1), nil, nil) == http.StatusNotFound
	})

	if got := values(parseFamilies(t, scrape(t, s)))["ellasmsc_messages_purged_total"]; got != 1 {
		t.Fatalf("ellasmsc_messages_purged_total = %v, want 1", got)
	}
}

func TestRetentionDoesNotStallDelivery(t *testing.T) {
	const old = 5 * server.PurgeBatch

	path := filepath.Join(t.TempDir(), "smsc.db")
	finished := time.Now().Add(-2 * 24 * time.Hour)

	store, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	messages := make([]db.NewMessage, old)
	for i := range messages {
		messages[i] = db.NewMessage{
			Originator: db.Address{Digits: "15550001111", TypeOfNumber: 1, NumberingPlan: 1},
			Recipient:  db.Address{Digits: "15559990000", TypeOfNumber: 1, NumberingPlan: 1},
			MSISDN:     "15559990000", Origin: db.OriginAPI,
			TPDU:        []byte{0x01, 0x00, 0x0b, 0x91, 0x51, 0x55, 0x99, 0x09, 0x00, 0xf0, 0x00, 0x00, 0x02, 0xe8, 0x34},
			SubmittedAt: finished, ExpiresAt: finished.Add(time.Hour),
		}
	}

	ids, err := store.CreateMessages(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range ids {
		if err := store.CompleteMessage(context.Background(), id, db.StatusDelivered, finished, nil); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	core := newFakeCore(t, alice, bob)

	var purging atomic.Bool

	cfg := testConfig(t, path)
	cfg.Settings.Retention = settings.Retention{Messages: settings.MinRetention}
	cfg.PurgeNow = func() time.Time {
		if !purging.Load() {
			return finished
		}

		return time.Now()
	}
	cfg.PurgeInterval = time.Millisecond

	s := startSMSC(t, cfg)
	core.connect(s)

	purging.Store(true)
	core.submit(alice, bob, "hi bob")
	s.waitForStatus(t, int64(old+1), db.StatusDelivered)

	eventually(t, "the old messages to be purged", func() bool {
		var list api.ListMessagesResponse
		s.api(t, http.MethodGet, "/api/v1/messages", nil, &list)

		return list.TotalCount == 1
	})
}
