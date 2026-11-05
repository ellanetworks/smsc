//go:build integration && linux

package integration

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
)

func TestBackToBackJoins(t *testing.T) {
	for round := range 3 {
		t.Run("round"+strconv.Itoa(round), func(t *testing.T) {
			first := newNode(t, "node0")

			var login struct {
				Token string `json:"token"`
			}

			first.must(http.MethodPost, "/api/v1/init", map[string]string{"email": adminEmail, "password": adminPassword}, &login)
			first.token = login.Token

			cl := &cluster{t: t, nodes: []*node{first, newNode(t, "node1"), newNode(t, "node2")}}

			for _, n := range cl.nodes[1:] {
				token := first.mintToken()

				if _, err := n.request(http.MethodPost, "/api/v1/cluster/join", map[string]string{"token": token}, nil); err != nil {
					t.Fatalf("%s failed to join: %v", n.name, err)
				}
			}

			for _, n := range cl.nodes[1:] {
				n.logIn()
			}

			for _, n := range cl.nodes {
				n.identify()
			}

			cl.waitStable()

			for _, n := range cl.nodes[1:] {
				if _, err := cl.member(n); err != nil {
					t.Fatal(fmt.Errorf("after back-to-back joins: %w", err))
				}
			}
		})
	}
}
