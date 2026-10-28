package db

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func testESME(systemID string, userID int64) ESME {
	return ESME{
		SystemID:     systemID,
		UserID:       userID,
		PasswordHash: "hash-" + systemID,
		AllowedNetworks: []netip.Prefix{
			netip.MustParsePrefix("10.1.0.0/24"), netip.MustParsePrefix("2001:db8::/32"),
			netip.MustParsePrefix("192.0.2.7/32"),
		},
		CreatedAt: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
	}
}

func createTestESME(t *testing.T, d *DB, e ESME) int64 {
	t.Helper()

	id, err := d.CreateESME(context.Background(), e)
	if err != nil {
		t.Fatalf("CreateESME: %v", err)
	}

	return id
}

func TestESMELifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	owner := createTestUser(t, d, "ota@example.com", RoleSubmitter)
	other := createTestUser(t, d, "other@example.com", RoleSubmitter)

	if n, err := d.CountESMEs(ctx); err != nil || n != 0 {
		t.Fatalf("CountESMEs = %d, %v, want 0", n, err)
	}

	want := testESME("ota", owner)
	want.ID = createTestESME(t, d, want)

	got, err := d.GetESMEBySystemID(ctx, "ota")
	if err != nil {
		t.Fatalf("GetESMEBySystemID: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ESME = %+v, want %+v", got, want)
	}

	if _, err := d.GetESMEBySystemID(ctx, "OTA"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetESMEBySystemID of another case = %v, want ErrNotFound", err)
	}

	second := testESME("second", other)
	second.AllowedNetworks = nil
	second.ID = createTestESME(t, d, second)
	second.AllowedNetworks = []netip.Prefix{}

	third := testESME("third", owner)
	third.ID = createTestESME(t, d, third)

	if n, err := d.CountESMEs(ctx); err != nil || n != 3 {
		t.Fatalf("CountESMEs = %d, %v, want 3", n, err)
	}

	if all, err := d.ListESMEs(ctx); err != nil || !reflect.DeepEqual(all, []ESME{want, second, third}) {
		t.Fatalf("ListESMEs = %+v, %v", all, err)
	}

	if mine, err := d.ListESMEsOfUser(ctx, owner); err != nil || !reflect.DeepEqual(mine, []ESME{want, third}) {
		t.Fatalf("ListESMEsOfUser = %+v, %v", mine, err)
	}

	if none, err := d.ListESMEsOfUser(ctx, 42); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("ListESMEsOfUser of an unknown user = %#v, %v, want an empty list", none, err)
	}

	// An empty hash keeps the password; the networks are replaced, in their new order.
	update := want
	update.PasswordHash = ""
	update.AllowedNetworks = []netip.Prefix{netip.MustParsePrefix("192.0.2.7/32"), netip.MustParsePrefix("10.2.0.0/16")}

	if err := d.UpdateESME(ctx, update); err != nil {
		t.Fatalf("UpdateESME: %v", err)
	}

	update.PasswordHash = want.PasswordHash

	if got, err := d.GetESMEBySystemID(ctx, "ota"); err != nil || !reflect.DeepEqual(got, update) {
		t.Fatalf("after UpdateESME = %+v, %v, want %+v", got, err, update)
	}

	update.PasswordHash = "new-hash"
	update.AllowedNetworks = nil

	if err := d.UpdateESME(ctx, update); err != nil {
		t.Fatalf("UpdateESME: %v", err)
	}

	update.AllowedNetworks = []netip.Prefix{}

	if got, err := d.GetESMEBySystemID(ctx, "ota"); err != nil || !reflect.DeepEqual(got, update) {
		t.Fatalf("after UpdateESME = %+v, %v, want %+v", got, err, update)
	}

	if err := d.UpdateESME(ctx, ESME{ID: 42}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateESME of an unknown ID = %v, want ErrNotFound", err)
	}

	if err := d.DeleteESME(ctx, second.ID); err != nil {
		t.Fatalf("DeleteESME: %v", err)
	}

	if err := d.DeleteESME(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteESME twice = %v, want ErrNotFound", err)
	}

	if _, err := d.GetESMEBySystemID(ctx, "second"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetESMEBySystemID after DeleteESME = %v, want ErrNotFound", err)
	}

	if n := count(t, d, `esme_networks`); n != 3 {
		t.Errorf("esme_networks = %d, want 3 (third's only)", n)
	}
}

func TestESMEsAreChecked(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	owner := createTestUser(t, d, "ota@example.com", RoleSubmitter)
	createTestESME(t, d, testESME("ota", owner))

	if _, err := d.CreateESME(ctx, testESME("ota", owner)); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("CreateESME of a taken system_id = %v, want ErrAlreadyExists", err)
	}

	tests := map[string]func(*ESME){
		"empty system_id":   func(e *ESME) { e.SystemID = "" },
		"16 char system_id": func(e *ESME) { e.SystemID = "abcdefghijklmnop" },
		"unknown user":      func(e *ESME) { e.UserID = 42 },
		"a network twice":   func(e *ESME) { e.AllowedNetworks = append(e.AllowedNetworks, e.AllowedNetworks[0]) },
	}

	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			e := testESME("new", owner)
			change(&e)

			if _, err := d.CreateESME(ctx, e); err == nil {
				t.Fatal("CreateESME succeeded")
			}

			if n, err := d.CountESMEs(ctx); err != nil || n != 1 {
				t.Fatalf("CountESMEs = %d, %v; a failed create must store nothing", n, err)
			}

			if n := count(t, d, `esme_networks`); n != 3 {
				t.Fatalf("esme_networks = %d, want 3; a failed create must store no network", n)
			}
		})
	}
}

