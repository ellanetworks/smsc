package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/cluster"
)

func TestLivez(t *testing.T) {
	cfg := testConfig(t, openStore(t))
	cfg.Ready = func(context.Context, bool) []cluster.Check {
		return []cluster.Check{{Name: "database", Detail: "database is locked"}}
	}

	if rec := serve(api.NewHandler(cfg), request(http.MethodGet, "/api/v1/livez", "", "")); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
}

func readyz(t *testing.T, h http.Handler, path string) (int, api.Readiness) {
	t.Helper()

	rec := serve(h, request(http.MethodGet, path, "", ""))

	var body struct {
		Result api.Readiness `json:"result"`
	}

	if rec.Code != http.StatusBadRequest {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body %s: %v", path, rec.Body.String(), err)
		}
	}

	return rec.Code, body.Result
}

func TestReadyz(t *testing.T) {
	var (
		checks      []cluster.Check
		withCluster bool
	)

	cfg := testConfig(t, openStore(t))
	cfg.Ready = func(_ context.Context, c bool) []cluster.Check {
		withCluster = c
		return checks
	}
	h := api.NewHandler(cfg)

	checks = []cluster.Check{{Name: "shutdown", OK: true}, {Name: "database", OK: true}}

	code, res := readyz(t, h, "/api/v1/readyz")
	if code != http.StatusOK || !res.Ready || len(res.Checks) != 2 || withCluster {
		t.Fatalf("ready: code = %d, result = %+v, cluster %t", code, res, withCluster)
	}

	checks = []cluster.Check{{Name: "shutdown", OK: true}, {Name: "leader", Detail: "no contact with a cluster leader"}}

	code, res = readyz(t, h, "/api/v1/readyz")
	if code != http.StatusServiceUnavailable || res.Ready {
		t.Fatalf("not ready: code = %d, result = %+v", code, res)
	}

	if res.Checks[1] != checks[1] {
		t.Fatalf("not ready: checks = %+v, want %+v", res.Checks, checks)
	}

	checks = []cluster.Check{{Name: "cluster", OK: true}}

	if code, _ := readyz(t, h, "/api/v1/readyz?cluster=true"); code != http.StatusOK || !withCluster {
		t.Fatalf("cluster=true: code = %d, cluster %t", code, withCluster)
	}

	if code, _ := readyz(t, h, "/api/v1/readyz?cluster=maybe"); code != http.StatusBadRequest {
		t.Fatalf("cluster=maybe: code = %d", code)
	}
}

func TestReadyzWithoutChecks(t *testing.T) {
	cfg := testConfig(t, openStore(t))
	cfg.Ready = nil

	code, res := readyz(t, api.NewHandler(cfg), "/api/v1/readyz")
	if code != http.StatusOK || !res.Ready || res.Checks == nil {
		t.Fatalf("code = %d, result = %+v", code, res)
	}
}
