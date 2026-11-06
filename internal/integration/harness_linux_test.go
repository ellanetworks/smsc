//go:build integration && linux

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	adminEmail    = "admin@example.com"
	adminPassword = "integration password"
	waitTimeout   = 60 * time.Second
)

var binary, upgraded string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "smsc-integration-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	binary, upgraded = filepath.Join(dir, "smsc"), filepath.Join(dir, "smsc-next")

	builds := []*exec.Cmd{
		exec.CommandContext(context.Background(), "go", "build", "-o", binary, "../../cmd/smsc"),
		exec.CommandContext(context.Background(), "go", "build", "-tags", "upgradetest", "-o", upgraded, "../../cmd/smsc"),
	}

	for _, build := range builds {
		build.Stdout, build.Stderr = os.Stdout, os.Stderr

		if err := build.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	for _, build := range builds {
		if err := build.Wait(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	code := m.Run()

	_ = os.RemoveAll(dir)

	os.Exit(code)
}

var (
	portsMu sync.Mutex
	ports   = map[int]bool{}
)

func freePort(t *testing.T) int {
	t.Helper()

	portsMu.Lock()
	defer portsMu.Unlock()

	for {
		var lc net.ListenConfig

		ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}

		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()

		if !ports[port] {
			ports[port] = true
			return port
		}
	}
}

type node struct {
	t            *testing.T
	name         string
	dir          string
	apiPort      int
	diameterPort int
	clusterPort  int
	smppPort     int
	bin          string
	cmd          *exec.Cmd
	exited       chan struct{}
	session      atomic.Pointer[string]
	id           string
}

func newNode(t *testing.T, name string) *node {
	t.Helper()

	n := &node{
		t: t, name: name, bin: binary, dir: t.TempDir(), apiPort: freePort(t), diameterPort: freePort(t),
		clusterPort: freePort(t), smppPort: freePort(t),
	}

	n.configure()

	t.Cleanup(func() {
		n.kill()

		if t.Failed() {
			if out, err := os.ReadFile(filepath.Join(n.dir, "smsc.log")); err == nil {
				fmt.Fprintf(os.Stderr, "--- %s log ---\n%s\n", n.name, tail(out, 8<<10))
			}
		}
	})

	n.start()

	return n
}

func (n *node) configure() {
	n.t.Helper()

	config := fmt.Sprintf(`logging:
  level: debug
db:
  path: %s
api:
  address: 127.0.0.1
  port: %d
diameter:
  address: 127.0.0.1
  port: %d
smpp:
  address: 127.0.0.1
  port: %d
cluster:
  address: 127.0.0.1
  port: %d
`, filepath.Join(n.dir, "smsc.db"), n.apiPort, n.diameterPort, n.smppPort, n.clusterPort)

	if err := os.WriteFile(filepath.Join(n.dir, "smsc.yaml"), []byte(config), 0o600); err != nil {
		n.t.Fatal(err)
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}

	return b
}

func (n *node) start() {
	n.t.Helper()

	n.launch()

	eventually(n.t, n.name+" to answer", func() error {
		_, err := n.request(http.MethodGet, "/api/v1/livez", nil, nil)
		return err
	})
}

func (n *node) launch() {
	n.t.Helper()

	log, err := os.OpenFile(filepath.Join(n.dir, "smsc.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		n.t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), n.bin, "--config", filepath.Join(n.dir, "smsc.yaml"))
	cmd.Stdout, cmd.Stderr = log, log

	if err := cmd.Start(); err != nil {
		n.t.Fatal(err)
	}

	exited := make(chan struct{})

	go func() {
		_ = cmd.Wait()
		_ = log.Close()

		close(exited)
	}()

	n.cmd, n.exited = cmd, exited
}

func (n *node) running() bool {
	if n.cmd == nil {
		return false
	}

	select {
	case <-n.exited:
		return false
	default:
		return true
	}
}

func (n *node) kill() {
	if !n.running() {
		return
	}

	_ = n.cmd.Process.Signal(syscall.SIGCONT)
	_ = n.cmd.Process.Kill()
	<-n.exited
}

func (n *node) stop() {
	if !n.running() {
		return
	}

	_ = n.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-n.exited:
	case <-time.After(30 * time.Second):
		n.kill()
	}
}

func (n *node) freeze() { _ = n.cmd.Process.Signal(syscall.SIGSTOP) }

func (n *node) thaw() { _ = n.cmd.Process.Signal(syscall.SIGCONT) }

func (n *node) token() string {
	if t := n.session.Load(); t != nil {
		return *t
	}

	return ""
}

func (n *node) authenticate(token string) {
	n.session.Store(&token)
}

func (n *node) request(method, path string, body, out any) (int, error) {
	return n.requestAs(n.token(), method, path, body, out)
}

func (n *node) requestAs(token, method, path string, body, out any) (int, error) {
	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}

		reader = bytes.NewReader(raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(n.apiPort)+path, reader)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}

	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &envelope)

	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s = %d: %s", method, path, resp.StatusCode, envelope.Error)
	}

	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return resp.StatusCode, err
		}
	}

	return resp.StatusCode, nil
}

