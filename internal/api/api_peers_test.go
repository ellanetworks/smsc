package api_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/api"
)

const deaBody = `{
	"host": "dea1.ipx.example.net",
	"addresses": ["192.0.2.10", "2001:db8::10"],
	"routes": [
		{"default": true, "application": "sgd"},
		{"realm": "epc.mnc001.mcc001.3gppnetwork.org", "application": "s6c", "priority": 1}
	]
}`

func decodePeer(t *testing.T, result json.RawMessage) api.Peer {
	t.Helper()

	var p api.Peer
	if err := json.Unmarshal(result, &p); err != nil {
		t.Fatalf("invalid peer %s: %v", result, err)
	}

	return p
}

func TestPeerLifecycle(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	code, result, errMsg := a.do(t, http.MethodPost, "/api/v1/diameter/peers", deaBody)
	if code != http.StatusCreated {
		t.Fatalf("POST = %d %s", code, errMsg)
	}

	want := api.Peer{
		ID:        1,
		Host:      "dea1.ipx.example.net",
		Addresses: []string{"192.0.2.10", "2001:db8::10"},
		Port:      3868,
		Routes: []api.PeerRoute{
			{Default: true, Application: "sgd"},
			{Realm: "epc.mnc001.mcc001.3gppnetwork.org", Application: "s6c", Priority: 1},
		},
	}

	if got := decodePeer(t, result); !reflect.DeepEqual(got, want) {
		t.Fatalf("POST = %+v, want %+v", got, want)
	}

	if !strings.Contains(string(result), `"routes":[{"default":true,"application":"sgd","priority":0}`) {
		t.Fatalf("a default route has no realm: %s", result)
	}

	code, result, _ = a.do(t, http.MethodGet, "/api/v1/diameter/peers/1", "")
	if got := decodePeer(t, result); code != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Fatalf("GET = %d %+v", code, got)
	}

	code, result, errMsg = a.do(t, http.MethodPut, "/api/v1/diameter/peers/1",
		`{"addresses": ["192.0.2.10"], "port": 3869, "routes": []}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, errMsg)
	}

	want = api.Peer{ID: 1, Addresses: []string{"192.0.2.10"}, Port: 3869, Routes: []api.PeerRoute{}}
	if got := decodePeer(t, result); !reflect.DeepEqual(got, want) {
		t.Fatalf("PUT = %+v, want %+v", got, want)
	}

	code, result, _ = a.do(t, http.MethodGet, "/api/v1/diameter/peers", "")

	var list api.ListPeersResponse
	if err := json.Unmarshal(result, &list); err != nil || code != http.StatusOK || len(list.Items) != 1 ||
		!reflect.DeepEqual(list.Items[0], want) {
		t.Fatalf("GET list = %d %s", code, result)
	}

	if code, _, errMsg := a.do(t, http.MethodDelete, "/api/v1/diameter/peers/1", ""); code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", code, errMsg)
	}

	if code, result, _ := a.do(t, http.MethodGet, "/api/v1/diameter/peers", ""); code != http.StatusOK ||
		string(result) != `{"items":[]}` {
		t.Fatalf("GET list after delete = %d %s", code, result)
	}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if code, _, _ := a.do(t, method, "/api/v1/diameter/peers/1", deaBody); code != http.StatusNotFound {
			t.Errorf("%s of a deleted peer = %d", method, code)
		}
	}
}

func TestPeerStatus(t *testing.T) {
	since := time.Date(2026, 9, 29, 9, 59, 0, 0, time.UTC)

	a := newTestAPI(t, fakeDiameter{peers: []diameter.PeerStatus{{
		ID:         "peer-1",
		Host:       "dea1.ipx.example.net",
		Realm:      "ipx.example.net",
		State:      diameter.PeerDown,
		Since:      since,
		LastError:  "connection refused",
		Configured: true,
	}}})

	// A write returns the peer as stored: the SMSC applies it after the response.
	code, result, errMsg := a.do(t, http.MethodPost, "/api/v1/diameter/peers", deaBody)
	if code != http.StatusCreated || decodePeer(t, result).Status != nil {
		t.Fatalf("POST = %d %s %s", code, result, errMsg)
	}

	if _, result, _ := a.do(t, http.MethodPut, "/api/v1/diameter/peers/1", deaBody); decodePeer(t, result).Status != nil {
		t.Fatalf("PUT = %s", result)
	}

	_, result, _ = a.do(t, http.MethodGet, "/api/v1/diameter/peers/1", "")

	want := &api.PeerStatus{
		State: "down",
		Host:  "dea1.ipx.example.net",
		Realm: "ipx.example.net",
		Since: "2026-09-29T09:59:00.000Z",
		Error: "connection refused",
	}

	if got := decodePeer(t, result).Status; !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}

	_, result, _ = a.do(t, http.MethodGet, "/api/v1/diameter", "")

	var status api.DiameterStatus
	if err := json.Unmarshal(result, &status); err != nil || len(status.Peers) != 1 {
		t.Fatalf("GET /api/v1/diameter = %s", result)
	}

	if p := status.Peers[0]; p.ID != 1 || !p.Configured || p.Error != "connection refused" {
		t.Fatalf("configured peer = %+v", p)
	}
}

func TestPeerValidation(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	tests := map[string]string{
		"not JSON":           `{`,
		"unknown field":      `{"addresses": ["192.0.2.10"], "transport": "tcp"}`,
		"no address":         `{"addresses": []}`,
		"hostname address":   `{"addresses": ["dea1.ipx.example.net"]}`,
		"zoned address":      `{"addresses": ["fe80::1%eth0"]}`,
		"mapped unspecified": `{"addresses": ["::ffff:0.0.0.0"]}`,
		"mapped twice":       `{"addresses": ["::ffff:192.0.2.10", "192.0.2.10"]}`,
		"invalid host":       `{"host": "dea1 ipx", "addresses": ["192.0.2.10"]}`,
		"port":               `{"addresses": ["192.0.2.10"], "port": 70000}`,
		"application":        `{"addresses": ["192.0.2.10"], "routes": [{"realm": "example.net", "application": "s6a"}]}`,
		"no realm":           `{"addresses": ["192.0.2.10"], "routes": [{"application": "sgd"}]}`,
		"default and realm":  `{"addresses": ["192.0.2.10"], "routes": [{"default": true, "realm": "example.net", "application": "sgd"}]}`,
		"default s6c":        `{"addresses": ["192.0.2.10"], "routes": [{"default": true, "application": "s6c"}]}`,
		"negative priority":  `{"addresses": ["192.0.2.10"], "routes": [{"realm": "example.net", "application": "sgd", "priority": -1}]}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := a.do(t, http.MethodPost, "/api/v1/diameter/peers", body); code != http.StatusBadRequest {
				t.Fatalf("POST = %d", code)
			}
		})
	}

	if code, _, _ := a.do(t, http.MethodGet, "/api/v1/diameter/peers/x", ""); code != http.StatusBadRequest {
		t.Fatalf("GET with an invalid ID = %d", code)
	}

	if code, _, _ := a.do(t, http.MethodGet, "/api/v1/diameter/peers", ""); code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
}

