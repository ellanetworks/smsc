package esme

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/smpp"
)

func moSubmit(first, dcs byte, udl byte, ud []byte) []byte {
	out := []byte{first, 0x2a, 0x05, 0x81, 0x04, 0x00, 0xf1, 0x00, dcs, udl}

	return append(out, ud...)
}

func moMessage(t *testing.T, env *testEnv, tpduBytes []byte, expires time.Time) int64 {
	t.Helper()

	id, err := env.store.CreateMessage(context.Background(), db.NewMessage{
		Originator:       db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:        db.Address{Digits: "40001", TypeOfNumber: 2, NumberingPlan: 1},
		MSISDN:           "40001",
		Origin:           db.OriginMobile,
		TPDU:             tpduBytes,
		MessageReference: 0x2a,
		SubmittedAt:      env.service.Now(),
		ExpiresAt:        expires,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	return id
}

func moDeliveryResult(t *testing.T, env *testEnv, messageID int64) string {
	t.Helper()

	conn, err := sql.Open("sqlite3", "file:"+env.path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	var result sql.Null[string]

	err = conn.QueryRowContext(context.Background(),
		`SELECT result FROM esme_deliveries WHERE message_id = ?`, messageID).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return "missing"
	}

	if err != nil {
		t.Fatal(err)
	}

	return result.V
}

func waitForMO(t *testing.T, env *testEnv, messageID int64, want string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if got := moDeliveryResult(t, env, messageID); got == want {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("MO delivery on message %d: result %q, want %q", messageID, moDeliveryResult(t, env, messageID), want)
}

func TestMODeliveredToTheReceiver(t *testing.T) {
	env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	id := moMessage(t, env, moSubmit(0x01, 0x04, 0x04, []byte("ping")), c.Now().Add(time.Hour))

	d := conn.next(t)

	if d.Source != (smpp.Address{TON: 1, NPI: 1, Addr: "15551230001"}) {
		t.Fatalf("source = %+v", d.Source)
	}

	if d.Destination != (smpp.Address{TON: 2, NPI: 1, Addr: "40001"}) {
		t.Fatalf("destination = %+v", d.Destination)
	}

	if d.ESMClass != 0 || d.ProtocolID != 0 || d.DataCoding != 0x04 || !bytes.Equal(d.ShortMessage, []byte("ping")) {
		t.Fatalf("deliver_sm = class %#x pid %#x dcs %#x %q", d.ESMClass, d.ProtocolID, d.DataCoding, d.ShortMessage)
	}

	if d.Options.ReceiptedMessageID != nil {
		t.Fatalf("receipted_message_id = %q; a routed message is not a receipt", *d.Options.ReceiptedMessageID)
	}

	if r := d.Options.UserMessageReference; r == nil || *r != 0x2a {
		t.Fatalf("user_message_reference = %v; want the TP-MR 0x2a", r)
	}

	waitForMO(t, env, id, string(db.NotificationAcknowledged))

	if m, err := env.store.GetMessage(context.Background(), id); err != nil || m.Status != db.StatusDelivered {
		t.Fatalf("message = %+v, %v; want %v", m, err, db.StatusDelivered)
	}
}

func TestMODataCodings(t *testing.T) {
	septets, ok := tpdu.GSM7Septets("hello")
	if !ok {
		t.Fatal("no GSM 7 text")
	}

	gsmUDL, gsmUD := tpdu.PackSeptets(nil, septets)

	ucs2 := make([]byte, 0, 4)
	for _, r := range utf16.Encode([]rune{'h', 0x00e9}) {
		ucs2 = binary.BigEndian.AppendUint16(ucs2, r)
	}

	tests := []struct {
		name string
		tpdu []byte
		dcs  byte
		want []byte
	}{
		{"gsm7", moSubmit(0x01, 0x00, gsmUDL, gsmUD), dcDefault, []byte("hello")},
		{"ucs2", moSubmit(0x01, 0x08, byte(len(ucs2)), ucs2), dcUCS2, ucs2},
		{"octet", moSubmit(0x01, 0x04, 0x04, []byte("ping")), dcOctetA, []byte("ping")},
		{"gsm7 class 0", moSubmit(0x01, 0x10, gsmUDL, gsmUD), 0xf0, []byte("hello")},
		{"gsm7 class control", moSubmit(0x01, 0xf2, gsmUDL, gsmUD), 0xf2, []byte("hello")},
		{"octet class 1", moSubmit(0x01, 0x15, 0x04, []byte("ping")), 0xf5, []byte("ping")},
		{"octet class control", moSubmit(0x01, 0xf6, 0x04, []byte("ping")), 0xf6, []byte("ping")},
		{"ucs2 class 1", moSubmit(0x01, 0x19, byte(len(ucs2)), ucs2), dcUCS2, ucs2},
		{"compressed", moSubmit(0x01, 0x24, 0x04, []byte("ping")), dcOctetA, []byte("ping")},
		{"compressed class 1", moSubmit(0x01, 0x35, 0x04, []byte("ping")), 0xf5, []byte("ping")},
		{"compressed ucs2", moSubmit(0x01, 0x28, 0x04, []byte("ping")), dcOctetA, []byte("ping")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
			c := env.run(t)
			conn := env.bind(1, Transceiver, env.esme)

			moMessage(t, env, tc.tpdu, c.Now().Add(time.Hour))

			d := conn.next(t)

			if d.DataCoding != tc.dcs || !bytes.Equal(d.ShortMessage, tc.want) {
				t.Fatalf("deliver_sm dcs %#x = %x; want %#x %x", d.DataCoding, d.ShortMessage, tc.dcs, tc.want)
			}
		})
	}
}

func TestMORejectedByTheESME(t *testing.T) {
	env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
	c := env.run(t)
	conn := env.bindAnswering(1, Receiver, env.esme,
		func(*smpp.DeliverSM) (smpp.CommandStatus, error) { return smpp.StatusXRAppn, nil })

	id := moMessage(t, env, moSubmit(0x01, 0x04, 0x04, []byte("ping")), c.Now().Add(time.Hour))
	conn.next(t)

	waitForMO(t, env, id, string(db.NotificationRejected))

	if m, err := env.store.GetMessage(context.Background(), id); err != nil || m.Status != db.StatusFailed {
		t.Fatalf("message = %+v, %v; want %v", m, err, db.StatusFailed)
	}
}

func TestMOWaitsForAReceiverToBind(t *testing.T) {
	env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
	c := env.run(t)

	id := moMessage(t, env, moSubmit(0x01, 0x04, 0x04, []byte("ping")), c.Now().Add(time.Hour))

	if got := moDeliveryResult(t, env, id); got != "" {
		t.Fatalf("delivery result = %q; want unhandled", got)
	}

	if m, err := env.store.GetMessage(context.Background(), id); err != nil || m.Status != db.StatusDelegated {
		t.Fatalf("message = %+v, %v; want %v", m, err, db.StatusDelegated)
	}

	conn := env.bind(1, Receiver, env.esme)

	env.service.NotificationsQueued()

	d := conn.next(t)

	if !bytes.Equal(d.ShortMessage, []byte("ping")) {
		t.Fatalf("deliver_sm = %q", d.ShortMessage)
	}

	waitForMO(t, env, id, string(db.NotificationAcknowledged))
}

func TestMORetriedOnATemporaryError(t *testing.T) {
	env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
	c := env.run(t)
	conn := env.bindAnswering(1, Receiver, env.esme,
		func(*smpp.DeliverSM) (smpp.CommandStatus, error) { return smpp.StatusSysErr, nil })

	id := moMessage(t, env, moSubmit(0x01, 0x04, 0x04, []byte("ping")), c.Now().Add(24*time.Hour))
	conn.next(t)

	for range 2 {
		conn.none(t)
		c.advance(time.Hour)
		env.service.NotificationsQueued()
		conn.next(t)
	}

	if m, err := env.store.GetMessage(context.Background(), id); err != nil || m.Status != db.StatusDelegated {
		t.Fatalf("message = %+v, %v; want %v", m, err, db.StatusDelegated)
	}

	conn.answer = func(*smpp.DeliverSM) (smpp.CommandStatus, error) { return smpp.StatusOK, nil }

	c.advance(time.Hour)
	env.service.NotificationsQueued()
	conn.next(t)

	waitForMO(t, env, id, string(db.NotificationAcknowledged))
}

func TestMOExpiresWithTheValidityPeriod(t *testing.T) {
	env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
	c := env.run(t)

	id := moMessage(t, env, moSubmit(0x01, 0x04, 0x04, []byte("ping")), c.Now().Add(time.Minute))

	if n, err := env.service.Store.ExpireMODeliveries(context.Background(), c.Now()); err != nil || n != 0 {
		t.Fatalf("expire early = %d, %v", n, err)
	}

	c.advance(2 * time.Minute)

	if n, err := env.service.Store.ExpireMODeliveries(context.Background(), c.Now()); err != nil || n != 1 {
		t.Fatalf("expire = %d, %v", n, err)
	}

	if m, err := env.store.GetMessage(context.Background(), id); err != nil || m.Status != db.StatusExpired {
		t.Fatalf("message = %+v, %v; want %v", m, err, db.StatusExpired)
	}
}

func TestMOUDHIsForwarded(t *testing.T) {
	header := []byte{0x05, 0x00, 0x03, 0xcc, 0x02, 0x01}

	septets, ok := tpdu.GSM7Septets("part")
	if !ok {
		t.Fatal("no GSM 7 text")
	}

	udl, ud := tpdu.PackSeptets(header, septets)

	env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	moMessage(t, env, moSubmit(0x41, 0x00, udl, ud), c.Now().Add(time.Hour))

	d := conn.next(t)

	if d.ESMClass != esmUDHI {
		t.Fatalf("esm_class = %#x; want the UDH bit", d.ESMClass)
	}

	want := append(append([]byte{}, header...), []byte("part")...)
	if !bytes.Equal(d.ShortMessage, want) {
		t.Fatalf("short_message = %x; want %x", d.ShortMessage, want)
	}
}

func TestUndecodableMOFailsAtOnce(t *testing.T) {
	env := newTestEnv(t, func(e *db.ESME) { e.Routes = []string{"40000-40009"} })
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	id, err := env.store.CreateMessage(context.Background(), db.NewMessage{
		Originator:  db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:   db.Address{Digits: "40001", TypeOfNumber: 2, NumberingPlan: 1},
		MSISDN:      "40001",
		Origin:      db.OriginAPI,
		TPDU:        []byte{0x01},
		SubmittedAt: env.service.Now(),
		ExpiresAt:   c.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	waitForMO(t, env, id, string(db.NotificationNoUserData))
	conn.none(t)

	if m, err := env.store.GetMessage(context.Background(), id); err != nil || m.Status != db.StatusFailed {
		t.Fatalf("message = %+v, %v; want %v", m, err, db.StatusFailed)
	}
}
