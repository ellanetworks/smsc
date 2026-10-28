package esme

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/ellanetworks/smsc/smpp"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/bcrypt"
)

var testNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// destination is the SMS-SUBMIT TP-DA of +15551230002 (TS 23.040 clause 9.1.2.5).
const destination = "0b915155210300f2"

type testEnv struct {
	store *db.DB
	// path is the database's file, which a test reads apart from the store.
	path     string
	service  *Service
	received *intake.Received
	esme     db.ESME
	user     db.User
	settings settings.Settings
}

func newTestEnv(t *testing.T, edit func(*db.ESME)) *testEnv {
	t.Helper()

	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "smsc.db")

	store, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	userID, err := store.CreateUser(ctx, db.NewUser{Email: "ota@example.com", Role: db.RoleSubmitter, PasswordHash: string(hash)})
	if err != nil {
		t.Fatal(err)
	}

	e := db.ESME{
		SystemID: "ota", UserID: userID, PasswordHash: string(hash), CreatedAt: testNow,
		AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	}

	if edit != nil {
		edit(&e)
	}

	if e.ID, err = store.CreateESME(ctx, e); err != nil {
		t.Fatal(err)
	}

	current, err := store.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	user, err := store.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}

	received := intake.New()
	now := func() time.Time { return testNow }

	return &testEnv{
		store: store,
		path:  path,
		service: &Service{
			Store:      store,
			Submitter:  &submission.Submitter{Store: store, Settings: func() settings.Settings { return current }, Received: received, Now: now},
			Settings:   func() settings.Settings { return current },
			Received:   received,
			BcryptCost: bcrypt.MinCost,
			Now:        now,
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		received: received,
		esme:     e,
		user:     user,
		settings: current,
	}
}

func (e *testEnv) session(t BindType) *Session {
	return &Session{ID: 1, ESME: e.esme, UserID: e.user.ID, BindType: t}
}

// submit sends a submit_sm on a transceiver session, and returns the stored SMS-SUBMIT in hex, or the status.
func (e *testEnv) submit(t *testing.T, sm smpp.Message) (string, smpp.CommandStatus) {
	t.Helper()

	id, status := e.service.SubmitSM(context.Background(), e.session(Transceiver), &smpp.SubmitSM{Message: sm})
	if status != smpp.StatusOK {
		return "", status
	}

	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("message_id %q is not decimal", id)
	}

	m, err := e.store.GetMessage(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(m.TPDU), status
}

func unhex(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}

	return b
}

func ptr[T any](v T) *T { return &v }

// baseSubmit is a submit_sm from +15550001000 to +15551230002 with the given user data.
func baseSubmit(dc byte, sm string) smpp.Message {
	return smpp.Message{
		Source:       smpp.Address{TON: 1, NPI: 1, Addr: "15550001000"},
		Destination:  smpp.Address{TON: 1, NPI: 1, Addr: "15551230002"},
		DataCoding:   dc,
		ShortMessage: unhex(sm),
	}
}

func with(m smpp.Message, edit func(*smpp.Message)) smpp.Message {
	edit(&m)
	return m
}

