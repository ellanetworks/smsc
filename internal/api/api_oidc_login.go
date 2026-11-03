package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/oidc"
)

const (
	OIDCLoginCookieName = "__Host-smsc_oidc_login"
	maxErrorDetail      = 200
)

const (
	OIDCSuccess          = "success"
	OIDCRateLimited      = "rate_limited"
	OIDCUnavailable      = "unavailable"
	OIDCFailed           = "failed"
	OIDCDenied           = "denied"
	OIDCNotSetUp         = "not_set_up"
	OIDCEmailNotVerified = "email_not_verified"
	OIDCLinkedElsewhere  = "linked_elsewhere"
	OIDCNoGroup          = "no_group"
	OIDCGroupsOverage    = "groups_overage"
	OIDCTooManyUsers     = "too_many_users"
	OIDCError            = "error"
)

type OIDCLogin struct {
	Enabled     bool   `json:"enabled"`
	DisplayName string `json:"display_name,omitempty"`
}

type oidcRefusal struct {
	outcome string
	detail  string
}

func (e oidcRefusal) Error() string { return e.outcome + ": " + e.detail }

func refuse(outcome, format string, args ...any) error {
	return oidcRefusal{outcome: outcome, detail: fmt.Sprintf(format, args...)}
}

func GetOIDCLogin(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := cfg.OIDC.Get()

		resp := OIDCLogin{Enabled: o.Enabled}
		if o.Enabled {
			resp.DisplayName = o.DisplayName
		}

		writeResponse(w, r, resp, http.StatusOK, cfg.Logger)
	})
}

// OpenID Connect Core 1.0 section 3.1.2.1
func StartOIDCLogin(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		if !a.logins.Allow(clientKey(r)) {
			a.oidcDone(w, r, "", refuse(OIDCRateLimited, "too many login attempts"))
			return
		}

		c, err := cfg.OIDC.Client(r.Context(), cfg.Now)
		if err != nil {
			a.oidcDone(w, r, "", refuse(OIDCUnavailable, "%v", err))
			return
		}

		l := c.NewLogin(cfg.Now())

		value, err := oidc.Seal(cfg.OIDCCookieKey, OIDCLoginCookieName, l)
		if err != nil {
			a.oidcDone(w, r, "", err)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     OIDCLoginCookieName,
			Value:    value,
			Path:     "/",
			MaxAge:   int(oidc.LoginLifetime.Seconds()),
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, c.AuthCodeURL(l.State, l.Nonce, l.Verifier), http.StatusSeeOther)
	})
}

// OpenID Connect Core 1.0 section 3.1.2.7
// RFC 6749 section 10.12
func OIDCCallback(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.SetCookie(w, &http.Cookie{
			Name: OIDCLoginCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true,
			SameSite: http.SameSiteLaxMode,
		})

		c, cl, err := a.oidcAuthenticate(r)
		if err != nil {
			a.oidcDone(w, r, "", err)
			return
		}

		u, identityID, err := a.oidcUser(r, c, cl)
		if err != nil {
			a.oidcDone(w, r, actor(cl), err)
			return
		}

		if err := cfg.Auth.DeleteExpiredSessions(r.Context(), cfg.Now()); err != nil {
			a.oidcDone(w, r, u.Email, err)
			return
		}

		if _, err := a.startSession(w, r, u, db.SessionOIDC, identityID, c.Settings().SessionLifetime); err != nil {
			a.oidcDone(w, r, u.Email, err)
			return
		}

		a.oidcDone(w, r, u.Email, nil)
	})
}

func (a *auth) oidcAuthenticate(r *http.Request) (*oidc.Client, oidc.Claims, error) {
	cfg := a.cfg

	cookie, err := r.Cookie(OIDCLoginCookieName)
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "no login in progress")
	}

	l, err := oidc.Open(cfg.OIDCCookieKey, OIDCLoginCookieName, cookie.Value, cfg.Now())
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "%v", err)
	}

	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(l.State)) != 1 {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "the state does not match the login")
	}

	c, err := cfg.OIDC.Client(r.Context(), cfg.Now)
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCUnavailable, "%v", err)
	}

	if !c.Owns(l) {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "the OIDC settings changed during the login")
	}

	if err := c.CheckIssuer(q.Get("iss"), q.Has("iss")); err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "%v", err)
	}

	if e := q.Get("error"); e != "" {
		outcome := OIDCFailed
		if e == "access_denied" {
			outcome = OIDCDenied
		}

		return nil, oidc.Claims{}, refuse(outcome, "the identity provider returned %s: %s", clip(e),
			clip(q.Get("error_description")))
	}

	code := q.Get("code")
	if code == "" {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "no authorization code")
	}

	cl, err := c.Exchange(r.Context(), code, l.Verifier, l.Nonce)
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "%v", err)
	}

	return c, cl, nil
}