func TestPeerConflicts(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	if code, _, _ := a.do(t, http.MethodPost, "/api/v1/diameter/peers", deaBody); code != http.StatusCreated {
		t.Fatalf("POST = %d", code)
	}

	if code, _, errMsg := a.do(t, http.MethodPost, "/api/v1/diameter/peers", deaBody); code != http.StatusConflict ||
		!strings.Contains(errMsg, "dea1.ipx.example.net") {
		t.Fatalf("POST of the same host = %d %q", code, errMsg)
	}

	// The SMSC's host is smsc.node.<realm> of the operator settings.
	self := `{"host": "smsc.node.epc.mnc001.mcc001.3gppnetwork.org", "addresses": ["192.0.2.20"]}`
	if code, _, _ := a.do(t, http.MethodPost, "/api/v1/diameter/peers", self); code != http.StatusConflict {
		t.Fatalf("POST of the SMSC's host = %d", code)
	}
}

func TestPeerChangesAreAudited(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	a.do(t, http.MethodPost, "/api/v1/diameter/peers", deaBody)
	a.do(t, http.MethodPut, "/api/v1/diameter/peers/1", `{"addresses": ["192.0.2.10"]}`)
	a.do(t, http.MethodDelete, "/api/v1/diameter/peers/1", "")

	for _, action := range []string{"create_diameter_peer", "update_diameter_peer", "delete_diameter_peer"} {
		if !strings.Contains(a.logs.String(), `"action":"`+action+`"`) {
			t.Errorf("no audit event %s in %s", action, a.logs.String())
		}
	}
}

func TestPeerAddressesAreStoredUnmapped(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	body := `{"addresses": ["::ffff:192.0.2.10"]}`

	_, created, _ := a.do(t, http.MethodPost, "/api/v1/diameter/peers", body)
	_, updated, _ := a.do(t, http.MethodPut, "/api/v1/diameter/peers/1", body)
	_, got, _ := a.do(t, http.MethodGet, "/api/v1/diameter/peers/1", "")

	for name, result := range map[string]json.RawMessage{"POST": created, "PUT": updated, "GET": got} {
		if p := decodePeer(t, result); !reflect.DeepEqual(p.Addresses, []string{"192.0.2.10"}) {
			t.Errorf("%s addresses = %v", name, p.Addresses)
		}
	}
}

func TestOperatorChangeToAPeersHostConflicts(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	peer := `{"host": "smsc.node.epc.mnc002.mcc001.3gppnetwork.org", "addresses": ["192.0.2.10"]}`
	if code, _, _ := a.do(t, http.MethodPost, "/api/v1/diameter/peers", peer); code != http.StatusCreated {
		t.Fatalf("POST peer = %d", code)
	}

	operator := `{"mcc": "001", "mnc": "02", "service_centre_address": "+15550000000",
		"numbering": {"country_code": "1", "national_prefix": "1", "international_prefix": "011"}}`
	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/operator", operator); code != http.StatusConflict {
		t.Fatalf("PUT operator = %d", code)
	}
}
