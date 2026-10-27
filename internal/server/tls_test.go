package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/server"
)

// writeCertificate writes a self-signed certificate for the loopback address, and its key, and returns their paths.
func writeCertificate(t testing.TB, dir string) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "smsc"},
		IPAddresses:  []net.IP{net.ParseIP(loopback.String())},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")

	for path, block := range map[string]*pem.Block{
		certPath: {Type: "CERTIFICATE", Bytes: der},
		keyPath:  {Type: "PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return certPath, keyPath
}

// trustingClient is an HTTPS client that trusts the certificate at certPath.
func trustingClient(t testing.TB, certPath string) *http.Client {
	t.Helper()

	pemCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemCert) {
		t.Fatal("no certificate")
	}

	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
}

func TestAPIOverTLS(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, filepath.Join(dir, "smsc.db"))
	certPath, keyPath := writeCertificate(t, dir)
	cfg.API.TLS = &config.TLS{Cert: certPath, Key: keyPath}

	// The harness creates the admin over TLS.
	s := startSMSC(t, cfg)
	client := trustingClient(t, certPath)

	body := `{"email": "admin@example.com", "password": "correct horse"}`

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.url+"/api/v1/auth/login",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || resp.TLS == nil {
		t.Fatalf("login over TLS = %d, TLS %v", resp.StatusCode, resp.TLS != nil)
	}

	var secure bool

	for _, c := range resp.Cookies() {
		if c.Name == api.SecureSessionCookieName {
			secure = c.Secure
		}
	}

	if !secure {
		t.Fatalf("no Secure %s cookie over TLS", api.SecureSessionCookieName)
	}

	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts == "" {
		t.Fatal("no Strict-Transport-Security over TLS")
	}

	// Plain HTTP on the TLS port gets no API.
	plain, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+s.server.APIAddr().String()+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp, err := http.DefaultClient.Do(plain); err == nil {
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("plain HTTP to the TLS port = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	}

	// TLS 1.1 and older are refused.
	old := &tls.Config{RootCAs: client.Transport.(*http.Transport).TLSClientConfig.RootCAs, MaxVersion: tls.VersionTLS11} //nolint:gosec // The test checks that the server refuses it.

	dialer := tls.Dialer{Config: old}

	conn, err := dialer.DialContext(context.Background(), "tcp", s.server.APIAddr().String())
	if err == nil {
		_ = conn.Close()

		t.Fatal("TLS 1.1 handshake succeeded")
	}
}

func TestStartFailsWithAMismatchedKey(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeCertificate(t, dir)

	other := filepath.Join(dir, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}

	_, otherKey := writeCertificate(t, other)

	cfg := testConfig(t, filepath.Join(dir, "smsc.db"))
	cfg.API.TLS = &config.TLS{Cert: certPath, Key: otherKey}

	srv := &server.Server{Config: cfg.Config, Logger: discardLogger()}

	err := srv.Start(context.Background())
	if err == nil {
		srv.Shutdown(context.Background())
		t.Fatal("Start with a key of another certificate succeeded")
	}

	if !strings.Contains(err.Error(), "API certificate") {
		t.Fatalf("Start = %v, want an error about the API certificate", err)
	}
}
