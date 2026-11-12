package api_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"golang.org/x/crypto/bcrypt"
)

type fakeSMPP struct {
	sessions    map[int64][]api.SMPPSession
	closedESMEs []int64
}

func (f *fakeSMPP) Sessions(id int64) []api.SMPPSession { return f.sessions[id] }

func newSMPPTestAPI(t *testing.T, smpp api.SMPP) *testAPI {
	t.Helper()

	store := openStore(t)
	a := &testAPI{store: store, now: testNow}

	if f, ok := smpp.(*fakeSMPP); ok {
		store.OnEvents(func(ev db.Events) { f.closedESMEs = append(f.closedESMEs, ev.ClosedESMEs...) })
	}

	cfg := testConfig(t, store)
	cfg.SMPP = smpp
	cfg.Now = func() time.Time { return a.now }
	cfg.Logger = slog.New(slog.NewJSONHandler(&a.logs, nil))

	a.handler = api.NewHandler(cfg)
	a.token = initialize(t, a.handler)

	return a
}

const otaESMEBody = `{"system_id": "ota", "user": "ota@example.com", "password": "secret",
	"allowed_networks": ["10.1.0.0/24", "2001:db8::/32"]}`

func (a *testAPI) createESME(t *testing.T, body string) api.ESME {
	t.Helper()

	code, result, msg := a.do(t, http.MethodPost, "/api/v1/esmes", body)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/esmes = %d %q", code, msg)
	}

	return decode[api.ESME](t, result)
}

func (a *testAPI) storedESME(t *testing.T, systemID string) db.ESME {
	t.Helper()

	e, err := a.store.GetESMEBySystemID(context.Background(), systemID)
	if err != nil {
		t.Fatalf("GetESMEBySystemID(%q): %v", systemID, err)
	}

	return e
}

func (a *testAPI) userID(t *testing.T) int64 {
	t.Helper()

	email := "ota@example.com"

	u, err := a.store.GetUser(context.Background(), email)
	if err != nil {
		t.Fatalf("GetUser(%q): %v", email, err)
	}

	return u.ID
}

