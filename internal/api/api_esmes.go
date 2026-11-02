package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/ellanetworks/smsc/internal/db"
)

const (
	MaxNumESMEs = 50
	// SMPP 3.4 section 4.1.1 table 4-1
	maxSystemID             = 15
	maxSMPPPassword         = 8
	generatedPasswordLength = 8
	maxAllowedNetworks      = 50
)

type ESME struct {
	SystemID        string        `json:"system_id"`
	User            string        `json:"user"`
	AllowedNetworks []string      `json:"allowed_networks"`
	CreatedAt       string        `json:"created_at"`
	Sessions        []ESMESession `json:"sessions"`
	Password        string        `json:"password,omitempty"`
}

type ESMESession struct {
	BindType      string `json:"bind_type"`
	RemoteAddress string `json:"remote_address"`
	BoundAt       string `json:"bound_at"`
}

type CreateESMEParams struct {
	SystemID        string   `json:"system_id"`
	User            string   `json:"user"`
	Password        *string  `json:"password"`
	AllowedNetworks []string `json:"allowed_networks"`
}

type UpdateESMEParams struct {
	Password        *string  `json:"password"`
	AllowedNetworks []string `json:"allowed_networks"`
}

type ListESMEsResponse struct {
	Items []ESME `json:"items"`
}

func ListESMEs(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esmes, err := cfg.ESMEs.ListESMEs(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to list ESMEs", err, cfg.Logger)
			return
		}

		resp := ListESMEsResponse{Items: make([]ESME, 0, len(esmes))}
		emails := map[int64]string{}

		for _, e := range esmes {
			email, ok := emails[e.UserID]
			if !ok {
				u, err := cfg.Auth.GetUserByID(r.Context(), e.UserID)
				if err != nil {
					writeError(w, r, http.StatusInternalServerError, "Failed to list ESMEs", err, cfg.Logger)
					return
				}

				email, emails[e.UserID] = u.Email, u.Email
			}

			resp.Items = append(resp.Items, esmeResponse(cfg, e, email))
		}

		writeResponse(w, r, resp, http.StatusOK, cfg.Logger)
	})
}

func GetESME(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, ok := lookupESME(w, r, cfg)
		if !ok {
			return
		}

		u, err := cfg.Auth.GetUserByID(r.Context(), e.UserID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to retrieve ESME", err, cfg.Logger)
			return
		}

		writeResponse(w, r, esmeResponse(cfg, e, u.Email), http.StatusOK, cfg.Logger)
	})
}

func CreateESME(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params CreateESMEParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		if !validSystemID(params.SystemID) {
			writeError(w, r, http.StatusBadRequest, fmt.Sprintf("system_id must be 1 to %d printable ASCII characters", maxSystemID),
				nil, cfg.Logger)

			return
		}

		email, ok := normalEmail(params.User)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "user must be the email of a user", nil, cfg.Logger)
			return
		}

		u, err := cfg.Auth.GetUser(r.Context(), email)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, r, http.StatusBadRequest, "user must be the email of a user", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to create ESME", err, cfg.Logger)
			return
		}

		e := db.ESME{SystemID: params.SystemID, UserID: u.ID, CreatedAt: cfg.Now()}

		if !validESME(w, r, &e, params.AllowedNetworks, cfg) {
			return
		}

		var generated string

		password := params.Password
		if password == nil {
			generated = newSMPPPassword()
			password = &generated
		}

		if !validSMPPPassword(*password) {
			writeError(w, r, http.StatusBadRequest, fmt.Sprintf("password must be 1 to %d printable ASCII characters", maxSMPPPassword),
				nil, cfg.Logger)

			return
		}

		n, err := cfg.ESMEs.CountESMEs(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to create ESME", err, cfg.Logger)
			return
		}

		if n >= MaxNumESMEs {
			writeError(w, r, http.StatusBadRequest, fmt.Sprintf("There are already %d ESMEs, the most there can be", MaxNumESMEs),
				nil, cfg.Logger)

			return
		}

		if e.PasswordHash, err = a.hashPassword(r.Context(), *password); err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to create ESME", err, cfg.Logger)
			return
		}

		e.ID, err = cfg.ESMEs.CreateESME(r.Context(), e)
		if errors.Is(err, db.ErrAlreadyExists) {
			writeError(w, r, http.StatusConflict, "An ESME with this system_id already exists", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to create ESME", err, cfg.Logger)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "create_esme", p.Email, fmt.Sprintf("ESME %q created for %s", e.SystemID, u.Email))

		resp := esmeResponse(cfg, e, u.Email)
		resp.Password = generated

		writeResponse(w, r, resp, http.StatusCreated, cfg.Logger)
	})
}

