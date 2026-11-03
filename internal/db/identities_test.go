package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const testIssuer = "https://idp.example.com/realms/smsc"

func enableTestOIDC(t *testing.T, d *DB) {
	t.Helper()

	if err := d.UpdateOIDC(context.Background(), testOIDC()); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityLinksAUser(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	enableTestOIDC(t, d)

	id := createTestUser(t, d, "nm@example.com", RoleNetworkManager)

	if _, err := d.GetIdentity(ctx, testIssuer, "sub-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIdentity before linking: %v, want ErrNotFound", err)
	}

	identityID, err := d.CreateIdentity(ctx, id, NewIdentity{Issuer: testIssuer, Subject: "sub-1", Email: "nm@example.com", At: now})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	want := Identity{
		ID: identityID, UserID: id, Issuer: testIssuer, Subject: "sub-1", Email: "nm@example.com",
		CreatedAt: now, LastLoginAt: now,
	}
	if got, err := d.GetIdentity(ctx, testIssuer, "sub-1"); err != nil || got != want {
		t.Fatalf("GetIdentity = %+v, %v, want %+v", got, err, want)
	}

	for name, lookup := range map[string][2]string{
		"another subject":         {testIssuer, "sub-2"},
		"another issuer":          {"https://other.example.com", "sub-1"},
		"the subject in caps":     {testIssuer, "SUB-1"},
		"the issuer with a slash": {testIssuer + "/", "sub-1"},
	} {
		if _, err := d.GetIdentity(ctx, lookup[0], lookup[1]); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetIdentity with %s: %v, want ErrNotFound", name, err)
		}
	}

	later := now.Add(time.Hour)
	if err := d.TouchIdentity(ctx, identityID, "renamed@example.com", later); err != nil {
		t.Fatalf("TouchIdentity: %v", err)
	}

	want.Email, want.LastLoginAt = "renamed@example.com", later
	if got, err := d.GetIdentity(ctx, testIssuer, "sub-1"); err != nil || got != want {
		t.Fatalf("GetIdentity after TouchIdentity = %+v, %v, want %+v", got, err, want)
	}

	if u, err := d.GetUserByID(ctx, id); err != nil || u.Email != "nm@example.com" {
		t.Fatalf("user email after TouchIdentity = %q, %v, want it unchanged", u.Email, err)
	}

	if err := d.TouchIdentity(ctx, identityID+1, "", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TouchIdentity of an unknown identity: %v, want ErrNotFound", err)
	}

	if err := d.DeleteUser(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetIdentity(ctx, testIssuer, "sub-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIdentity after deleting the user: %v, want ErrNotFound", err)
	}

	other := createTestUser(t, d, "other@example.com", RoleReadOnly)
	if _, err := d.CreateIdentity(ctx, other, NewIdentity{Issuer: testIssuer, Subject: "sub-1", At: now}); err != nil {
		t.Fatalf("CreateIdentity with the subject of a deleted user: %v", err)
	}
}

func TestUserHasIdentityOfTheCurrentIssuer(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	enableTestOIDC(t, d)

	id := createTestUser(t, d, "a@example.com", RoleReadOnly)

	hasIdentity := func() bool {
		u, err := d.GetUserByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}

		return u.HasIdentity
	}

	if hasIdentity() {
		t.Fatal("HasIdentity before linking")
	}

	if _, err := d.CreateIdentity(ctx, id, NewIdentity{Issuer: testIssuer, Subject: "sub", At: now}); err != nil {
		t.Fatal(err)
	}

	if !hasIdentity() {
		t.Fatal("no HasIdentity after linking")
	}

	o := testOIDC()
	o.Issuer = "https://other.example.com"

	if err := d.UpdateOIDC(ctx, o); err != nil {
		t.Fatal(err)
	}

	if hasIdentity() {
		t.Fatal("HasIdentity with a link to another issuer")
	}

	if _, err := d.GetIdentity(ctx, testIssuer, "sub"); err != nil {
		t.Fatalf("link to the previous issuer: %v, want kept", err)
	}

	if err := d.UpdateOIDC(ctx, testOIDC()); err != nil {
		t.Fatal(err)
	}

	if !hasIdentity() {
		t.Fatal("no HasIdentity after going back to the issuer")
	}

	if err := d.DeleteIdentities(ctx, id); err != nil {
		t.Fatalf("DeleteIdentities: %v", err)
	}

	if hasIdentity() {
		t.Fatal("HasIdentity after DeleteIdentities")
	}
}

func TestIdentityLinksOnce(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	a := createTestUser(t, d, "a@example.com", RoleReadOnly)
	b := createTestUser(t, d, "b@example.com", RoleReadOnly)

	if _, err := d.CreateIdentity(ctx, a, NewIdentity{Issuer: testIssuer, Subject: "sub-a", At: now}); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		user    int64
		subject string
	}{
		"the same subject for another user":   {b, "sub-a"},
		"another subject for the linked user": {a, "sub-b"},
		"the same link again":                 {a, "sub-a"},
	} {
		_, err := d.CreateIdentity(ctx, tc.user, NewIdentity{Issuer: testIssuer, Subject: tc.subject, At: now})
		if !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("CreateIdentity with %s: %v, want ErrAlreadyExists", name, err)
		}
	}

	if _, err := d.CreateIdentity(ctx, a, NewIdentity{Issuer: "https://other.example.com", Subject: "sub-a", At: now}); err != nil {
		t.Errorf("CreateIdentity with another issuer: %v", err)
	}
}

