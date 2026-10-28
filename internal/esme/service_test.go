package esme

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/ratelimit"
	"github.com/ellanetworks/smsc/smpp"
)

func bindRequest(id smpp.CommandID, systemID, password string, remote string) smpp.BindRequest {
	ap := netip.MustParseAddrPort(remote)

	return smpp.BindRequest{
		Bind:       &smpp.Bind{ID: id, SystemID: systemID, Password: password, InterfaceVersion: 0x34},
		RemoteAddr: &net.TCPAddr{IP: ap.Addr().AsSlice(), Port: int(ap.Port())},
	}
}

// TestBindChecks checks each check of a bind (spec_smpp.md §8.2): every refusal is ESME_RBINDFAIL, which does not tell
// which check failed (SMPP 3.4 section 4.1).
func TestBindChecks(t *testing.T) {
	tests := []struct {
		name string
		role db.Role
		esme func(*db.ESME)
		req  smpp.BindRequest
		ok   bool
	}{
		{name: "transceiver", role: db.RoleSubmitter, req: bindRequest(smpp.CmdBindTransceiver, "ota", "secret", "127.0.0.1:4000"), ok: true},
		{name: "IPv4-mapped IPv6 address", role: db.RoleSubmitter, req: bindRequest(smpp.CmdBindReceiver, "ota", "secret", "[::ffff:127.0.0.1]:4000"), ok: true},
		{name: "unknown system_id", role: db.RoleSubmitter, req: bindRequest(smpp.CmdBindTransceiver, "nobody", "secret", "127.0.0.1:4000")},
		{name: "system_id in another case", role: db.RoleSubmitter, req: bindRequest(smpp.CmdBindTransceiver, "OTA", "secret", "127.0.0.1:4000")},
		{name: "wrong password", role: db.RoleSubmitter, req: bindRequest(smpp.CmdBindTransceiver, "ota", "wrong", "127.0.0.1:4000")},
		{
			name: "interface_version 3.3", role: db.RoleSubmitter,
			req: func() smpp.BindRequest {
				r := bindRequest(smpp.CmdBindTransceiver, "ota", "secret", "127.0.0.1:4000")
				r.Bind.InterfaceVersion = 0x33

				return r
			}(),
		},
		{name: "address outside the networks", role: db.RoleSubmitter, req: bindRequest(smpp.CmdBindTransceiver, "ota", "secret", "10.0.0.1:4000")},
		{
			name: "no networks", role: db.RoleSubmitter, esme: func(e *db.ESME) { e.AllowedNetworks = nil },
			req: bindRequest(smpp.CmdBindTransceiver, "ota", "secret", "127.0.0.1:4000"),
		},
		{name: "Read Only binds a receiver", role: db.RoleReadOnly, req: bindRequest(smpp.CmdBindReceiver, "ota", "secret", "127.0.0.1:4000")},
		{name: "Read Only binds a transmitter", role: db.RoleReadOnly, req: bindRequest(smpp.CmdBindTransmitter, "ota", "secret", "127.0.0.1:4000")},
		{name: "Network Manager binds a transceiver", role: db.RoleNetworkManager, req: bindRequest(smpp.CmdBindTransceiver, "ota", "secret", "127.0.0.1:4000"), ok: true},
		{name: "Admin binds a receiver", role: db.RoleAdmin, req: bindRequest(smpp.CmdBindReceiver, "ota", "secret", "127.0.0.1:4000"), ok: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, tc.esme)

			if err := env.store.UpdateUserRole(context.Background(), env.user.ID, tc.role); err != nil {
				t.Fatal(err)
			}

			out := env.service.Authenticate(context.Background(), tc.req)

			if !tc.ok {
				if out.Status != smpp.StatusBindFail || out.Data != nil {
					t.Fatalf("Authenticate = %+v, want ESME_RBINDFAIL", out)
				}

				return
			}

			sess, _ := out.Data.(*Session)
			if out.Status != smpp.StatusOK || out.Account != strconv.FormatInt(env.esme.ID, 10) || out.MaxSessions != 4 ||
				sess == nil || sess.UserID != env.user.ID {
				t.Fatalf("Authenticate = %+v", out)
			}
		})
	}
}