func passwordMatches(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func TestESMELifecycle(t *testing.T) {
	smpp := &fakeSMPP{}
	a := newSMPPTestAPI(t, smpp)
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	code, result, msg := a.do(t, http.MethodPost, "/api/v1/esmes", otaESMEBody)
	if code != http.StatusCreated {
		t.Fatalf("POST = %d %q", code, msg)
	}

	want := api.ESME{
		SystemID:        "ota",
		User:            "ota@example.com",
		AllowedNetworks: []string{"10.1.0.0/24", "2001:db8::/32"},
		Routes:          []string{},
		CreatedAt:       "2026-09-29T10:00:00.000Z",
		Sessions:        []api.ESMESession{},
	}

	if got := decode[api.ESME](t, result); !reflect.DeepEqual(got, want) {
		t.Fatalf("POST = %+v, want %+v", got, want)
	}

	if strings.Contains(string(result), "password") || strings.Contains(string(result), "secret") {
		t.Fatalf("POST with a password returns it: %s", result)
	}

	stored := a.storedESME(t, "ota")
	if !passwordMatches(stored.PasswordHash, "secret") {
		t.Fatal("the stored hash is not that of the password")
	}

	smpp.sessions = map[int64][]api.SMPPSession{stored.ID: {
		{BindType: "transceiver", Remote: "10.1.0.7:40000", BoundAt: testNow.Add(-time.Minute)},
		{BindType: "receiver", Remote: "[2001:db8::7]:40001", BoundAt: testNow},
	}}

	withSessions := want
	withSessions.Sessions = []api.ESMESession{
		{BindType: "transceiver", RemoteAddress: "10.1.0.7:40000", BoundAt: "2026-09-29T09:59:00.000Z"},
		{BindType: "receiver", RemoteAddress: "[2001:db8::7]:40001", BoundAt: "2026-09-29T10:00:00.000Z"},
	}

	code, result, _ = a.do(t, http.MethodGet, "/api/v1/esmes/ota", "")
	if got := decode[api.ESME](t, result); code != http.StatusOK || !reflect.DeepEqual(got, withSessions) {
		t.Fatalf("GET = %d %+v, want %+v", code, got, withSessions)
	}

	if !strings.Contains(string(result), `"remote_address":"10.1.0.7:40000","bound_at":"2026-09-29T09:59:00.000Z"`) {
		t.Fatalf("GET = %s", result)
	}

	code, result, _ = a.do(t, http.MethodGet, "/api/v1/esmes", "")
	if got := decode[api.ListESMEsResponse](t, result); code != http.StatusOK ||
		!reflect.DeepEqual(got.Items, []api.ESME{withSessions}) {
		t.Fatalf("GET list = %d %+v", code, got)
	}

	smpp.sessions = nil

	code, result, msg = a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": ["192.0.2.7/32"], "routes": []}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %q", code, msg)
	}

	updated := api.ESME{
		SystemID:        "ota",
		User:            "ota@example.com",
		AllowedNetworks: []string{"192.0.2.7/32"},
		Routes:          []string{},
		CreatedAt:       "2026-09-29T10:00:00.000Z",
		Sessions:        []api.ESMESession{},
	}

	if got := decode[api.ESME](t, result); !reflect.DeepEqual(got, updated) {
		t.Fatalf("PUT = %+v, want %+v", got, updated)
	}

	if e := a.storedESME(t, "ota"); e.PasswordHash != stored.PasswordHash {
		t.Fatal("PUT without a password changed the password")
	}

	code, result, _ = a.do(t, http.MethodGet, "/api/v1/esmes/ota", "")
	if got := decode[api.ESME](t, result); code != http.StatusOK || !reflect.DeepEqual(got, updated) {
		t.Fatalf("GET after PUT = %d %+v", code, got)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"password": "newpass", "allowed_networks": [], "routes": []}`); code != http.StatusOK {
		t.Fatalf("PUT with a password = %d %q", code, msg)
	}

	if e := a.storedESME(t, "ota"); !passwordMatches(e.PasswordHash, "newpass") || len(e.AllowedNetworks) != 0 {
		t.Fatalf("after PUT with a password: %+v", e)
	}

	if code, result, msg := a.do(t, http.MethodDelete, "/api/v1/esmes/ota", ""); code != http.StatusOK ||
		!strings.Contains(string(result), "ESME deleted successfully") {
		t.Fatalf("DELETE = %d %s %q", code, result, msg)
	}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if code, _, _ := a.do(t, method, "/api/v1/esmes/ota", `{"allowed_networks": [], "routes": []}`); code != http.StatusNotFound {
			t.Errorf("%s of a deleted ESME = %d, want 404", method, code)
		}
	}

	if code, result, _ := a.do(t, http.MethodGet, "/api/v1/esmes", ""); code != http.StatusOK ||
		string(result) != `{"items":[]}` {
		t.Fatalf("GET list after delete = %d %s", code, result)
	}
}

func TestESMEGeneratedPassword(t *testing.T) {
	a := newSMPPTestAPI(t, &fakeSMPP{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	seen := map[string]bool{}

	for i := range 3 {
		systemID := fmt.Sprintf("ota%d", i)

		code, result, msg := a.do(t, http.MethodPost, "/api/v1/esmes",
			`{"system_id": "`+systemID+`", "user": "ota@example.com", "allowed_networks": []}`)
		if code != http.StatusCreated {
			t.Fatalf("POST = %d %q", code, msg)
		}

		password := decode[api.ESME](t, result).Password
		if !regexp.MustCompile(`^[A-Za-z0-9]{8}$`).MatchString(password) {
			t.Fatalf("generated password %q, want 8 characters of [A-Za-z0-9]", password)
		}

		if seen[password] {
			t.Fatalf("generated password %q twice", password)
		}

		seen[password] = true

		if !passwordMatches(a.storedESME(t, systemID).PasswordHash, password) {
			t.Fatal("the stored hash is not that of the generated password")
		}

		for _, path := range []string{"/api/v1/esmes/" + systemID, "/api/v1/esmes"} {
			if _, result, _ := a.do(t, http.MethodGet, path, ""); strings.Contains(string(result), password) ||
				strings.Contains(string(result), `"password"`) {
				t.Fatalf("GET %s returns the password: %s", path, result)
			}
		}

		if _, result, _ := a.do(t, http.MethodPut, "/api/v1/esmes/"+systemID,
			`{"allowed_networks": [], "routes": []}`); strings.Contains(string(result),
			`"password"`) {
			t.Fatalf("PUT returns a password: %s", result)
		}
	}
}

func TestESMESystemIDInThePath(t *testing.T) {
	a := newSMPPTestAPI(t, &fakeSMPP{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	a.createESME(t, `{"system_id": "my esme/1", "user": "ota@example.com", "password": "x", "allowed_networks": []}`)

	for _, tc := range []struct {
		method, body string
	}{
		{http.MethodGet, ""},
		{http.MethodPut, `{"allowed_networks": [], "routes": []}`},
		{http.MethodDelete, ""},
	} {
		if code, _, msg := a.do(t, tc.method, "/api/v1/esmes/my%20esme%2F1", tc.body); code != http.StatusOK {
			t.Fatalf("%s of an escaped system_id = %d %q", tc.method, code, msg)
		}
	}
}

func TestESMEValidation(t *testing.T) {
	a := newSMPPTestAPI(t, &fakeSMPP{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)
	a.createESME(t, otaESMEBody)

	create := func(fields string) string {
		body := `{"system_id": "new", "user": "ota@example.com", "password": "secret", "allowed_networks": ["10.0.0.0/8"]`
		if fields != "" {
			body += ", " + fields
		}

		return body + "}"
	}

	createWith := func(field, value string) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(create("")), &m); err != nil {
			t.Fatal(err)
		}

		var v any
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			t.Fatal(err)
		}

		m[field] = v
		b, _ := json.Marshal(m)

		return string(b)
	}

	update := func(fields string) string {
		body := `{"allowed_networks": ["10.0.0.0/8"], "routes": []`
		if fields != "" {
			body += ", " + fields
		}

		return body + "}"
	}

	updateWith := func(field, value string) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(update("")), &m); err != nil {
			t.Fatal(err)
		}

		var v any
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			t.Fatal(err)
		}

		m[field] = v
		b, _ := json.Marshal(m)

		return string(b)
	}

	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"valid", http.MethodPost, "/api/v1/esmes", createWith("system_id", `"abcdefghijklmno"`), http.StatusCreated},
		{
			"valid password of 8", http.MethodPost, "/api/v1/esmes",
			`{"system_id": "p8", "user": "ota@example.com", "password": "12345678", "allowed_networks": []}`,
			http.StatusCreated,
		},
		{"empty system_id", http.MethodPost, "/api/v1/esmes", createWith("system_id", `""`), http.StatusBadRequest},
		{
			"no system_id", http.MethodPost, "/api/v1/esmes", `{"user": "ota@example.com", "allowed_networks": []}`,
			http.StatusBadRequest,
		},
		{
			"system_id of 16", http.MethodPost, "/api/v1/esmes", createWith("system_id", `"abcdefghijklmnop"`),
			http.StatusBadRequest,
		},
		{
			"non-printable system_id", http.MethodPost, "/api/v1/esmes", createWith("system_id", `"ab\u0001c"`),
			http.StatusBadRequest,
		},
		{
			"non-ASCII system_id", http.MethodPost, "/api/v1/esmes", createWith("system_id", `"été"`),
			http.StatusBadRequest,
		},
		{"password of 9", http.MethodPost, "/api/v1/esmes", createWith("password", `"123456789"`), http.StatusBadRequest},
		{"empty password", http.MethodPost, "/api/v1/esmes", createWith("password", `""`), http.StatusBadRequest},
		{
			"non-ASCII password", http.MethodPost, "/api/v1/esmes", createWith("password", `"pässwd"`),
			http.StatusBadRequest,
		},
		{
			"non-printable password", http.MethodPost, "/api/v1/esmes", createWith("password", `"a\tb"`),
			http.StatusBadRequest,
		},
		{
			"unknown user", http.MethodPost, "/api/v1/esmes", createWith("user", `"nobody@example.com"`),
			http.StatusBadRequest,
		},
		{"user not an email", http.MethodPost, "/api/v1/esmes", createWith("user", `"ota"`), http.StatusBadRequest},
		{
			"bad network", http.MethodPost, "/api/v1/esmes", createWith("allowed_networks", `["10.0.0.0"]`),
			http.StatusBadRequest,
		},
		{
			"not a network", http.MethodPost, "/api/v1/esmes", createWith("allowed_networks", `["example.org/8"]`),
			http.StatusBadRequest,
		},
		{"max_sessions", http.MethodPost, "/api/v1/esmes", createWith("max_sessions", `4`), http.StatusBadRequest},
		{"default_alphabet", http.MethodPost, "/api/v1/esmes", createWith("default_alphabet", `"gsm7"`), http.StatusBadRequest},
		{
			"forward_sim_answers", http.MethodPost, "/api/v1/esmes", createWith("forward_sim_answers", `true`),
			http.StatusBadRequest,
		},
		{"unknown field", http.MethodPost, "/api/v1/esmes", create(`"tls": true`), http.StatusBadRequest},
		{"not JSON", http.MethodPost, "/api/v1/esmes", `system_id=new`, http.StatusBadRequest},
		{"taken system_id", http.MethodPost, "/api/v1/esmes", createWith("system_id", `"ota"`), http.StatusConflict},
		{
			"update: password of 9", http.MethodPut, "/api/v1/esmes/ota", updateWith("password", `"123456789"`),
			http.StatusBadRequest,
		},
		{
			"update: empty password", http.MethodPut, "/api/v1/esmes/ota", updateWith("password", `""`),
			http.StatusBadRequest,
		},
		{
			"update: max_sessions", http.MethodPut, "/api/v1/esmes/ota", updateWith("max_sessions", `4`),
			http.StatusBadRequest,
		},
		{
			"update: bad network", http.MethodPut, "/api/v1/esmes/ota", updateWith("allowed_networks", `["10.0.0.1/33"]`),
			http.StatusBadRequest,
		},
		{
			"update: system_id", http.MethodPut, "/api/v1/esmes/ota", update(`"system_id": "other"`),
			http.StatusBadRequest,
		},
		{
			"update: user", http.MethodPut, "/api/v1/esmes/ota", update(`"user": "ota@example.com"`),
			http.StatusBadRequest,
		},
		{"update: unknown ESME", http.MethodPut, "/api/v1/esmes/nobody", update(""), http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := a.storedESME(t, "ota")

			if code, _, msg := a.do(t, tt.method, tt.path, tt.body); code != tt.want {
				t.Fatalf("%s %s = %d %q, want %d", tt.method, tt.body, code, msg, tt.want)
			}

			if tt.method == http.MethodPut {
				if after := a.storedESME(t, "ota"); !reflect.DeepEqual(after, before) {
					t.Fatalf("a refused PUT changed the ESME: %+v, was %+v", after, before)
				}
			}
		})
	}

	e := a.createESME(t, `{"system_id": "masked", "user": "ota@example.com", "password": "x",
		"allowed_networks": ["10.1.2.3/8", "10.0.0.0/8", "192.0.2.1/32"]}`)
	if want := []string{"10.0.0.0/8", "192.0.2.1/32"}; !slices.Equal(e.AllowedNetworks, want) {
		t.Fatalf("allowed_networks = %v, want %v", e.AllowedNetworks, want)
	}
}

func TestMaxNumESMEs(t *testing.T) {
	a := newSMPPTestAPI(t, &fakeSMPP{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	for i := range api.MaxNumESMEs {
		a.createESME(t, fmt.Sprintf(`{"system_id": "esme%d", "user": "ota@example.com", "password": "x",
			"allowed_networks": []}`, i))
	}

	if code, _, msg := a.do(t, http.MethodPost, "/api/v1/esmes",
		`{"system_id": "onemore", "user": "ota@example.com", "password": "x", "allowed_networks": []}`); code != http.StatusBadRequest {
		t.Fatalf("ESME %d = %d %q, want %d", api.MaxNumESMEs+1, code, msg, http.StatusBadRequest)
	}

	if n, err := a.store.CountESMEs(context.Background()); err != nil || n != api.MaxNumESMEs {
		t.Fatalf("CountESMEs = %d, %v", n, err)
	}
}

func TestESMEChangesNeedALogin(t *testing.T) {
	a := newSMPPTestAPI(t, &fakeSMPP{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)
	a.createESME(t, otaESMEBody)

	token := a.createToken(t, a.token, "/api/v1/users/me/api-tokens", `{"name": "automation"}`)

	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{
			http.MethodPost, "/api/v1/esmes", `{"system_id": "new", "user": "ota@example.com", "allowed_networks": []}`,
			http.StatusForbidden,
		},
		{
			http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": [], "routes": []}`,
			http.StatusForbidden,
		},
		{http.MethodDelete, "/api/v1/esmes/ota", "", http.StatusForbidden},
		{http.MethodGet, "/api/v1/esmes", "", http.StatusOK},
		{http.MethodGet, "/api/v1/esmes/ota", "", http.StatusOK},
	} {
		code, _, msg := a.doAs(t, token, tc.method, tc.path, tc.body)
		if code != tc.want || tc.want == http.StatusForbidden && msg != "This needs a login, not an API token" {
			t.Errorf("%s %s with an admin's API token = %d %q, want %d", tc.method, tc.path, code, msg, tc.want)
		}
	}

	if esmes, err := a.store.ListESMEs(context.Background()); err != nil || len(esmes) != 1 ||
		len(esmes[0].AllowedNetworks) != 2 {
		t.Fatalf("ESMEs after the refused changes = %+v, %v", esmes, err)
	}
}

