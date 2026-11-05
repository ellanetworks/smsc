package listener

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster/pki"
)

func newTestListener(t *testing.T, pinned map[string]bool) (*Listener, *tls.Certificate) {
	t.Helper()

	cert, err := pki.GenerateCert(pki.NewID(), pki.NewID(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	l := New(Config{
		Address: "127.0.0.1:0",
		Cert:    func() *tls.Certificate { return &cert },
		Pins: func(fp string) (string, bool) {
			return AnyNode, pinned[fp]
		},
	})

	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(l.Close)

	return l, &cert
}

func echo(s *Stream) {
	for {
		c, err := s.Accept()
		if err != nil {
			return
		}

		go func() {
			defer func() { _ = c.Close() }()

			_, _ = io.Copy(c, c)
		}()
	}
}

func TestRevalidateKeepsJoinConnections(t *testing.T) {
	server, serverCert := newTestListener(t, nil)
	client, _ := newTestListener(t, nil)

	join := server.Stream(ALPNJoin, "")

	t.Cleanup(func() { _ = join.Close() })

	go echo(join)

	serverFP := pki.Fingerprint(serverCert.Leaf)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	conn, _, err := client.Dial(ctx, server.Addr().String(), ALPNJoin, func(fp string) (string, bool) {
		return AnyNode, fp == serverFP
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	roundTrip(t, conn, "before")

	server.Revalidate()

	roundTrip(t, conn, "after")
}

func roundTrip(t *testing.T, conn net.Conn, msg string) {
	t.Helper()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("%s revalidating: %v", msg, err)
	}
}
