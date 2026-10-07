package api_test

import (
	"net/http"
	"testing"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/version"
)

func TestGetStatus(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	code, result, _ := a.do(t, http.MethodGet, "/api/v1/status", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}

	if got, want := decode[api.Status](t, result), (api.Status{Version: version.Get().Version, Revision: version.Get().Revision}); got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}
