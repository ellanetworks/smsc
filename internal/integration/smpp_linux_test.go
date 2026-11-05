//go:build integration && linux

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp"
)

const (
	esmeSystemID = "ota"
	esmePassword = "esmepass"
)

func bindReceiver(n *node) (*gosmpp.Session, error) {
	return gosmpp.NewSession(gosmpp.RXConnector(gosmpp.NonTLSDialer, gosmpp.Auth{
		SMSC: "127.0.0.1:" + strconv.Itoa(n.smppPort), SystemID: esmeSystemID, Password: esmePassword,
	}), gosmpp.Settings{ReadTimeout: 10 * time.Second}, -1)
}

func TestSessionsOfADeadNodeAreReleased(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	followers := cl.followers(leader)
	doomed, other := followers[0], followers[1]

	password := esmePassword

	leader.must(http.MethodPost, "/api/v1/users",
		map[string]any{"email": "ota@example.com", "password": "correct horse", "role_id": 4}, nil)
	leader.must(http.MethodPost, "/api/v1/esmes", map[string]any{
		"system_id": esmeSystemID, "user": "ota@example.com", "password": &password,
		"allowed_networks": []string{"127.0.0.0/8"},
	}, nil)

	eventually(t, "the ESME on the doomed node", func() error {
		_, err := doomed.request(http.MethodGet, "/api/v1/esmes/"+esmeSystemID, nil, nil)
		return err
	})

	for range 4 {
		var s *gosmpp.Session

		eventually(t, "a bind on the doomed node", func() error {
			var err error

			s, err = bindReceiver(doomed)

			return err
		})

		t.Cleanup(func() { _ = s.Close() })
	}

	if s, err := bindReceiver(other); err == nil {
		_ = s.Close()

		t.Fatal("a fifth session across the cluster was bound")
	}

	doomed.kill()

	eventually(t, "the leader to drop the dead node's sessions", func() error {
		var esme struct {
			Sessions []json.RawMessage `json:"sessions"`
		}

		if _, err := leader.request(http.MethodGet, "/api/v1/esmes/"+esmeSystemID, nil, &esme); err != nil {
			return err
		}

		if len(esme.Sessions) > 0 {
			return fmt.Errorf("%d sessions left", len(esme.Sessions))
		}

		return nil
	})

	s, err := bindReceiver(other)
	if err != nil {
		t.Fatalf("a bind once the dead node's sessions are released: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })
}