func TestBindRateLimit(t *testing.T) {
	env := newTestEnv(t, nil)

	for i := range api.LoginRateLimit {
		if out := env.service.Authenticate(context.Background(), bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
			"127.0.0.1:4000")); out.Status != smpp.StatusOK {
			t.Fatalf("bind %d = %s", i+1, out.Status)
		}
	}

	if out := env.service.Authenticate(context.Background(), bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
		"127.0.0.1:4001")); out.Status != smpp.StatusBindFail {
		t.Fatalf("bind %d from one address = %s, want ESME_RBINDFAIL", api.LoginRateLimit+1, out.Status)
	}

	if out := env.service.Authenticate(context.Background(), bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
		"127.0.0.2:4000")); out.Status != smpp.StatusOK {
		t.Fatalf("bind from another address = %s", out.Status)
	}
}

func TestBindAccountFailures(t *testing.T) {
	env := newTestEnv(t, nil)

	for i := range api.AccountFailureLimit {
		// One address per attempt, so that the per-address limit does not count.
		remote := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 1, byte(i / 256), byte(i % 256)}), 4000).String()
		env.service.Authenticate(context.Background(), bindRequest(smpp.CmdBindTransceiver, "ota", "wrong", remote))
	}

	if out := env.service.Authenticate(context.Background(), bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
		"127.0.0.1:4000")); out.Status != smpp.StatusBindFail {
		t.Fatalf("bind after %d wrong passwords = %s, want ESME_RBINDFAIL", api.AccountFailureLimit, out.Status)
	}
}

// TestBindFailuresOutsideTheNetworks checks that the wrong passwords from outside an account's networks neither count
// toward its lockout nor are refused by it, and that unknown system_ids, counted with them, cannot fill the account
// limiter (SRV-M1).
func TestBindFailuresOutsideTheNetworks(t *testing.T) {
	env := newTestEnv(t, nil)
	ctx := context.Background()

	outside := func(i int) string {
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 1, byte(i / 256), byte(i % 256)}), 4000).String()
	}

	for i := range api.AccountFailureLimit {
		env.service.Authenticate(ctx, bindRequest(smpp.CmdBindTransceiver, "ota", "wrong", outside(i)))
	}

	if out := env.service.Authenticate(ctx, bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
		"127.0.0.1:4000")); out.Status != smpp.StatusOK {
		t.Fatalf("bind after %d wrong passwords from outside = %s, want ESME_ROK", api.AccountFailureLimit, out.Status)
	}

	// The outsiders' limit refuses outsiders only.
	if out := env.service.Authenticate(ctx, bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
		outside(1000))); out.Status != smpp.StatusBindFail {
		t.Fatalf("bind from outside = %s, want ESME_RBINDFAIL", out.Status)
	}

	// A full limiter of unknown system_ids does not refuse a known account.
	for i := range ratelimit.MaxKeys {
		env.service.outsiders.Allow("unknown" + strconv.Itoa(i))
	}

	if out := env.service.Authenticate(ctx, bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
		"127.0.0.2:4000")); out.Status != smpp.StatusOK {
		t.Fatalf("bind with a full limiter of unknown system_ids = %s, want ESME_ROK", out.Status)
	}
}

// fakeAccounts is the server's registry.
type fakeAccounts struct{ closed []string }

func (f *fakeAccounts) CloseAccount(account string) int {
	f.closed = append(f.closed, account)
	return 0
}

func TestRevocation(t *testing.T) {
	env := newTestEnv(t, nil)
	accounts := &fakeAccounts{}
	env.service.Accounts = accounts

	if out := env.service.Authenticate(context.Background(), bindRequest(smpp.CmdBindTransceiver, "ota", "secret",
		"127.0.0.1:4000")); out.Status != smpp.StatusOK {
		t.Fatal(out.Status)
	}

	conn := env.bind(1, Transceiver, env.esme)

	if got := env.service.Sessions(env.esme.ID); len(got) != 1 || got[0].BindType != Transceiver {
		t.Fatalf("Sessions = %+v", got)
	}

	env.service.CloseESME(env.esme.ID + 1)

	if conn.closed.Load() {
		t.Fatal("closing another ESME closed the session")
	}

	if err := env.service.CloseUser(context.Background(), env.user.ID); err != nil {
		t.Fatal(err)
	}

	if !conn.closed.Load() || len(accounts.closed) != 2 || accounts.closed[1] != strconv.FormatInt(env.esme.ID, 10) {
		t.Fatalf("CloseUser: session closed %t, accounts closed %v", conn.closed.Load(), accounts.closed)
	}

	conn2 := env.bind(2, Receiver, env.esme)
	env.service.CloseESME(env.esme.ID)

	if !conn2.closed.Load() {
		t.Fatal("CloseESME did not close the session")
	}
}