func (n *node) must(method, path string, body, out any) {
	n.t.Helper()

	if _, err := n.request(method, path, body, out); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) login(email, password string) (string, error) {
	var login struct {
		Token string `json:"token"`
	}

	if _, err := n.requestAs("", http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": password}, &login); err != nil {
		return "", err
	}

	return login.Token, nil
}

func (n *node) logIn() {
	n.t.Helper()

	eventually(n.t, n.name+" to accept the admin", func() error {
		token, err := n.login(adminEmail, adminPassword)
		if err != nil {
			return err
		}

		if _, err := n.requestAs(token, http.MethodGet, "/api/v1/cluster", nil, nil); err != nil {
			return err
		}

		n.authenticate(token)

		return nil
	})
}

func (n *node) initialize() {
	n.t.Helper()

	var login struct {
		Token string `json:"token"`
	}

	n.must(http.MethodPost, "/api/v1/init", map[string]string{"email": adminEmail, "password": adminPassword}, &login)
	n.authenticate(login.Token)
}

type member struct {
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	Suffrage   string `json:"suffrage"`
	Leader     bool   `json:"leader"`
	Healthy    *bool  `json:"healthy"`
	DrainState string `json:"drain_state"`
}

type status struct {
	NodeID   string   `json:"node_id"`
	Schema   int      `json:"schema"`
	LeaderID string   `json:"leader_id"`
	Members  []member `json:"members"`
}

func (n *node) status() (status, error) {
	var st status

	_, err := n.request(http.MethodGet, "/api/v1/cluster", nil, &st)

	return st, err
}

func (n *node) diameterHost() string {
	n.t.Helper()

	var d struct {
		Host string `json:"host"`
	}

	n.must(http.MethodGet, "/api/v1/diameter", nil, &d)

	return d.Host
}

type cluster struct {
	t     *testing.T
	nodes []*node
}

func newCluster(t *testing.T) *cluster {
	t.Helper()

	const size = 3

	c := &cluster{t: t}

	for i := range size {
		c.nodes = append(c.nodes, newNode(t, "node"+strconv.Itoa(i)))
	}

	c.nodes[0].initialize()

	for _, n := range c.nodes[1:] {
		c.join(n)
	}

	for _, n := range c.nodes {
		st, err := n.status()
		if err != nil {
			t.Fatal(err)
		}

		n.id = st.NodeID
	}

	c.waitStable()

	return c
}

func (c *cluster) join(n *node) {
	c.t.Helper()

	var token struct {
		Token string `json:"token"`
	}

	c.live().must(http.MethodPost, "/api/v1/cluster/tokens", map[string]int{}, &token)
	n.must(http.MethodPost, "/api/v1/cluster/join", map[string]string{"token": token.Token}, nil)
	n.logIn()
}

func (c *cluster) live() *node {
	for _, n := range c.nodes {
		if n.running() && n.token() != "" {
			return n
		}
	}

	c.t.Fatal("no live node")

	return nil
}

func (c *cluster) waitStable() {
	const voters = 3

	c.t.Helper()

	eventually(c.t, fmt.Sprintf("%d healthy voters", voters), func() error {
		st, err := c.live().status()
		if err != nil {
			return err
		}

		count := 0

		for _, m := range st.Members {
			if m.Suffrage == "voter" && m.Healthy != nil && *m.Healthy {
				count++
			}
		}

		if count != voters {
			return fmt.Errorf("%d healthy voters", count)
		}

		return nil
	})
}

func (c *cluster) leader() *node {
	c.t.Helper()

	var leader *node

	eventually(c.t, "a leader", func() error {
		for _, n := range c.nodes {
			if !n.running() {
				continue
			}

			st, err := n.status()
			if err != nil || st.LeaderID == "" {
				continue
			}

			for _, m := range c.nodes {
				if m.id == st.LeaderID && m.running() {
					leader = m
					return nil
				}
			}
		}

		return errors.New("no leader")
	})

	return leader
}

func (c *cluster) followers(leader *node) []*node {
	var out []*node

	for _, n := range c.nodes {
		if n != leader && n.running() {
			out = append(out, n)
		}
	}

	return out
}

func eventually(t *testing.T, what string, f func() error) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	var err error

	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s: %v", what, err)
}