func (a *auth) oidcUser(r *http.Request, c *oidc.Client, cl oidc.Claims) (db.User, int64, error) {
	ctx := r.Context()
	store := a.cfg.Auth
	now := a.cfg.Now()
	issuer := c.Settings().Issuer
	identity := db.NewIdentity{Issuer: issuer, Subject: cl.Subject, Email: cl.Email, At: now}

	i, err := store.GetIdentity(ctx, issuer, cl.Subject)
	if err == nil {
		u, err := store.GetUserByID(ctx, i.UserID)
		if err != nil {
			return db.User{}, 0, err
		}

		if u, err = a.syncRole(r, c, cl, u); err != nil {
			return db.User{}, 0, err
		}

		return u, i.ID, store.TouchIdentity(ctx, i.ID, cl.Email, now)
	}

	if !errors.Is(err, db.ErrNotFound) {
		return db.User{}, 0, err
	}

	email, ok := normalEmail(cl.Email)
	if !ok {
		return db.User{}, 0, refuse(OIDCNotSetUp, "the ID token has no email address")
	}

	if !c.TrustsEmail(cl) {
		return db.User{}, 0, refuse(OIDCEmailNotVerified, "the identity provider has not verified %s", email)
	}

	u, err := store.GetUser(ctx, email)
	if err == nil {
		id, err := store.CreateIdentity(ctx, u.ID, identity)
		if errors.Is(err, db.ErrAlreadyExists) {
			return db.User{}, 0, refuse(OIDCLinkedElsewhere, "%s is linked to another account at the identity provider",
				u.Email)
		}

		if err != nil {
			return db.User{}, 0, err
		}

		a.audit(r, "link_user_oidc", u.Email, "User linked to the identity provider account "+cl.Subject)

		if u, err = a.syncRole(r, c, cl, u); err != nil {
			return db.User{}, 0, err
		}

		return u, id, nil
	}

	if !errors.Is(err, db.ErrNotFound) {
		return db.User{}, 0, err
	}

	role, err := groupRole(c, cl)
	if errors.Is(err, errNoMapping) {
		return db.User{}, 0, refuse(OIDCNotSetUp, "no user has the email %s", email)
	}

	if err != nil {
		return db.User{}, 0, err
	}

	userID, id, err := store.CreateUserWithIdentity(ctx, db.NewUser{Email: email, Role: role}, identity, MaxNumUsers)
	if errors.Is(err, db.ErrTooManyUsers) {
		return db.User{}, 0, refuse(OIDCTooManyUsers, "%d users exist", MaxNumUsers)
	}

	if errors.Is(err, db.ErrAlreadyExists) {
		return db.User{}, 0, refuse(OIDCFailed, "the user %s was created during the login", email)
	}

	if err != nil {
		return db.User{}, 0, err
	}

	a.audit(r, "create_user", email, fmt.Sprintf("User %s created from the identity provider with role %d", email, role))

	return db.User{ID: userID, Email: email, Role: role, HasIdentity: true}, id, nil
}

var errNoMapping = errors.New("no group mapping")

func groupRole(c *oidc.Client, cl oidc.Claims) (db.Role, error) {
	if !c.MapsGroups() {
		return 0, errNoMapping
	}

	if cl.GroupsOverage {
		return 0, refuse(OIDCGroupsOverage, "%v", oidc.ErrOverage)
	}

	role, ok := c.RoleFor(cl.Groups)
	if !ok {
		return 0, refuse(OIDCNoGroup, "none of the user's %d groups is mapped to a role", len(cl.Groups))
	}

	return role, nil
}

func (a *auth) syncRole(r *http.Request, c *oidc.Client, cl oidc.Claims, u db.User) (db.User, error) {
	if u.RolePinned {
		return u, nil
	}

	role, err := groupRole(c, cl)
	if errors.Is(err, errNoMapping) {
		return u, nil
	}

	if err != nil || role == u.Role {
		return u, err
	}

	err = a.cfg.Auth.UpdateUserRole(r.Context(), u.ID, role, false)
	if errors.Is(err, db.ErrLastPasswordAdmin) {
		a.audit(r, "update_user", u.Email, fmt.Sprintf("Role %d from the identity provider not applied to %s, the last admin with a password", role, u.Email))
		return u, nil
	}

	if err != nil {
		return db.User{}, err
	}

	a.audit(r, "update_user", u.Email, fmt.Sprintf("Role of %s set to %d from the identity provider", u.Email, role))
	u.Role = role

	return u, nil
}

func (a *auth) oidcDone(w http.ResponseWriter, r *http.Request, who string, err error) {
	outcome, target := OIDCSuccess, "/"

	var refusal oidcRefusal

	switch {
	case err == nil:
		a.audit(r, "auth_oidc_login", who, "User logged in through OIDC")
	case errors.As(err, &refusal):
		outcome = refusal.outcome
		a.audit(r, "auth_oidc_login", who, "User failed to log in through OIDC: "+refusal.Error())
	default:
		outcome = OIDCError

		a.cfg.Logger.ErrorContext(r.Context(), "OIDC login failed", slog.Any("error", err))
		a.audit(r, "auth_oidc_login", who, "User failed to log in through OIDC: internal error")
	}

	a.cfg.Requests.oidcLogin(outcome)

	if outcome != OIDCSuccess {
		target = "/login?" + url.Values{"error": {outcome}}.Encode()
	}

	http.Redirect(w, r, target, http.StatusSeeOther)
}

func actor(cl oidc.Claims) string {
	if cl.Email != "" {
		return cl.Email
	}

	return cl.Subject
}

func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, s)

	if len(s) > maxErrorDetail {
		s = s[:maxErrorDetail]
	}

	return s
}
