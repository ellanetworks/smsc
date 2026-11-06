//go:build integration && linux

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

func (n *node) raw(method, path, contentType string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(n.apiPort)+path,
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	req.Header.Set("Authorization", "Bearer "+n.token())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("%s %s = %d: %s", method, path, resp.StatusCode, out)
	}

	return out, nil
}

func (n *node) mintToken() string {
	n.t.Helper()

	var token struct {
		Token string `json:"token"`
	}

	n.must(http.MethodPost, "/api/v1/cluster/tokens", map[string]int{}, &token)

	return token.Token
}

func (n *node) identify() {
	n.t.Helper()

	st, err := n.status()
	if err != nil {
		n.t.Fatal(err)
	}

	n.id = st.NodeID
}

func (c *cluster) member(n *node) (member, error) {
	st, err := c.otherThan(n).status()
	if err != nil {
		return member{}, err
	}

	for _, m := range st.Members {
		if m.NodeID == n.id {
			return m, nil
		}
	}

	return member{}, fmt.Errorf("%s is not a member", n.name)
}

func TestConcurrentJoinsWithSingleUseTokens(t *testing.T) {
	first := newNode(t, "node0")
	first.initialize()
	first.identify()

	cl := &cluster{t: t, nodes: []*node{first, newNode(t, "node1"), newNode(t, "node2")}}
	tokens := []string{first.mintToken(), first.mintToken()}

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for i, n := range cl.nodes[1:] {
		wg.Go(func() {
			_, errs[i] = n.request(http.MethodPost, "/api/v1/cluster/join", map[string]string{"token": tokens[i]}, nil)
		})
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("node%d failed to join: %v", i+1, err)
		}
	}

	for _, n := range cl.nodes[1:] {
		n.logIn()
		n.identify()
	}

	cl.waitStable()

	late := newNode(t, "late")

	for _, token := range tokens {
		code, err := late.request(http.MethodPost, "/api/v1/cluster/join", map[string]string{"token": token}, nil)
		if err == nil {
			t.Fatal("a join token was redeemed twice")
		}

		if code != http.StatusConflict {
			t.Fatalf("reusing a join token = %d, want %d: %v", code, http.StatusConflict, err)
		}
	}

	cl.waitStable()

	cl.nodes = append(cl.nodes, late)
	cl.join(late)
	late.identify()

	eventually(t, "the late node a member", func() error {
		m, err := cl.member(late)
		if err != nil {
			return err
		}

		if m.Healthy == nil || !*m.Healthy {
			return fmt.Errorf("%s is %s and not healthy", late.name, m.Suffrage)
		}

		return nil
	})
}

func TestAMemberReturnsOnANewAddress(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	moved := cl.followers(leader)[0]

	before, err := cl.member(moved)
	if err != nil {
		t.Fatal(err)
	}

	moved.stop()

	moved.clusterPort = freePort(t)
	moved.configure()
	moved.start()
	moved.logIn()

	want := "127.0.0.1:" + strconv.Itoa(moved.clusterPort)

	eventually(t, moved.name+" known at its new address", func() error {
		m, err := cl.member(moved)
		if err != nil {
			return err
		}

		if m.Address != want {
			return fmt.Errorf("address %s, was %s, want %s", m.Address, before.Address, want)
		}

		return nil
	})

	cl.waitStable()

	leader.must(http.MethodPost, "/api/v1/users",
		map[string]any{"email": "moved@example.com", "password": "correct horse", "role_id": 4}, nil)

	eventually(t, "the write on the moved node", func() error {
		_, err := moved.request(http.MethodGet, "/api/v1/users/moved@example.com", nil, nil)
		return err
	})
}

func TestRestoreABackupInACluster(t *testing.T) {
	cl := newCluster(t)
	leader := cl.leader()
	followers := cl.followers(leader)
	source, target := followers[0], followers[1]

	user := func(email string) map[string]any {
		return map[string]any{"email": email, "password": "correct horse", "role_id": 4}
	}

	leader.must(http.MethodPost, "/api/v1/users", user("before@example.com"), nil)

	eventually(t, "the first write on "+source.name, func() error {
		_, err := source.request(http.MethodGet, "/api/v1/users/before@example.com", nil, nil)
		return err
	})

	backup, err := source.raw(http.MethodGet, "/api/v1/backup", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	leader.must(http.MethodPost, "/api/v1/users", user("after@example.com"), nil)

	if _, err := target.raw(http.MethodPost, "/api/v1/restore", "application/gzip", backup); err != nil {
		t.Fatal(err)
	}

	for _, n := range cl.nodes {
		n.logIn()

		eventually(t, n.name+" to hold the backup's data", func() error {
			if _, err := n.request(http.MethodGet, "/api/v1/users/before@example.com", nil, nil); err != nil {
				return err
			}

			if code, _ := n.request(http.MethodGet, "/api/v1/users/after@example.com", nil, nil); code != http.StatusNotFound {
				return fmt.Errorf("a write made after the backup = %d", code)
			}

			return nil
		})
	}

	cl.waitStable()

	for _, n := range cl.nodes {
		if _, err := cl.member(n); err != nil {
			t.Fatal(err)
		}
	}

	cl.leader().must(http.MethodPost, "/api/v1/users", user("restored@example.com"), nil)

	for _, n := range cl.nodes {
		eventually(t, n.name+" to take writes after the restore", func() error {
			_, err := n.request(http.MethodGet, "/api/v1/users/restored@example.com", nil, nil)
			return err
		})
	}
}
