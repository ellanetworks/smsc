package db

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/settings"
)

func TestSetDisplayName(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	const id = "01a1ac75-e354-762b-b38f-013c0a7ca824"

	if err := d.RegisterMember(ctx, Member{NodeID: id, Address: "192.0.2.1:7000", Version: "v"}); err != nil {
		t.Fatal(err)
	}

	displayName := func() string {
		t.Helper()

		m, err := d.Member(ctx, id)
		if err != nil {
			t.Fatal(err)
		}

		return m.DisplayName
	}

	if got := displayName(); got != "" {
		t.Fatalf("display name of a new member = %q", got)
	}

	if err := d.SetDisplayName(ctx, id, "paris-1"); err != nil {
		t.Fatal(err)
	}

	if got := displayName(); got != "paris-1" {
		t.Fatalf("display name = %q", got)
	}

	if err := d.RegisterMember(ctx, Member{NodeID: id, Address: "192.0.2.2:7000", Version: "v"}); err != nil {
		t.Fatal(err)
	}

	if got := displayName(); got != "paris-1" {
		t.Fatalf("display name after a re-registration = %q", got)
	}

	if err := d.SetDisplayName(ctx, id, strings.Repeat("a", MaxDisplayNameLength+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetDisplayName of a long name = %v", err)
	}

	if err := d.SetDisplayName(ctx, "01a1ac75-e354-762b-b38f-0123456789ab", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetDisplayName of an unknown member = %v", err)
	}

	if err := d.SetDisplayName(ctx, id, ""); err != nil {
		t.Fatal(err)
	}

	if got := displayName(); got != "" {
		t.Fatalf("display name after clearing = %q", got)
	}
}

func TestDiameterLabels(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	const (
		a = "01a1ac75-e354-762b-b38f-000000000001"
		b = "01a1ac75-e354-762b-b38f-000000000002"
		c = "01a1ac75-e354-762b-b38f-000000000003"
	)

	label := func(id string) (string, bool) {
		t.Helper()

		m, err := d.Member(ctx, id)
		if err != nil {
			t.Fatal(err)
		}

		return m.DiameterLabel, m.LabelGenerated
	}

	if err := d.RegisterMember(ctx, Member{NodeID: a, Address: "192.0.2.1:7000", Version: "v"}); err != nil {
		t.Fatal(err)
	}

	if l, generated := label(a); l != settings.NodeLabel(a) || !generated {
		t.Fatalf("label without a configured one = %q, %t", l, generated)
	}

	if err := d.RegisterMember(ctx, Member{NodeID: b, Address: "192.0.2.2:7000", Version: "v", DiameterLabel: "smsc-x"}); err != nil {
		t.Fatal(err)
	}

	if l, generated := label(b); l != "smsc-x" || generated {
		t.Fatalf("configured label = %q, %t", l, generated)
	}

	err := d.RegisterMember(ctx, Member{NodeID: c, Address: "192.0.2.3:7000", Version: "v", DiameterLabel: "smsc-x"})
	if !errors.Is(err, settings.ErrConflict) || !strings.Contains(err.Error(), b) {
		t.Fatalf("RegisterMember with a member's label = %v, want a conflict naming %s", err, b)
	}

	if err := d.CreateJoinToken(ctx, "t1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	err = d.AdmitMember(ctx, "t1", Member{
		NodeID: c, Address: "192.0.2.3:7000", Fingerprint: "f", Version: "v",
		DiameterLabel: "smsc-x",
	})
	if !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("AdmitMember with a member's label = %v, want a conflict", err)
	}

	err = d.RegisterMember(ctx, Member{NodeID: b, Address: "192.0.2.2:7000", Version: "v", DiameterLabel: settings.NodeLabel(a)})
	if !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("relabeling to a member's generated label = %v, want a conflict", err)
	}

	err = d.RegisterMember(ctx, Member{NodeID: b, Address: "192.0.2.2:7000", Version: "v", DiameterLabel: "Smsc-X"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("RegisterMember with an invalid label = %v, want %v", err, ErrInvalid)
	}

	if err := d.RemoveMember(ctx, b, RemovedDrained); err != nil {
		t.Fatal(err)
	}

	if err := d.AdmitMember(ctx, "t1", Member{
		NodeID: c, Address: "192.0.2.3:7000", Fingerprint: "f", Version: "v",
		DiameterLabel: "smsc-x",
	}); err != nil {
		t.Fatalf("AdmitMember with a freed label: %v", err)
	}

	if err := d.RegisterMember(ctx, Member{
		NodeID: c, Address: "192.0.2.3:7000", Fingerprint: "f", Version: "v",
		DiameterLabel: "smsc-y",
	}); err != nil {
		t.Fatalf("relabeling: %v", err)
	}

	s, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{settings.NodeLabel(a), "smsc-y"}; !slices.Equal(s.Labels, want) {
		t.Fatalf("Labels = %v, want %v", s.Labels, want)
	}

	p := testPeer()
	p.Host = s.Operator.Host("smsc-z")

	if _, err := d.CreatePeer(ctx, p); err != nil {
		t.Fatal(err)
	}

	err = d.RegisterMember(ctx, Member{
		NodeID: c, Address: "192.0.2.3:7000", Fingerprint: "f", Version: "v",
		DiameterLabel: "smsc-z",
	})
	if !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("relabeling to a peer's host = %v, want a conflict", err)
	}
}

func TestRemovalLeavesATombstone(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	const (
		a = "01a1ac75-e354-762b-b38f-000000000001"
		b = "01a1ac75-e354-762b-b38f-000000000002"
	)

	if err := d.RegisterMember(ctx, Member{NodeID: a, Address: "192.0.2.1:7000", Fingerprint: "fa", Version: "v"}); err != nil {
		t.Fatal(err)
	}

	if err := d.RegisterMember(ctx, Member{NodeID: b, Address: "192.0.2.2:7000", Fingerprint: "fb", Version: "v"}); err != nil {
		t.Fatal(err)
	}

	if err := d.RemoveMember(ctx, b, RemovedForced); err != nil {
		t.Fatal(err)
	}

	tombstones, err := d.Tombstones(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(tombstones) != 1 || tombstones[0].NodeID != b || tombstones[0].Fingerprint != "fb" ||
		tombstones[0].Reason != RemovedForced || tombstones[0].RemovedAt.IsZero() {
		t.Fatalf("tombstones = %+v", tombstones)
	}

	if err := d.RemoveMember(ctx, b, RemovedForced); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing a removed member = %v, want %v", err, ErrNotFound)
	}

	err = d.RegisterMember(ctx, Member{NodeID: b, Address: "192.0.2.2:7000", Fingerprint: "fb2", Version: "v"})
	if !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("registering a removed node again = %v, want a conflict", err)
	}

	if err := d.CreateJoinToken(ctx, "t1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	err = d.AdmitMember(ctx, "t1", Member{NodeID: b, Address: "192.0.2.2:7000", Fingerprint: "fb2", Version: "v"})
	if !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("admitting a removed node again = %v, want a conflict", err)
	}

	if err := d.RemoveMember(ctx, a, "gone"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("removal with an unknown reason = %v, want %v", err, ErrInvalid)
	}
}

func TestRemoveMemberAcceptsTheOlderPayload(t *testing.T) {
	var p removeMember

	if err := json.Unmarshal([]byte(`"01a1ac75-e354-762b-b38f-000000000001"`), &p); err != nil {
		t.Fatal(err)
	}

	if p.NodeID != "01a1ac75-e354-762b-b38f-000000000001" || p.Reason != RemovedForced {
		t.Fatalf("payload = %+v", p)
	}
}