func TestESMEChangesCloseSessions(t *testing.T) {
	smpp := &fakeSMPP{}
	a := newSMPPTestAPI(t, smpp)
	a.createUser(t, "ota@example.com", db.RoleSubmitter)
	a.createUser(t, "other@example.com", db.RoleSubmitter)
	a.createESME(t, otaESMEBody)
	a.createESME(t, `{"system_id": "other", "user": "other@example.com", "password": "x", "allowed_networks": []}`)

	ota, other := a.storedESME(t, "ota").ID, a.storedESME(t, "other").ID

	if len(smpp.closedESMEs) != 0 {
		t.Fatalf("creating ESMEs closed sessions: %v", smpp.closedESMEs)
	}

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": ["10.0.0.1"], "routes": []}`); code != http.StatusBadRequest {
		t.Fatalf("invalid PUT = %d", code)
	}

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": [], "routes": []}`); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}

	if !slices.Equal(smpp.closedESMEs, []int64{ota}) {
		t.Fatalf("closed ESMEs after PUT = %v, want [%d]", smpp.closedESMEs, ota)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/esmes/other", ""); code != http.StatusOK {
		t.Fatalf("DELETE = %d", code)
	}

	if !slices.Equal(smpp.closedESMEs, []int64{ota, other}) {
		t.Fatalf("closed ESMEs after DELETE = %v, want [%d %d]", smpp.closedESMEs, ota, other)
	}

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/ota@example.com", `{"role_id": 2}`); code != http.StatusOK {
		t.Fatalf("role change = %d", code)
	}

	if !slices.Equal(smpp.closedESMEs, []int64{ota, other, ota}) {
		t.Fatalf("closed ESMEs after a role change = %v, want [%d %d %d]", smpp.closedESMEs, ota, other, ota)
	}

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/other@example.com/password",
		`{"password": "new password"}`); code != http.StatusOK {
		t.Fatalf("password change = %d", code)
	}

	if !slices.Equal(smpp.closedESMEs, []int64{ota, other, ota}) {
		t.Fatalf("closed ESMEs after a password change = %v, want no more", smpp.closedESMEs)
	}

	a.createESME(t, `{"system_id": "other2", "user": "other@example.com", "password": "x", "allowed_networks": []}`)
	other2 := a.storedESME(t, "other2").ID

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/users/other@example.com", ""); code != http.StatusOK {
		t.Fatalf("delete user = %d", code)
	}

	if !slices.Equal(smpp.closedESMEs, []int64{ota, other, ota, other2}) {
		t.Fatalf("closed ESMEs after a deletion = %v, want [%d %d %d %d]", smpp.closedESMEs, ota, other, ota, other2)
	}
}