// TestSubmitMapping checks each rule of the submit_sm mapping (spec_smpp.md §5) against the exact SMS-SUBMIT that
// the SMSC stores, or the command_status that refuses it (SMPP 3.4 section 5.1.3, Table 5-2).
func TestSubmitMapping(t *testing.T) {
	tests := []struct {
		name   string
		esme   func(*db.ESME)
		sm     smpp.Message
		want   string
		status smpp.CommandStatus
	}{
		{
			// smpp-ota-tool.py: esm_class 0x40, PID 0x7F, data_coding 0x02. TP-DCS becomes 0xF6, class 2 (TS 23.040
			// clause 9.2.3.9), and TP-UDHI is set.
			name: "pySim OTA submit",
			sm: with(baseSubmit(0x02, "027000 0011aabb"), func(m *smpp.Message) {
				m.ESMClass, m.ProtocolID = 0x40, 0x7f
			}),
			want: "4100" + destination + "7ff6 07 0270000011aabb",
		},
		{
			name: "8-bit 0x04 is TP-DCS 0x04",
			sm:   baseSubmit(0x04, "aabb"),
			want: "0100" + destination + "0004 02 aabb",
		},
		{
			name: "0x04 and the smart card subunit is class 2, 0x16",
			sm:   with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.Options.DestAddrSubunit = ptr[byte](3) }),
			want: "0100" + destination + "0016 02 aabb",
		},
		{
			name: "0x04, PID 0x7F and the smart card subunit stays 0x16",
			sm: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
				m.ProtocolID, m.Options.DestAddrSubunit = 0x7f, ptr[byte](3)
			}),
			want: "0100" + destination + "7f16 02 aabb",
		},
		{
			name:   "0xF5 (class 1) and the smart card subunit (class 2) conflict",
			sm:     with(baseSubmit(0xf5, "aabb"), func(m *smpp.Message) { m.Options.DestAddrSubunit = ptr[byte](3) }),
			status: smpp.StatusInvOptParamVal,
		},
		{
			name: "a reserved subunit changes nothing",
			sm:   with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.Options.DestAddrSubunit = ptr[byte](9) }),
			want: "0100" + destination + "0004 02 aabb",
		},
		{
			// "hi" in unpacked septets, packed by the SC (TS 23.038 clause 6.1.2.1.1).
			name: "0x00 with the GSM 7 bit default alphabet",
			sm:   baseSubmit(0x00, "6869"),
			want: "0100" + destination + "0000 02 e834",
		},
		{
			// A 6-octet UDH takes 7 septets with one fill bit (TS 23.040 clause 9.2.3.24).
			name: "0x00 septets after a UDH, with fill bits",
			sm:   with(baseSubmit(0x00, "050003010201 6869"), func(m *smpp.Message) { m.ESMClass = 0x40 }),
			want: "4100" + destination + "0000 09 050003010201d069",
		},
		{
			name:   "0x00 with an octet above 0x7F",
			sm:     baseSubmit(0x00, "68e9"),
			status: smpp.StatusSubmitFail,
		},
		{
			name: "0x03 is Latin-1: é is in the GSM 7 bit alphabet",
			sm:   baseSubmit(0x03, "e9"),
			want: "0100" + destination + "0000 01 05",
		},
		{
			name: "IA5 is converted to the GSM 7 bit alphabet",
			sm:   baseSubmit(0x01, "6869"),
			want: "0100" + destination + "0000 02 e834",
		},
		{
			name:   "IA5 above 0x7F",
			sm:     baseSubmit(0x01, "80"),
			status: smpp.StatusSubmitFail,
		},
		{
			name: "Latin-1 ê has no GSM 7 bit equivalent: UCS2",
			sm:   baseSubmit(0x03, "ea"),
			want: "0100" + destination + "0008 02 00ea",
		},
		{
			name: "UCS2 is copied",
			sm:   baseSubmit(0x08, "00ea0041"),
			want: "0100" + destination + "0008 04 00ea0041",
		},
		{
			name:   "UCS2 of an odd number of octets",
			sm:     baseSubmit(0x08, "00ea00"),
			status: smpp.StatusSubmitFail,
		},
		{
			name:   "MWI control, store, is not supported",
			sm:     baseSubmit(0xd8, "6869"),
			status: smpp.StatusSubmitFail,
		},
		{
			name:   "MWI control, discard, is not supported either",
			sm:     baseSubmit(0xcc, "6869"),
			status: smpp.StatusSubmitFail,
		},
		{
			name: "message class control 0xF6 is copied",
			sm:   with(baseSubmit(0xf6, "aabb"), func(m *smpp.Message) { m.ProtocolID = 0x7f }),
			want: "0100" + destination + "7ff6 02 aabb",
		},
		{
			name: "message class control 0xF1 is GSM 7 bit",
			sm:   baseSubmit(0xf1, "6869"),
			want: "0100" + destination + "00f1 02 e834",
		},
		{name: "0xF8 sets the reserved bit 3", sm: baseSubmit(0xf8, "aabb"), status: smpp.StatusSubmitFail},
		{name: "JIS 0x05", sm: baseSubmit(0x05, "aabb"), status: smpp.StatusSubmitFail},
		{name: "reserved 0x0B", sm: baseSubmit(0x0b, "aabb"), status: smpp.StatusSubmitFail},
		{name: "reserved 0x10", sm: baseSubmit(0x10, "aabb"), status: smpp.StatusSubmitFail},
		{name: "reserved 0xE0", sm: baseSubmit(0xe0, "aabb"), status: smpp.StatusSubmitFail},
		{
			name:   "PID 0x7F in the GSM 7 bit alphabet is not class 2",
			sm:     with(baseSubmit(0x00, "6869"), func(m *smpp.Message) { m.ProtocolID = 0x7f }),
			status: smpp.StatusSubmitFail,
		},
		{
			name:   "telematic interworking",
			sm:     with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.ProtocolID = 0x21 }),
			status: smpp.StatusSubmitFail,
		},
		{
			name:   "device trigger without an application port",
			sm:     with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.ProtocolID = 0x48 }),
			status: smpp.StatusSubmitFail,
		},
		{
			name: "replace type PID 0x41 is copied",
			sm:   with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.ProtocolID = 0x41 }),
			want: "0100" + destination + "4104 02 aabb",
		},
		{
			name:   "a UDH longer than the user data",
			sm:     with(baseSubmit(0x04, "0500"), func(m *smpp.Message) { m.ESMClass = 0x40 }),
			status: smpp.StatusSubmitFail,
		},
		{
			name:   "a malformed UDH",
			sm:     with(baseSubmit(0x04, "020003"), func(m *smpp.Message) { m.ESMClass = 0x40 }),
			status: smpp.StatusSubmitFail,
		},
		{
			name: "sar_* with an 8-bit reference",
			sm: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
				m.Options.SarMsgRefNum, m.Options.SarTotalSegments, m.Options.SarSegmentSeqnum = ptr[uint16](0x12), ptr[byte](2), ptr[byte](1)
			}),
			want: "4100" + destination + "0004 08 050003120201aabb",
		},
		{
			name: "sar_* with a 16-bit reference",
			sm: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
				m.Options.SarMsgRefNum, m.Options.SarTotalSegments, m.Options.SarSegmentSeqnum = ptr[uint16](0x1234), ptr[byte](2), ptr[byte](2)
			}),
			want: "4100" + destination + "0004 09 06080412340202aabb",
		},
		{
			name: "incomplete sar_* are ignored",
			sm:   with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.Options.SarMsgRefNum = ptr[uint16](1) }),
			want: "0100" + destination + "0004 02 aabb",
		},
		{
			name: "sar_segment_seqnum above sar_total_segments",
			sm: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
				m.Options.SarMsgRefNum, m.Options.SarTotalSegments, m.Options.SarSegmentSeqnum = ptr[uint16](1), ptr[byte](2), ptr[byte](3)
			}),
			status: smpp.StatusInvOptParamVal,
		},
		{
			name: "sar_total_segments 0",
			sm: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
				m.Options.SarMsgRefNum, m.Options.SarTotalSegments, m.Options.SarSegmentSeqnum = ptr[uint16](1), ptr[byte](0), ptr[byte](0)
			}),
			status: smpp.StatusInvOptParamVal,
		},
		{
			name: "ports make the 16-bit application port element",
			sm: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
				m.Options.DestinationPort, m.Options.SourcePort = ptr[uint16](2948), ptr[uint16](9200)
			}),
			want: "4100" + destination + "0004 09 0605040b8423f0aabb",
		},
		{
			name: "ports with UDHI",
			sm: with(baseSubmit(0x04, "027000aabb"), func(m *smpp.Message) {
				m.ESMClass, m.Options.DestinationPort = 0x40, ptr[uint16](2948)
			}),
			status: smpp.StatusOptParNotAllwd,
		},
		{
			name: "short_message and message_payload",
			sm: with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
				m.Options.MessagePayload = []byte{1}
			}),
			status: smpp.StatusOptParNotAllwd,
		},
		{
			name: "message_payload",
			sm: with(baseSubmit(0x04, ""), func(m *smpp.Message) {
				m.Options.MessagePayload = unhex("aabbcc")
			}),
			want: "0100" + destination + "0004 03 aabbcc",
		},
		{
			name: "141 octets do not fit one TPDU",
			sm: with(baseSubmit(0x04, ""), func(m *smpp.Message) {
				m.Options.MessagePayload = make([]byte, 141)
			}),
			status: smpp.StatusInvMsgLen,
		},
		{
			name: "161 septets do not fit one TPDU",
			sm: with(baseSubmit(0x00, ""), func(m *smpp.Message) {
				m.Options.MessagePayload = make([]byte, 161)
			}),
			status: smpp.StatusInvMsgLen,
		},
		{
			name:   "IA5 converted to more than 140 octets of UCS2",
			sm:     baseSubmit(0x01, strings.Repeat("60", 71)),
			status: smpp.StatusInvMsgLen,
		},
		{name: "datagram mode", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ESMClass = 0x01 }), status: smpp.StatusInvEsmClass},
		{name: "transaction mode", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ESMClass = 0x02 }), status: smpp.StatusInvEsmClass},
		{name: "ESME delivery acknowledgement", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ESMClass = 0x08 }), status: smpp.StatusInvEsmClass},
		{name: "ESME user acknowledgement", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ESMClass = 0x10 }), status: smpp.StatusInvEsmClass},
		{name: "reply path", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ESMClass = 0x80 }), status: smpp.StatusInvEsmClass},
		{
			name: "store and forward mode and a reserved message type are accepted",
			sm:   with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ESMClass = 0x07 }),
			want: "0100" + destination + "0004 01 aa",
		},
		{
			name: "national destination with the country code",
			sm:   with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination = smpp.Address{TON: 2, NPI: 1, Addr: "5551230002"} }),
			want: "0100" + destination + "0004 01 aa",
		},
		{
			name: "unknown destination with the national prefix",
			sm:   with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination = smpp.Address{Addr: "15551230002"} }),
			want: "0100" + destination + "0004 01 aa",
		},
		{name: "network specific destination", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination.TON = 3 }), status: smpp.StatusInvDstTON},
		{name: "alphanumeric destination", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination.TON = 5 }), status: smpp.StatusInvDstTON},
		{name: "private numbering plan destination", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination.NPI = 9 }), status: smpp.StatusInvDstNPI},
		{name: "destination not digits", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination.Addr = "+1555" }), status: smpp.StatusInvDstAdr},
		{name: "destination with a leading 0", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination.Addr = "05551230002" }), status: smpp.StatusInvDstAdr},
		{name: "destination of 16 digits", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Destination.Addr = "1555123000212345" }), status: smpp.StatusInvDstAdr},
		{name: "empty source", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Source.Addr = "" }), status: smpp.StatusInvSrcAdr},
		{name: "international source with the data plan", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Source.NPI = 3 }), status: smpp.StatusInvSrcNPI},
		{name: "source name too long", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Source = smpp.Address{TON: 5, Addr: "Ella Networks"} }), status: smpp.StatusInvSrcAdr},
		{name: "reserved source TON", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Source.TON = 7 }), status: smpp.StatusInvSrcTON},
		{name: "short code source with E.212", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.Source = smpp.Address{TON: 3, NPI: 6, Addr: "1234"} }), status: smpp.StatusInvSrcNPI},
		{name: "schedule_delivery_time", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ScheduleDeliveryTime = "000001000000000R" }), status: smpp.StatusInvSched},
		{name: "malformed validity_period", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ValidityPeriod = "26102812" }), status: smpp.StatusInvExpiry},
		{name: "validity_period nn 49", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ValidityPeriod = "261029120000049+" }), status: smpp.StatusInvExpiry},
		{name: "past validity_period", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ValidityPeriod = "261027120000000+" }), status: smpp.StatusInvExpiry},
		{name: "replace_if_present_flag 1", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ReplaceIfPresentFlag = 1 }), status: smpp.StatusInvSubRep},
		{
			name: "reserved replace_if_present_flag is the default",
			sm:   with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.ReplaceIfPresentFlag = 2 }),
			want: "0100" + destination + "0004 01 aa",
		},
		{name: "canned message", sm: with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.SMDefaultMsgID = 1 }), status: smpp.StatusInvDftMsgID},
		{
			name: "reserved sm_default_msg_id 255 and priority_flag 4 are ignored",
			sm:   with(baseSubmit(0x04, "aa"), func(m *smpp.Message) { m.SMDefaultMsgID, m.PriorityFlag = 255, 4 }),
			want: "0100" + destination + "0004 01 aa",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, tc.esme)

			got, status := env.submit(t, tc.sm)
			if status != tc.status {
				t.Fatalf("status = %s, want %s", status, tc.status)
			}

			if want := strings.ReplaceAll(tc.want, " ", ""); got != want {
				t.Fatalf("SMS-SUBMIT = %s, want %s", got, want)
			}

			result := intake.Accepted
			if tc.status != smpp.StatusOK {
				result = intake.Rejected
			}

			for _, r := range []string{intake.Accepted, intake.Rejected, intake.Error} {
				want := 0.0
				if r == result {
					want = 1
				}

				if n := receivedCount(t, env.received, r); n != want {
					t.Errorf("received_total{origin=smpp,result=%s} = %v, want %v", r, n, want)
				}
			}

			if tc.status != smpp.StatusOK {
				if _, total, err := env.store.ListMessages(context.Background(), db.MessageFilter{}, 1, 10); err != nil || total != 0 {
					t.Fatalf("%d messages stored, %v; want none", total, err)
				}
			}
		})
	}
}

