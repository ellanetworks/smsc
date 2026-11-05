//go:build integration && linux

package integration

import (
	"fmt"
	"net/http"
	"testing"
)

func TestALoginWorksOnEveryNodeRightAway(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()

	credentials := map[string]any{"email": "ops@example.com", "password": "correct horse"}

	leader.must(http.MethodPost, "/api/v1/users",
		map[string]any{"email": "ops@example.com", "password": "correct horse", "role_id": 1}, nil)

	for _, n := range cl.nodes {
		eventually(t, n.name+" to know the user", func() error {
			_, err := n.request(http.MethodGet, "/api/v1/users/ops@example.com", nil, nil)
			return err
		})
	}

	for i := range 6 {
		for _, via := range cl.nodes {
			var login struct {
				Token string `json:"token"`
			}

			as := *via
			as.token = ""
			as.must(http.MethodPost, "/api/v1/auth/login", credentials, &login)

			for _, other := range cl.nodes {
				use := *other
				use.token = login.Token

				if _, err := use.request(http.MethodGet, "/api/v1/cluster", nil, nil); err != nil {
					t.Fatalf("round %d: a login on %s used on %s: %v", i, via.name, other.name, err)
				}
			}
		}
	}
}

func TestAFollowerReadsItsOwnWrites(t *testing.T) {
	cl := newCluster(t)

	for _, f := range cl.followers(cl.leader()) {
		user := f.name + "@example.com"

		f.must(http.MethodPost, "/api/v1/users",
			map[string]any{"email": user, "password": "correct horse", "role_id": 4}, nil)

		for i := range 20 {
			id := fmt.Sprintf("%s-%d", f.name, i)
			password := esmePassword

			f.must(http.MethodPost, "/api/v1/esmes", map[string]any{
				"system_id": id, "user": user, "password": &password,
				"allowed_networks": []string{"127.0.0.0/8"},
			}, nil)

			if _, err := f.request(http.MethodGet, "/api/v1/esmes/"+id, nil, nil); err != nil {
				t.Fatalf("%s wrote ESME %s but does not see it: %v", f.name, id, err)
			}
		}
	}
}