func TestESMEChangesAreAudited(t *testing.T) {
	a := newSMPPTestAPI(t, &fakeSMPP{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	a.createESME(t, otaESMEBody)

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"password": "newpass", "allowed_networks": [], "routes": []}`); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/esmes/ota", ""); code != http.StatusOK {
		t.Fatalf("DELETE = %d", code)
	}

	if code, _, _ := a.do(t, http.MethodPost, "/api/v1/esmes", `{"system_id": "", "user": "ota@example.com"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid POST = %d", code)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/esmes/ota", ""); code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d", code)
	}

	for action, want := range map[string][]string{
		"create_esme": {testAdmin + `: ESME "ota" created for ota@example.com`},
		"update_esme": {testAdmin + `: ESME "ota" updated`},
		"delete_esme": {testAdmin + `: ESME "ota" deleted`},
	} {
		if got := a.auditEvents(t, action); !slices.Equal(got, want) {
			t.Errorf("%s events = %q, want %q", action, got, want)
		}
	}

	if strings.Contains(a.logs.String(), "secret") || strings.Contains(a.logs.String(), "newpass") {
		t.Fatal("the logs carry an SMPP password")
	}
}

func TestMessagesFromAnESME(t *testing.T) {
	ctx := context.Background()
	a := newSMPPTestAPI(t, &fakeSMPP{})

	ota := a.userLogin(t, "ota@example.com", db.RoleSubmitter)
	other := a.userLogin(t, "other@example.com", db.RoleSubmitter)
	a.createESME(t, otaESMEBody)

	submit, err := hex.DecodeString("01070b915155210300f200040268" + "69")
	if err != nil {
		t.Fatal(err)
	}

	to := db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1}

	id, err := a.store.CreateMessage(ctx, db.NewMessage{
		Originator: db.Address{Digits: "15550001111", TypeOfNumber: 1, NumberingPlan: 1}, Recipient: to,
		MSISDN: to.Digits, Origin: db.OriginSMPP, SubmittedBy: a.userID(t), TPDU: submit,
		SubmittedAt: testNow, ExpiresAt: testNow.Add(time.Hour),
		SMPP: &db.NewSMPPMessage{ESMEID: a.storedESME(t, "ota").ID, Receipt: db.ReceiptFinal, DataCoding: 0x04},
	})
	if err != nil {
		t.Fatal(err)
	}

	apiMessage := a.create(t, "+15550001111", "+15551230002", "from the API")[0].ID
	path := fmt.Sprintf("/api/v1/messages/%d", id)

	for name, token := range map[string]string{"owner": ota, "admin": a.token} {
		code, result, msg := a.doAs(t, token, http.MethodGet, path, "")
		if got := decode[api.Message](t, result); code != http.StatusOK || got.SystemID != "ota" ||
			got.SubmittedBy != "ota@example.com" {
			t.Fatalf("GET as the %s = %d %+v %q", name, code, got, msg)
		}

		code, result, _ = a.doAs(t, token, http.MethodGet, "/api/v1/messages", "")

		systemIDs := map[int64]string{}
		for _, m := range decode[api.ListMessagesResponse](t, result).Items {
			systemIDs[m.ID] = m.SystemID
		}

		if code != http.StatusOK || systemIDs[id] != "ota" || systemIDs[apiMessage] != "" {
			t.Fatalf("GET list as the %s = %d %v", name, code, systemIDs)
		}
	}

	if _, result, _ := a.do(t, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", apiMessage), ""); strings.Contains(
		string(result), "system_id") {
		t.Fatalf("an API message has a system_id: %s", result)
	}

	for _, p := range []string{path, path + "/attempts", path + "/content"} {
		if code, _, _ := a.doAs(t, other, http.MethodGet, p, ""); code != http.StatusNotFound {
			t.Errorf("GET %s as another Submitter = %d, want 404", p, code)
		}
	}

	if code, result, _ := a.doAs(t, other, http.MethodGet, "/api/v1/messages", ""); code != http.StatusOK ||
		decode[api.ListMessagesResponse](t, result).TotalCount != 0 {
		t.Fatalf("GET list as another Submitter = %d %s", code, result)
	}

	if c := a.content(t, ota, id); c.Binary == nil {
		t.Fatalf("content as the owner = %+v", c)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/esmes/ota", ""); code != http.StatusOK {
		t.Fatalf("DELETE = %d", code)
	}

	code, result, _ := a.doAs(t, ota, http.MethodGet, path, "")
	if got := decode[api.Message](t, result); code != http.StatusOK || got.SystemID != "" ||
		got.SubmittedBy != "ota@example.com" {
		t.Fatalf("GET after deleting the ESME = %d %+v", code, got)
	}
}