func TestSubmitOnReceiver(t *testing.T) {
	env := newTestEnv(t, nil)

	if _, status := env.service.SubmitSM(context.Background(), env.session(Receiver),
		&smpp.SubmitSM{Message: baseSubmit(0x04, "aa")}); status != smpp.StatusInvBndSts {
		t.Fatalf("submit_sm on a receiver = %s", status)
	}
}

// TestSubmitStoresSMPP checks what a submit_sm stores for its notifications (spec_smpp.md §5.5, §5.6).
func TestSubmitStoresSMPP(t *testing.T) {
	tests := []struct {
		name      string
		esme      func(*db.ESME)
		sm        smpp.Message
		receipt   uint8
		simAnswer bool
		expires   time.Time
	}{
		{name: "none", sm: baseSubmit(0x04, "aa")},
		{name: "final receipt", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.RegisteredDelivery = 0x01 }), receipt: 1},
		{name: "failure receipt", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.RegisteredDelivery = 0x02 }), receipt: 2},
		{name: "reserved receipt", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.RegisteredDelivery = 0x03 })},
		{name: "SME delivery acknowledgement", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.RegisteredDelivery = 0x04 })},
		{name: "user acknowledgement", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.RegisteredDelivery = 0x08 }), simAnswer: true},
		{name: "both acknowledgements and intermediate", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.RegisteredDelivery = 0x1d }), receipt: 1, simAnswer: true},
		{
			name: "PID 0x7F without a user acknowledgement",
			sm:   with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ProtocolID = 0x7f }),
		},
		{
			name: "relative validity", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ValidityPeriod = "000001020000000R" }),
			expires: testNow.Add(26 * time.Hour),
		},
		{
			name: "absolute validity", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ValidityPeriod = "261029120000004+" }),
			expires: time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, tc.esme)

			m := mapping{esme: env.esme, userID: env.user.ID, plan: env.settings.Operator.Numbering, now: testNow}

			r, rej := m.request(&tc.sm)
			if rej != nil {
				t.Fatal(rej)
			}

			if r.SMPP.Receipt != tc.receipt || r.SMPP.SIMAnswer != tc.simAnswer || !r.ExpiresAt.Equal(tc.expires) ||
				r.SMPP.ESMEID != env.esme.ID || r.Origin != db.OriginSMPP || r.SubmittedBy != env.user.ID {
				t.Fatalf("request %+v, smpp %+v", r, *r.SMPP)
			}
		})
	}
}

// TestCloseUserWithoutABind checks that CloseUser finds the ESMEs of a user in the database: a session of an ESME that
// has not completed a bind, such as one binding while the role changes, is closed through the server's registry
// (SRV-L7).
func TestCloseUserWithoutABind(t *testing.T) {
	env := newTestEnv(t, nil)
	accounts := &fakeAccounts{}
	env.service.Accounts = accounts

	if err := env.service.CloseUser(context.Background(), env.user.ID); err != nil {
		t.Fatal(err)
	}

	if want := strconv.FormatInt(env.esme.ID, 10); len(accounts.closed) != 1 || accounts.closed[0] != want {
		t.Fatalf("accounts closed = %v, want [%s]", accounts.closed, want)
	}

	// Another user has no ESME.
	accounts.closed = nil

	if err := env.service.CloseUser(context.Background(), env.user.ID+1); err != nil || len(accounts.closed) != 0 {
		t.Fatalf("CloseUser of another user = %v, closed %v", err, accounts.closed)
	}
}