// smppMessage returns a message that the ESME submitted, as its user.
func smppMessage(esmeID, userID int64, receipt uint8, simAnswer bool, at time.Time) NewMessage {
	m := testMessage(0, false, []byte{0x41, 0x42}, at)
	m.Origin, m.SubmittedBy = OriginSMPP, userID
	m.SMPP = &NewSMPPMessage{ESMEID: esmeID, Receipt: receipt, SIMAnswer: simAnswer, DataCoding: 0x04}

	return m
}

func TestDeleteESMEKeepsItsMessages(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	owner := createTestUser(t, d, "ota@example.com", RoleSubmitter)
	esme := createTestESME(t, d, testESME("ota", owner))

	id, err := d.CreateMessage(ctx, smppMessage(esme, owner, ReceiptFinal, false, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, now, nil, 0); err != nil {
		t.Fatal(err)
	}

	if ids, err := d.SystemIDsFor(ctx, []int64{id}); err != nil || ids[id] != "ota" {
		t.Fatalf("SystemIDsFor = %v, %v", ids, err)
	}

	if n := count(t, d, `smpp_notifications`); n != 1 {
		t.Fatalf("notifications = %d, want 1", n)
	}

	if err := d.DeleteESME(ctx, esme); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{`smpp_messages`, `smpp_notifications`, `esme_networks`} {
		if n := count(t, d, table); n != 0 {
			t.Errorf("%s = %d after deleting the ESME, want 0", table, n)
		}
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil || m.Origin != OriginSMPP || m.SubmittedBy != owner || m.Status != StatusDelivered {
		t.Fatalf("message after deleting its ESME = %+v, %v; want it kept as its user's", m, err)
	}

	if ids, err := d.SystemIDsFor(ctx, []int64{id}); err != nil || len(ids) != 0 {
		t.Fatalf("SystemIDsFor after deleting the ESME = %v, %v, want none", ids, err)
	}
}

func TestDeleteUserDeletesItsESMEs(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	owner := createTestUser(t, d, "ota@example.com", RoleSubmitter)
	other := createTestUser(t, d, "other@example.com", RoleSubmitter)
	esme := createTestESME(t, d, testESME("ota", owner))
	createTestESME(t, d, testESME("other", other))

	id, err := d.CreateMessage(ctx, smppMessage(esme, owner, ReceiptFinal, false, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteUser(ctx, owner); err != nil {
		t.Fatal(err)
	}

	if esmes, err := d.ListESMEs(ctx); err != nil || len(esmes) != 1 || esmes[0].SystemID != "other" {
		t.Fatalf("ListESMEs after deleting the owner = %+v, %v; want only the other user's", esmes, err)
	}

	if n := count(t, d, `smpp_messages`); n != 0 {
		t.Errorf("smpp_messages = %d, want 0", n)
	}

	if m, err := d.GetMessage(ctx, id); err != nil || m.SubmittedBy != 0 || m.Origin != OriginSMPP {
		t.Fatalf("message after deleting its submitter = %+v, %v", m, err)
	}
}

func TestMessageOriginChecks(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	user := createTestUser(t, d, "ota@example.com", RoleSubmitter)
	esme := createTestESME(t, d, testESME("ota", user))

	tests := []struct {
		name    string
		message NewMessage
		wantErr bool
	}{
		{"smpp with a submitter", smppMessage(esme, user, ReceiptNone, false, now), false},
		{"smpp without a submitter", smppMessage(esme, 0, ReceiptNone, false, now), false},
		{"api with a submitter", func() NewMessage {
			m := testMessage(1, false, []byte{0x01}, now)
			m.Origin, m.SubmittedBy = OriginAPI, user

			return m
		}(), false},
		{"mobile with a submitter", func() NewMessage {
			m := testMessage(1, false, []byte{0x01}, now)
			m.SubmittedBy = user

			return m
		}(), true},
		{"unknown origin", func() NewMessage {
			m := testMessage(1, false, []byte{0x01}, now)
			m.Origin = "smtp"

			return m
		}(), true},
		{"smpp of an unknown ESME", smppMessage(42, user, ReceiptNone, false, now), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := count(t, d, `messages`)

			_, err := d.CreateMessage(ctx, tt.message)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CreateMessage = %v, want error %v", err, tt.wantErr)
			}

			want := before + 1
			if tt.wantErr {
				want = before
			}

			if n := count(t, d, `messages`); n != want {
				t.Fatalf("messages = %d, want %d", n, want)
			}
		})
	}
}