// receivedCount returns ellasmsc_messages_received_total{origin="smpp"} of a result.
func receivedCount(t *testing.T, r *intake.Received, result string) float64 {
	t.Helper()

	reg := prometheus.NewRegistry()
	reg.MustRegister(r.Collectors()...)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range families {
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}

			if labels["origin"] == intake.OriginSMPP && labels["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// TestSubmitFailuresAreCounted checks that a submit_sm the SMSC fails to encode counts as an error, once, as one it
// fails to store does through the Submitter (SRV-L6).
func TestSubmitFailuresAreCounted(t *testing.T) {
	tests := []struct {
		err    error
		status smpp.CommandStatus
		result string
	}{
		{fmt.Errorf("%w: x", submission.ErrEncode), smpp.StatusSysErr, intake.Error},
		{fmt.Errorf("%w: x", submission.ErrEncodeText), smpp.StatusSysErr, intake.Error},
		{fmt.Errorf("%w: x", submission.ErrStore), smpp.StatusSysErr, ""},
		{submission.ErrUserDataTooLong, smpp.StatusInvMsgLen, intake.Rejected},
	}

	for _, tc := range tests {
		t.Run(tc.err.Error(), func(t *testing.T) {
			env := newTestEnv(t, nil)
			env.service.init()

			if got := env.service.submitFailed(env.service.Logger, tc.err); got != tc.status {
				t.Fatalf("status = %s, want %s", got, tc.status)
			}

			for _, r := range []string{intake.Accepted, intake.Rejected, intake.Error} {
				want := 0.0
				if r == tc.result {
					want = 1
				}

				if n := receivedCount(t, env.received, r); n != want {
					t.Errorf("received_total{origin=smpp,result=%s} = %v, want %v", r, n, want)
				}
			}
		})
	}
}
