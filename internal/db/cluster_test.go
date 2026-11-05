package db

import (
	"context"
	"errors"
	"strings"
	"testing"
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
