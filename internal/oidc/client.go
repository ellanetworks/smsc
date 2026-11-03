package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/ellanetworks/smsc/internal/db"
	"golang.org/x/oauth2"
)

const providerTTL = time.Hour

var (
	ErrDisabled = errors.New("OIDC is disabled")
	ErrIssuer   = errors.New("the authorization response comes from another issuer")
	ErrToken    = errors.New("invalid ID token")
	ErrOverage  = errors.New("the ID token refers to the groups instead of listing them")
)

type provider struct {
	key       string
	at        time.Time
	http      *http.Client
	provider  *gooidc.Provider
	discovery Discovery
}

type Client struct {
	settings  db.OIDC
	http      *http.Client
	oauth     oauth2.Config
	verifier  *gooidc.IDTokenVerifier
	discovery Discovery
}

type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Groups        []string
	GroupsOverage bool
}

func (l *Live) Client(ctx context.Context, now func() time.Time) (*Client, error) {
	o := l.Get()
	if !o.Enabled {
		return nil, ErrDisabled
	}

	p, err := l.provider(ctx, o, now())
	if err != nil {
		return nil, err
	}

	return &Client{
		settings: o,
		http:     p.http,
		oauth: oauth2.Config{
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			Endpoint:     p.provider.Endpoint(),
			RedirectURL:  RedirectURL(o),
			Scopes:       o.Scopes,
		},
		verifier:  p.provider.Verifier(&gooidc.Config{ClientID: o.ClientID, Now: now}),
		discovery: p.discovery,
	}, nil
}

func (l *Live) provider(ctx context.Context, o db.OIDC, now time.Time) (*provider, error) {
	key := o.Issuer + "\x00" + o.CACertificate

	l.providerMu.Lock()
	defer l.providerMu.Unlock()

	if p := l.cached; p != nil && p.key == key && now.Sub(p.at) < providerTTL && !now.Before(p.at) {
		return p, nil
	}

	client, err := HTTPClient(o.CACertificate)
	if err != nil {
		return nil, err
	}

	gp, d, err := discover(ctx, client, o.Issuer)
	if err != nil {
		return nil, err
	}

	l.cached = &provider{key: key, at: now, http: client, provider: gp, discovery: d}

	return l.cached, nil
}

func (c *Client) Settings() db.OIDC {
	return clone(c.settings)
}

// OpenID Connect Core 1.0 section 3.1.2.1
// RFC 7636 section 4.3
func (c *Client) AuthCodeURL(state, nonce, verifier string) string {
	return c.oauth.AuthCodeURL(state, gooidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// RFC 9207 section 2.4
func (c *Client) CheckIssuer(iss string, present bool) error {
	if present && iss != c.settings.Issuer || !present && c.discovery.IssuerParameter {
		return ErrIssuer
	}

	return nil
}

// OpenID Connect Core 1.0 section 3.1.3.5
// RFC 7636 section 4.5
func (c *Client) Exchange(ctx context.Context, code, verifier, nonce string) (Claims, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.http)

	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Claims{}, fmt.Errorf("%w: token request: %w", ErrProvider, err)
	}

	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return Claims{}, fmt.Errorf("%w: the token response has no ID token", ErrToken)
	}

	return c.verify(ctx, raw, nonce)
}

// OpenID Connect Core 1.0 section 3.1.3.7
func (c *Client) verify(ctx context.Context, raw, nonce string) (Claims, error) {
	token, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrToken, err)
	}

	var fields map[string]json.RawMessage
	if err := token.Claims(&fields); err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrToken, err)
	}

	var azp string
	if err := optional(fields, "azp", &azp); err != nil {
		return Claims{}, err
	}

	switch {
	case token.Nonce != nonce:
		return Claims{}, fmt.Errorf("%w: wrong nonce", ErrToken)
	case azp != "" && azp != c.settings.ClientID:
		return Claims{}, fmt.Errorf("%w: issued to %q", ErrToken, azp)
	case len(token.Audience) > 1 && azp == "":
		return Claims{}, fmt.Errorf("%w: several audiences and no azp", ErrToken)
	case token.Subject == "" || len(token.Subject) > maxValue:
		return Claims{}, fmt.Errorf("%w: the subject must be 1 to %d characters", ErrToken, maxValue)
	}

	return c.claims(token.Subject, fields)
}

// OpenID Connect Core 1.0 section 5.1
// OpenID Connect Core 1.0 section 5.6.2
func (c *Client) claims(subject string, fields map[string]json.RawMessage) (Claims, error) {
	cl := Claims{Subject: subject}

	if err := optional(fields, "email", &cl.Email); err != nil {
		return Claims{}, err
	}

	var verified any
	if err := optional(fields, "email_verified", &verified); err != nil {
		return Claims{}, err
	}

	cl.EmailVerified = verified == true || verified == "true"

	if c.settings.GroupsClaim == "" {
		return cl, nil
	}

	var names map[string]any
	if err := optional(fields, "_claim_names", &names); err != nil {
		return Claims{}, err
	}

	if _, ok := names[c.settings.GroupsClaim]; ok {
		cl.GroupsOverage = true
	}

	raw, ok := fields[c.settings.GroupsClaim]
	if !ok {
		return cl, nil
	}

	var one string
	if json.Unmarshal(raw, &one) == nil {
		cl.Groups = []string{one}
		return cl, nil
	}

	if err := json.Unmarshal(raw, &cl.Groups); err != nil {
		return Claims{}, fmt.Errorf("%w: the %s claim is not a list of strings", ErrToken, c.settings.GroupsClaim)
	}

	return cl, nil
}

func optional(fields map[string]json.RawMessage, name string, v any) error {
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return nil
	}

	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: the %s claim: %w", ErrToken, name, err)
	}

	return nil
}

func (c *Client) RoleFor(groups []string) (db.Role, bool) {
	for _, g := range c.settings.GroupRoles {
		if slices.Contains(groups, g.Group) {
			return g.Role, true
		}
	}

	return 0, false
}

func (c *Client) MapsGroups() bool {
	return len(c.settings.GroupRoles) > 0
}

func (c *Client) TrustsEmail(cl Claims) bool {
	return strings.TrimSpace(cl.Email) != "" && (cl.EmailVerified || c.settings.TrustUnverifiedEmail)
}