func UpdateESME(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params UpdateESMEParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		e, ok := lookupESME(w, r, cfg)
		if !ok {
			return
		}

		if !validESME(w, r, &e, params.AllowedNetworks, cfg) {
			return
		}

		e.PasswordHash = ""

		if params.Password != nil {
			if !validSMPPPassword(*params.Password) {
				writeError(w, r, http.StatusBadRequest, fmt.Sprintf("password must be 1 to %d printable ASCII characters", maxSMPPPassword),
					nil, cfg.Logger)

				return
			}

			hash, err := a.hashPassword(r.Context(), *params.Password)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "Failed to update ESME", err, cfg.Logger)
				return
			}

			e.PasswordHash = hash
		}

		err := cfg.ESMEs.UpdateESME(r.Context(), e)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "ESME not found", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to update ESME", err, cfg.Logger)
			return
		}

		cfg.SMPP.CloseESME(e.ID)

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "update_esme", p.Email, fmt.Sprintf("ESME %q updated", e.SystemID))

		u, err := cfg.Auth.GetUserByID(r.Context(), e.UserID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to retrieve ESME", err, cfg.Logger)
			return
		}

		writeResponse(w, r, esmeResponse(cfg, e, u.Email), http.StatusOK, cfg.Logger)
	})
}

func DeleteESME(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, ok := lookupESME(w, r, cfg)
		if !ok {
			return
		}

		err := cfg.ESMEs.DeleteESME(r.Context(), e.ID)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "ESME not found", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to delete ESME", err, cfg.Logger)
			return
		}

		cfg.SMPP.CloseESME(e.ID)

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "delete_esme", p.Email, fmt.Sprintf("ESME %q deleted", e.SystemID))
		writeResponse(w, r, SuccessResponse{Message: "ESME deleted successfully"}, http.StatusOK, cfg.Logger)
	})
}

func lookupESME(w http.ResponseWriter, r *http.Request, cfg Config) (db.ESME, bool) {
	e, err := cfg.ESMEs.GetESMEBySystemID(r.Context(), r.PathValue("system_id"))
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "ESME not found", nil, cfg.Logger)
		return db.ESME{}, false
	}

	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "Failed to retrieve ESME", err, cfg.Logger)
		return db.ESME{}, false
	}

	return e, true
}

func validESME(w http.ResponseWriter, r *http.Request, e *db.ESME, networks []string, cfg Config) bool {
	if len(networks) > maxAllowedNetworks {
		writeError(w, r, http.StatusBadRequest, fmt.Sprintf("allowed_networks must have at most %d networks", maxAllowedNetworks),
			nil, cfg.Logger)

		return false
	}

	e.AllowedNetworks = make([]netip.Prefix, 0, len(networks))
	seen := map[netip.Prefix]bool{}

	for _, s := range networks {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, fmt.Sprintf("allowed network %q must be a prefix such as 10.1.0.0/24", s),
				nil, cfg.Logger)

			return false
		}

		p = p.Masked()
		if !seen[p] {
			seen[p] = true
			e.AllowedNetworks = append(e.AllowedNetworks, p)
		}
	}

	return true
}

func printableASCII(s string, maxLen int) bool {
	if s == "" || len(s) > maxLen {
		return false
	}

	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}

	return true
}

func validSystemID(s string) bool { return printableASCII(s, maxSystemID) }

func validSMPPPassword(s string) bool { return printableASCII(s, maxSMPPPassword) }

func newSMPPPassword() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	b := make([]byte, generatedPasswordLength)
	for i := range b {
		b[i] = alphabet[randIntN(len(alphabet))]
	}

	return string(b)
}

func esmeResponse(cfg Config, e db.ESME, email string) ESME {
	resp := ESME{
		SystemID:        e.SystemID,
		User:            email,
		AllowedNetworks: make([]string, 0, len(e.AllowedNetworks)),
		CreatedAt:       formatTime(e.CreatedAt),
		Sessions:        []ESMESession{},
	}

	for _, p := range e.AllowedNetworks {
		resp.AllowedNetworks = append(resp.AllowedNetworks, p.String())
	}

	for _, s := range cfg.SMPP.Sessions(e.ID) {
		resp.Sessions = append(resp.Sessions, ESMESession{
			BindType:      s.BindType,
			RemoteAddress: s.Remote,
			BoundAt:       formatTime(s.BoundAt),
		})
	}

	return resp
}
