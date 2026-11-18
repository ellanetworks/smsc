package oidc

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ellanetworks/smsc/internal/db"
)

func fields(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()

	var f map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		t.Fatal(err)
	}

	return f
}

func TestClaims(t *testing.T) {
	c := &Client{settings: valid()}

	for name, tc := range map[string]struct {
		in       string
		verified Verified
		groups   []string
		overage  bool
	}{
		"verified":               {`{"sub": "s", "email_verified": true}`, VerifiedTrue, nil, false},
		"verified as a string":   {`{"sub": "s", "email_verified": "true"}`, VerifiedTrue, nil, false},
		"not verified":           {`{"sub": "s", "email_verified": false}`, VerifiedFalse, nil, false},
		"not verified as string": {`{"sub": "s", "email_verified": "false"}`, VerifiedFalse, nil, false},
		"verified as a number":   {`{"sub": "s", "email_verified": 1}`, VerifiedFalse, nil, false},
		"no email_verified":      {`{"sub": "s"}`, VerifiedUnknown, nil, false},
		"email_verified null":    {`{"sub": "s", "email_verified": null}`, VerifiedUnknown, nil, false},
		"groups null":            {`{"sub": "s", "groups": null}`, VerifiedUnknown, nil, false},
		"one group":              {`{"sub": "s", "groups": "a"}`, VerifiedUnknown, []string{"a"}, false},
		"groups":                 {`{"sub": "s", "groups": ["a", "b"]}`, VerifiedUnknown, []string{"a", "b"}, false},
		"overage": {
			`{"sub": "s", "_claim_names": {"groups": "src1"}, "_claim_sources": {"src1": {"endpoint": "https://x"}}}`,
			VerifiedUnknown, nil, true,
		},
	} {
		cl, err := c.claims(fields(t, tc.in), ErrToken)
		if err != nil || cl.Subject != "s" || cl.EmailVerified != tc.verified || !slices.Equal(cl.Groups, tc.groups) ||
			cl.GroupsOverage != tc.overage {
			t.Errorf("%s: %+v, %v", name, cl, err)
		}
	}

	for name, in := range map[string]string{
		"groups as a number": `{"sub": "s", "groups": 1}`,
		"email as a number":  `{"sub": "s", "email": 1}`,
	} {
		if _, err := c.claims(fields(t, in), ErrToken); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestTrustsEmail(t *testing.T) {
	for name, tc := range map[string]struct {
		email    string
		verified Verified
		trust    bool
		want     bool
	}{
		"verified":                      {"a@example.com", VerifiedTrue, false, true},
		"not verified":                  {"a@example.com", VerifiedFalse, false, false},
		"not verified, trusting":        {"a@example.com", VerifiedFalse, true, false},
		"unknown":                       {"a@example.com", VerifiedUnknown, false, false},
		"unknown, trusting":             {"a@example.com", VerifiedUnknown, true, true},
		"no email, verified":            {"", VerifiedTrue, true, false},
		"a blank email, trusting":       {" ", VerifiedUnknown, true, false},
		"verified, trusting":            {"a@example.com", VerifiedTrue, true, true},
		"not verified as a string":      {"a@example.com", VerifiedFalse, true, false},
		"unknown, trusting, no address": {"", VerifiedUnknown, true, false},
	} {
		o := valid()
		o.TrustUnverifiedEmail = tc.trust

		c := &Client{settings: o}
		if got := c.TrustsEmail(Claims{Email: tc.email, EmailVerified: tc.verified}); got != tc.want {
			t.Errorf("%s: %t, want %t", name, got, tc.want)
		}
	}
}

func TestMergeUserInfo(t *testing.T) {
	c := &Client{settings: valid()}

	for name, tc := range map[string]struct {
		idToken, userInfo string
		want              Claims
	}{
		"everything from UserInfo": {
			`{"sub": "s"}`,
			`{"sub": "s", "email": "a@example.com", "email_verified": true, "groups": ["g"]}`,
			Claims{Subject: "s", Email: "a@example.com", EmailVerified: VerifiedTrue, Groups: []string{"g"}},
		},
		"the ID token first": {
			`{"sub": "s", "email": "a@example.com", "email_verified": false, "groups": ["g"]}`,
			`{"sub": "s", "email": "b@example.com", "email_verified": true, "groups": ["h"]}`,
			Claims{Subject: "s", Email: "a@example.com", EmailVerified: VerifiedFalse, Groups: []string{"g"}},
		},
		"email_verified for the same email": {
			`{"sub": "s", "email": "a@example.com"}`,
			`{"sub": "s", "email": "a@example.com", "email_verified": true}`,
			Claims{Subject: "s", Email: "a@example.com", EmailVerified: VerifiedTrue},
		},
		"email_verified for another email": {
			`{"sub": "s", "email": "a@example.com"}`,
			`{"sub": "s", "email": "b@example.com", "email_verified": true}`,
			Claims{Subject: "s", Email: "a@example.com", EmailVerified: VerifiedUnknown},
		},
		"no groups in the ID token": {
			`{"sub": "s", "email": "a@example.com", "email_verified": true, "groups": null}`,
			`{"sub": "s", "groups": ["g"]}`,
			Claims{Subject: "s", Email: "a@example.com", EmailVerified: VerifiedTrue, Groups: []string{"g"}},
		},
		"an overage in the ID token": {
			`{"sub": "s", "_claim_names": {"groups": "src1"}}`,
			`{"sub": "s", "groups": ["g"]}`,
			Claims{Subject: "s", GroupsOverage: true},
		},
	} {
		idToken := fields(t, tc.idToken)

		cl, err := c.claims(idToken, ErrToken)
		if err != nil {
			t.Fatal(err)
		}

		if !c.incomplete(cl, idToken) && name != "the ID token first" {
			t.Errorf("%s: the ID token is complete", name)
		}

		got, err := c.merge(cl, idToken, fields(t, tc.userInfo))
		if err != nil || got.Subject != tc.want.Subject || got.Email != tc.want.Email ||
			got.EmailVerified != tc.want.EmailVerified || !slices.Equal(got.Groups, tc.want.Groups) ||
			got.GroupsOverage != tc.want.GroupsOverage {
			t.Errorf("%s: %+v, %v, want %+v", name, got, err, tc.want)
		}
	}
}

func TestRoleFor(t *testing.T) {
	c := &Client{settings: db.OIDC{GroupRoles: []db.GroupRole{
		{Group: "viewers", Role: db.RoleReadOnly},
		{Group: "senders", Role: db.RoleSubmitter},
		{Group: "noc", Role: db.RoleNetworkManager},
		{Group: "admins", Role: db.RoleAdmin},
	}}}

	for _, tc := range []struct {
		groups []string
		role   db.Role
		err    error
	}{
		{nil, 0, ErrNoGroup},
		{[]string{"other"}, 0, ErrNoGroup},
		{[]string{"viewers"}, db.RoleReadOnly, nil},
		{[]string{"senders"}, db.RoleSubmitter, nil},
		{[]string{"viewers", "noc"}, db.RoleNetworkManager, nil},
		{[]string{"admins", "noc", "viewers"}, db.RoleAdmin, nil},
		{[]string{"senders", "viewers", "noc"}, db.RoleNetworkManager, nil},
		{[]string{"senders", "viewers"}, 0, ErrConflict},
		{[]string{"viewers", "senders", "other"}, 0, ErrConflict},
	} {
		if role, err := c.RoleFor(tc.groups); role != tc.role || !errors.Is(err, tc.err) {
			t.Errorf("RoleFor(%v) = %d, %v, want %d, %v", tc.groups, role, err, tc.role, tc.err)
		}
	}
}

func TestLogoutURL(t *testing.T) {
	c := &Client{settings: db.OIDC{ExternalURL: "https://smsc.example.com", ClientID: "smsc"}}

	if got := c.LogoutURL(); got != "" {
		t.Fatalf("LogoutURL without an end session endpoint = %q", got)
	}

	c.discovery.EndSessionEndpoint = "https://idp.example.com/logout?realm=smsc"

	want := "https://idp.example.com/logout?client_id=smsc&post_logout_redirect_uri=https%3A%2F%2Fsmsc.example.com%2Flogin&realm=smsc"
	if got := c.LogoutURL(); got != want {
		t.Fatalf("LogoutURL = %q, want %q", got, want)
	}
}