func TestESMERoutes(t *testing.T) {
	a := newSMPPTestAPI(t, &fakeSMPP{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	created := a.createESME(t, `{"system_id": "ota", "user": "ota@example.com", "password": "secret",
		"allowed_networks": [], "routes": ["40000-40099", "1555", "4000-4000", "40000-40099"]}`)
	if !reflect.DeepEqual(created.Routes, []string{"1555", "4000", "40000-40099"}) {
		t.Fatalf("POST routes = %v; want them canonical, without duplicates, sorted", created.Routes)
	}

	if got := a.storedESME(t, "ota"); !slices.Equal(got.Routes, created.Routes) {
		t.Fatalf("stored routes = %v, want %v", got.Routes, created.Routes)
	}

	for i, tc := range []struct {
		route string
		want  int
	}{
		{"40050", http.StatusConflict},
		{"40090-40109", http.StatusConflict},
		{"4000", http.StatusConflict},
		{"40a0", http.StatusBadRequest},
		{"40000-4009", http.StatusBadRequest},
		{"40099-40000", http.StatusBadRequest},
		{"155512300123456789012", http.StatusBadRequest},
		{"40100-40199", http.StatusCreated},
	} {
		code, _, msg := a.do(t, http.MethodPost, "/api/v1/esmes",
			fmt.Sprintf(`{"system_id": "other%d", "user": "ota@example.com", "routes": [%q]}`, i, tc.route))
		if code != tc.want {
			t.Errorf("POST with route %s = %d %q, want %d", tc.route, code, msg, tc.want)
		}
	}

	code, _, msg := a.do(t, http.MethodPut, "/api/v1/esmes/ota",
		`{"allowed_networks": [], "routes": ["40000-40099", "40050"]}`)
	if code != http.StatusBadRequest || !strings.Contains(msg, "40000-40099 and 40050 overlap") {
		t.Fatalf("PUT with overlapping routes = %d %q", code, msg)
	}

	code, _, msg = a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": [], "routes": ["40100"]}`)
	if code != http.StatusConflict {
		t.Fatalf("PUT with a route of another ESME = %d %q", code, msg)
	}

	code, result, msg := a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": [], "routes": ["4001"]}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %q", code, msg)
	}

	if updated := decode[api.ESME](t, result); !slices.Equal(updated.Routes, []string{"4001"}) {
		t.Fatalf("PUT routes = %v", updated.Routes)
	}

	for name, body := range map[string]string{
		"without routes":     `{"allowed_networks": ["10.0.0.0/8"]}`,
		"with null routes":   `{"allowed_networks": ["10.0.0.0/8"], "routes": null}`,
		"with only password": `{"allowed_networks": [], "password": "newpass"}`,
	} {
		code, result, msg := a.do(t, http.MethodPut, "/api/v1/esmes/ota", body)
		if code != http.StatusOK {
			t.Fatalf("PUT %s = %d %q", name, code, msg)
		}

		if updated := decode[api.ESME](t, result); !slices.Equal(updated.Routes, []string{"4001"}) {
			t.Fatalf("PUT %s routes = %v; the routes must stay", name, updated.Routes)
		}

		if got := a.storedESME(t, "ota"); !slices.Equal(got.Routes, []string{"4001"}) {
			t.Fatalf("stored routes after a PUT %s = %v; the routes must stay", name, got.Routes)
		}
	}

	code, result, msg = a.do(t, http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": [], "routes": []}`)
	if code != http.StatusOK {
		t.Fatalf("PUT with no routes = %d %q", code, msg)
	}

	if updated := decode[api.ESME](t, result); len(updated.Routes) != 0 {
		t.Fatalf("PUT with [] routes = %v; [] removes them all", updated.Routes)
	}

	for action, want := range map[string][]string{
		"create_esme": {
			testAdmin + `: ESME "ota" created for ota@example.com with routes [1555 4000 40000-40099]`,
			testAdmin + `: ESME "other7" created for ota@example.com with routes [40100-40199]`,
		},
	} {
		if got := a.auditEvents(t, action); !slices.Equal(got, want) {
			t.Errorf("%s events = %q, want %q", action, got, want)
		}
	}
}

func TestRoutedRecipientsInTheAPI(t *testing.T) {
	ctx := context.Background()
	a := newTestAPI(t, fakeDiameter{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	esmeID, err := a.store.CreateESME(ctx, db.ESME{
		SystemID: "ota", UserID: a.userID(t), PasswordHash: "hash",
		Routes: []string{"40000-40009", "+15551230000-+15551239999"}, CreatedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		recipient db.Address
		msisdn    string
		want      string
	}{
		{"dialled-number route", db.Address{Digits: "40001", TypeOfNumber: 0, NumberingPlan: 1}, "140001", "40001"},
		{"abbreviated number", db.Address{Digits: "40002", TypeOfNumber: 6, NumberingPlan: 1}, "40002", "40002"},
		{
			"international route",
			db.Address{Digits: "15551230009", TypeOfNumber: 1, NumberingPlan: 1},
			"15551230009",
			"+15551230009",
		},
		{"no route", db.Address{Digits: "5559990002", TypeOfNumber: 0, NumberingPlan: 1}, "15559990002", "+15559990002"},
	} {
		id, err := a.store.CreateMessage(ctx, db.NewMessage{
			Originator:  db.Address{Digits: "15550001111", TypeOfNumber: 1, NumberingPlan: 1},
			Recipient:   tc.recipient,
			MSISDN:      tc.msisdn,
			Origin:      db.OriginMobile,
			TPDU:        []byte{0x01},
			SubmittedAt: testNow,
			ExpiresAt:   testNow.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("%s: CreateMessage: %v", tc.name, err)
		}

		code, result, errMsg := a.do(t, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), "")
		if code != http.StatusOK {
			t.Fatalf("%s: GET = %d %q", tc.name, code, errMsg)
		}

		if got := decode[api.Message](t, result); got.To != tc.want {
			t.Errorf("%s: to = %q, want %q", tc.name, got.To, tc.want)
		}
	}

	if err := a.store.DeleteESME(ctx, esmeID); err != nil {
		t.Fatalf("DeleteESME: %v", err)
	}

	code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages?to=40001", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %q", code, errMsg)
	}

	if got := decode[api.ListMessagesResponse](t, result); got.TotalCount != 1 || got.Items[0].To != "40001" {
		t.Fatalf("messages to 40001 after deleting the ESME = %+v; the dialled number must stay", got)
	}
}

func TestDelegatedMessagesInTheAPI(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	a.createUser(t, "ota@example.com", db.RoleSubmitter)

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.store.CreateESME(context.Background(), db.ESME{
		SystemID: "ota", UserID: a.userID(t), PasswordHash: string(hash),
		Routes: []string{"+15551230004"}, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	created := a.create(t, "+15550001111", "+15551230004", "to the application")
	if len(created) != 1 || created[0].Status != "delegated" {
		t.Fatalf("create = %+v; want one delegated message", created)
	}

	code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages?status=delegated", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %q", code, errMsg)
	}

	if got := decode[api.ListMessagesResponse](t, result); got.TotalCount != 1 ||
		got.Items[0].ID != created[0].ID {
		t.Fatalf("delegated messages = %+v", got)
	}

	code, result, errMsg = a.do(t, http.MethodGet, "/api/v1/messages?status=pending", "")
	if code != http.StatusOK {
		t.Fatalf("list pending = %d %q", code, errMsg)
	}

	if got := decode[api.ListMessagesResponse](t, result); got.TotalCount != 0 {
		t.Fatalf("the delegated message must not be pending: %+v", got)
	}

	if code, _, errMsg := a.do(t, http.MethodGet, "/api/v1/messages?status=lost", ""); code != http.StatusBadRequest {
		t.Fatalf("an unknown status = %d %q", code, errMsg)
	}

	code, result, errMsg = a.do(t, http.MethodGet, "/api/v1/messages/counts", "")
	if code != http.StatusOK {
		t.Fatalf("counts = %d %q", code, errMsg)
	}

	if got := decode[api.MessageCounts](t, result); got.Delegated != 1 {
		t.Fatalf("counts = %+v; want one delegated", got)
	}
}

type failingESMEs struct {
	*db.DB
	err error
}

func (f failingESMEs) CreateESME(context.Context, db.ESME) (int64, error) { return 0, f.err }

func (f failingESMEs) UpdateESME(context.Context, db.ESME) error { return f.err }

func TestESMEWritesWhileBusy(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"upgrade pending", fmt.Errorf("update ESME: %w", db.ErrUpgradePending), "The cluster is still upgrading, retry shortly"},
		{"busy", fmt.Errorf("update ESME: %w", db.ErrBusy), "Too busy, retry later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t)
			a := &testAPI{store: store, now: testNow}

			cfg := testConfig(t, store)
			cfg.SMPP = &fakeSMPP{}
			cfg.ESMEs = failingESMEs{DB: store, err: tc.err}

			a.handler = api.NewHandler(cfg)
			a.token = initialize(t, a.handler)
			a.createUser(t, "ota@example.com", db.RoleSubmitter)

			if _, err := store.CreateESME(context.Background(), db.ESME{
				SystemID: "ota", UserID: a.userID(t), PasswordHash: "hash", CreatedAt: testNow,
			}); err != nil {
				t.Fatal(err)
			}

			for _, req := range []struct{ method, path, body string }{
				{http.MethodPost, "/api/v1/esmes", `{"system_id": "other", "user": "ota@example.com", "routes": ["4000"]}`},
				{http.MethodPut, "/api/v1/esmes/ota", `{"allowed_networks": [], "routes": ["4000"]}`},
			} {
				if code, _, msg := a.do(t, req.method, req.path, req.body); code != http.StatusServiceUnavailable ||
					msg != tc.want {
					t.Errorf("%s %s = %d %q, want 503 %q", req.method, req.path, code, msg, tc.want)
				}
			}
		})
	}
}