func TestIdentityIsChecked(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "a@example.com", RoleReadOnly)

	for name, i := range map[string]NewIdentity{
		"no issuer":                {Subject: "sub", At: now},
		"no subject":               {Issuer: testIssuer, At: now},
		"a subject too long":       {Issuer: testIssuer, Subject: strings.Repeat("s", 256), At: now},
		"a subject over 255 bytes": {Issuer: testIssuer, Subject: strings.Repeat("é", 128), At: now},
	} {
		if _, err := d.CreateIdentity(ctx, id, i); err == nil {
			t.Errorf("CreateIdentity with %s: no error", name)
		}
	}

	if _, err := d.CreateIdentity(ctx, id+1, NewIdentity{Issuer: testIssuer, Subject: "sub", At: now}); err == nil {
		t.Error("CreateIdentity for an unknown user: no error")
	}

	if _, err := d.CreateIdentity(ctx, id, NewIdentity{Issuer: testIssuer, Subject: strings.Repeat("s", 255), At: now}); err != nil {
		t.Errorf("CreateIdentity with a 255-character subject: %v", err)
	}
}

func TestCreateUserWithIdentity(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	enableTestOIDC(t, d)

	userID, identityID, err := d.CreateUserWithIdentity(ctx, NewUser{Email: "new@example.com", Role: RoleReadOnly},
		NewIdentity{Issuer: testIssuer, Subject: "sub-new", Email: "new@example.com", At: now}, 2)
	if err != nil {
		t.Fatalf("CreateUserWithIdentity: %v", err)
	}

	if i, err := d.GetIdentity(ctx, testIssuer, "sub-new"); err != nil || i.ID != identityID || i.UserID != userID {
		t.Fatalf("GetIdentity = %+v, %v, want identity %d of user %d", i, err, identityID, userID)
	}

	want := User{ID: userID, Email: "new@example.com", Role: RoleReadOnly, HasIdentity: true}
	if got, err := d.GetUserByID(ctx, userID); err != nil || got != want {
		t.Fatalf("GetUserByID = %+v, %v, want %+v", got, err, want)
	}

	for name, tc := range map[string]struct {
		email, subject string
		want           error
	}{
		"a taken email":   {"NEW@example.com", "sub-other", ErrAlreadyExists},
		"a taken subject": {"other@example.com", "sub-new", ErrAlreadyExists},
	} {
		_, _, err := d.CreateUserWithIdentity(ctx, NewUser{Email: tc.email, Role: RoleReadOnly},
			NewIdentity{Issuer: testIssuer, Subject: tc.subject, At: now}, 2)
		if !errors.Is(err, tc.want) {
			t.Errorf("CreateUserWithIdentity with %s: %v, want %v", name, err, tc.want)
		}
	}

	if n, err := d.CountUsers(ctx); err != nil || n != 1 {
		t.Fatalf("CountUsers = %d, %v, want 1", n, err)
	}

	if _, _, err := d.CreateUserWithIdentity(ctx, NewUser{Email: "second@example.com", Role: RoleReadOnly},
		NewIdentity{Issuer: testIssuer, Subject: "sub-second", At: now}, 2); err != nil {
		t.Fatalf("CreateUserWithIdentity up to the limit: %v", err)
	}

	_, _, err = d.CreateUserWithIdentity(ctx, NewUser{Email: "third@example.com", Role: RoleReadOnly},
		NewIdentity{Issuer: testIssuer, Subject: "sub-third", At: now}, 2)
	if !errors.Is(err, ErrTooManyUsers) {
		t.Fatalf("CreateUserWithIdentity past the limit: %v, want ErrTooManyUsers", err)
	}

	if n, err := d.CountUsers(ctx); err != nil || n != 2 {
		t.Fatalf("CountUsers = %d, %v, want 2", n, err)
	}
}

func TestOIDCSessionNeedsAnIdentity(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "a@example.com", RoleReadOnly)

	identityID, err := d.CreateIdentity(ctx, id, NewIdentity{Issuer: testIssuer, Subject: "sub", At: now})
	if err != nil {
		t.Fatal(err)
	}

	for name, s := range map[string]NewSession{
		"an OIDC session without an identity":      {Method: SessionOIDC},
		"a password session with an identity":      {Method: SessionPassword, IdentityID: identityID},
		"an OIDC session with an unknown identity": {Method: SessionOIDC, IdentityID: identityID + 1},
	} {
		s.UserID, s.TokenHash, s.CreatedAt, s.ExpiresAt = id, []byte(name), now, now.Add(time.Hour)
		if _, err := d.CreateSession(ctx, s, 10); err == nil {
			t.Errorf("CreateSession with %s: no error", name)
		}
	}

	if _, err := d.CreateSession(ctx, NewSession{
		UserID: id, TokenHash: []byte("oidc"), Method: SessionOIDC, IdentityID: identityID, CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, 10); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if s, err := d.GetSessionByTokenHash(ctx, []byte("oidc")); err != nil || s.IdentityID != identityID {
		t.Fatalf("session identity = %d, %v, want %d", s.IdentityID, err, identityID)
	}

	if err := d.DeleteIdentities(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetSessionByTokenHash(ctx, []byte("oidc")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session after unlinking: %v, want ErrNotFound", err)
	}
}