func TestESMERouteChangesKeepSessions(t *testing.T) {
	smpp := &fakeSMPP{}
	a := newSMPPTestAPI(t, smpp)
	a.createUser(t, "ota@example.com", db.RoleSubmitter)
	a.createESME(t, otaESMEBody)

	ota := a.storedESME(t, "ota").ID

	for _, tc := range []struct {
		name, body string
		closes     bool
	}{
		{"new routes", `{"allowed_networks": ["10.1.0.0/24", "2001:db8::/32"], "routes": ["4000"]}`, false},
		{"other routes, networks reordered", `{"allowed_networks": ["2001:db8::/32", "10.1.0.0/24"], "routes": ["4001", "40000-40099"]}`, false},
		{"routes omitted", `{"allowed_networks": ["10.1.0.0/24", "2001:db8::/32"]}`, false},
		{"routes removed", `{"allowed_networks": ["10.1.0.0/24", "2001:db8::/32"], "routes": []}`, false},
		{"a network added", `{"allowed_networks": ["10.1.0.0/24", "2001:db8::/32", "192.0.2.0/24"], "routes": ["4000"]}`, true},
		{"a network removed", `{"allowed_networks": ["10.1.0.0/24"]}`, true},
		{"a password", `{"allowed_networks": ["10.1.0.0/24"], "password": "newpass"}`, true},
	} {
		before := len(smpp.closedESMEs)

		if code, _, msg := a.do(t, http.MethodPut, "/api/v1/esmes/ota", tc.body); code != http.StatusOK {
			t.Fatalf("PUT with %s = %d %q", tc.name, code, msg)
		}

		closed := smpp.closedESMEs[before:]
		if want := map[bool][]int64{true: {ota}, false: {}}[tc.closes]; !slices.Equal(closed, want) {
			t.Errorf("PUT with %s closed sessions of %v, want %v", tc.name, closed, want)
		}
	}
}
