package smpp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// rawPeer is the far end of a net.Pipe, which writes and reads PDUs with the codec only.
type rawPeer struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func (p *rawPeer) sendRaw(b []byte) {
	p.t.Helper()

	_ = p.c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := p.c.Write(b); err != nil {
		p.t.Fatalf("peer write: %v", err)
	}
}

func (p *rawPeer) send(pdu PDU, status CommandStatus, seq uint32) {
	p.t.Helper()

	b, err := Encode(pdu, status, seq)
	if err != nil {
		p.t.Fatal(err)
	}

	p.sendRaw(b)
}

func (p *rawPeer) read() (Header, PDU) {
	p.t.Helper()

	_ = p.c.SetReadDeadline(time.Now().Add(2 * time.Second))

	h, body, err := ReadPDU(p.r)
	if err != nil {
		p.t.Fatalf("peer read: %v", err)
	}

	if h.ID == CmdGenericNack {
		return h, &GenericNack{}
	}

	d, err := DecodeBody(h, body)
	if err != nil {
		p.t.Fatalf("peer decode %s: %v", h.ID, err)
	}

	return h, d
}

// expect reads a PDU and checks its header.
func (p *rawPeer) expect(id CommandID, status CommandStatus, seq uint32) PDU {
	p.t.Helper()

	h, d := p.read()
	if h.ID != id || h.Status != status || h.Sequence != seq {
		p.t.Fatalf("peer got %s %s seq %d, want %s %s seq %d", h.ID, h.Status, h.Sequence, id, status, seq)
	}

	return d
}

// expectClosed checks that the session closes the connection.
func (p *rawPeer) expectClosed() {
	p.t.Helper()

	_ = p.c.SetReadDeadline(time.Now().Add(2 * time.Second))

	for {
		if _, _, err := ReadPDU(p.r); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				p.t.Fatal("the session did not close the connection")
			}

			return
		}
	}
}

func pipe(t *testing.T, r role, cfg SessionConfig, h Handler, hk hooks) (*Session, *rawPeer) {
	t.Helper()

	a, b := net.Pipe()
	s := newSession(a, r, cfg, h, hk)
	s.start()

	t.Cleanup(func() {
		_ = s.Close()
		_ = b.Close()
	})

	return s, &rawPeer{t: t, c: b, r: bufio.NewReader(b)}
}

// acceptAll is the bind hook of an SMSC that accepts every bind.
func acceptAll(context.Context, *Session, *Bind) bindDecision {
	return bindDecision{status: StatusOK, account: "acct"}
}

// submitOK answers every submit_sm with message_id "m1".
var submitOK = HandlerFunc(func(context.Context, *Session, Request) Response {
	return Response{PDU: &SubmitSMResp{MessageID: "m1"}}
})

func smscHooks() hooks {
	return hooks{systemID: "EllaSMSC", bind: acceptAll}
}

// boundSMSC is an SMSC session that the peer, an ESME, has bound with bind id.
func boundSMSC(t *testing.T, id CommandID, cfg SessionConfig, h Handler) (*Session, *rawPeer) {
	t.Helper()

	s, p := pipe(t, roleSMSC, cfg, h, smscHooks())
	p.send(&Bind{ID: id, SystemID: "esme", Password: "pw", InterfaceVersion: InterfaceVersion}, StatusOK, 1)
	p.expect(id.Response(), StatusOK, 1)
	waitState(t, s, boundState(id))

	return s, p
}

func waitState(t *testing.T, s *Session, want State) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for s.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s", s.State(), want)
		}

		time.Sleep(time.Millisecond)
	}
}

func waitClosed(t *testing.T, s *Session) error {
	t.Helper()

	select {
	case <-s.Done():
		return s.Err()
	case <-time.After(3 * time.Second):
		t.Fatal("the session did not close")

		return nil
	}
}

func message() Message {
	return Message{
		Source: Address{TON: 1, NPI: 1, Addr: "15550001000"}, Destination: Address{TON: 1, NPI: 1, Addr: "15551230002"},
		ShortMessage: []byte("hi"),
	}
}

func TestBindResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      CommandID
		version byte
		status  CommandStatus
		want    *BindResp
	}{
		{
			"transceiver v3.4: sc_interface_version (§3.4, §5.3.2.25)", CmdBindTransceiver, 0x34, StatusOK,
			&BindResp{ID: CmdBindTransceiverResp, SystemID: "EllaSMSC", Options: Options{SCInterfaceVersion: new(byte(0x34))}},
		},
		{
			"receiver v3.3: no optional parameter (§3.4)", CmdBindReceiver, 0x33, StatusOK,
			&BindResp{ID: CmdBindReceiverResp, SystemID: "EllaSMSC"},
		},
		{
			"transceiver refused: system_id kept (Table 4-6)", CmdBindTransceiver, 0x34, StatusInvPaswd,
			&BindResp{ID: CmdBindTransceiverResp, SystemID: "EllaSMSC"},
		},
		{
			"transmitter refused: no body (Table 4-2)", CmdBindTransmitter, 0x34, StatusBindFail,
			&BindResp{ID: CmdBindTransmitterResp},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := pipe(t, roleSMSC, SessionConfig{}, submitOK, hooks{
				systemID: "EllaSMSC",
				bind: func(context.Context, *Session, *Bind) bindDecision {
					return bindDecision{status: tc.status}
				},
			})

			p.send(&Bind{ID: tc.id, SystemID: "esme", InterfaceVersion: tc.version}, StatusOK, 7)

			got := p.expect(tc.id.Response(), tc.status, 7).(*BindResp)
			if got.ID != tc.want.ID || got.SystemID != tc.want.SystemID ||
				(got.Options.SCInterfaceVersion == nil) != (tc.want.Options.SCInterfaceVersion == nil) || len(got.Options.Other) != 0 {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}

			want := StateOpen
			if tc.status == StatusOK {
				want = boundState(tc.id)
			}

			waitState(t, s, want)

			if tc.status == StatusOK && (s.SystemID() != "esme" || s.Info().InterfaceVersion != tc.version) {
				t.Fatalf("info %+v", s.Info())
			}
		})
	}
}

// TestStateErrors checks each request against the state of Table 2-1 and the checks of §3.3 and §4.3.
func TestStateErrors(t *testing.T) {
	submit := &SubmitSM{Message: message()}
	deliver := &DeliverSM{Message: message()}

	header := func(id CommandID, seq uint32, body ...byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(HeaderLength+len(body)))
		b = binary.BigEndian.AppendUint32(b, uint32(id))
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, seq)

		return append(b, body...)
	}

	smLength255 := func() []byte {
		b, err := Encode(&SubmitSM{Message: message()}, StatusOK, 4)
		if err != nil {
			t.Fatal(err)
		}

		b[len(b)-3] = 255 // sm_length of "hi"

		return b
	}

	for _, tc := range []struct {
		name   string
		role   role
		bind   CommandID // 0: stay OPEN
		send   PDU
		raw    []byte
		id     CommandID
		status CommandStatus
		seq    uint32
	}{
		{"submit_sm in OPEN", roleSMSC, 0, submit, nil, CmdSubmitSMResp, StatusInvBndSts, 2},
		{"enquire_link in OPEN", roleSMSC, 0, &EnquireLink{}, nil, CmdEnquireLinkResp, StatusInvBndSts, 2},
		{"unbind in OPEN", roleSMSC, 0, &Unbind{}, nil, CmdUnbindResp, StatusInvBndSts, 2},
		{
			"bind when bound", roleSMSC, CmdBindTransceiver, &Bind{ID: CmdBindTransmitter, SystemID: "esme"}, nil,
			CmdBindTransmitterResp, StatusAlyBnd, 2,
		},
		{"submit_sm on BOUND_RX", roleSMSC, CmdBindReceiver, submit, nil, CmdSubmitSMResp, StatusInvBndSts, 2},
		{"deliver_sm to an SMSC", roleSMSC, CmdBindTransceiver, deliver, nil, CmdGenericNack, StatusInvCmdID, 2},
		{"data_sm, unsupported", roleSMSC, CmdBindTransceiver, nil, header(CmdDataSM, 9, 0, 0), CmdGenericNack, StatusInvCmdID, 9},
		{"reserved command_id", roleSMSC, CmdBindTransceiver, nil, header(0x77, 3), CmdGenericNack, StatusInvCmdID, 3},
		{
			"undefined response command_id (§3.3)", roleSMSC, CmdBindTransceiver, nil, header(0x80000077, 6), CmdGenericNack,
			StatusInvCmdID, 6,
		},
		{"outbind to an SMSC", roleSMSC, 0, nil, header(CmdOutbind, 3, 0, 0), CmdGenericNack, StatusInvCmdID, 3},
		{
			"enquire_link with a body (§4.3)", roleSMSC, CmdBindTransceiver, nil, header(CmdEnquireLink, 5, 0), CmdGenericNack,
			StatusInvCmdLen, 5,
		},
		{
			"submit_sm with sm_length 255 (§5.2.21)", roleSMSC, CmdBindTransceiver, nil, smLength255(), CmdSubmitSMResp,
			StatusInvMsgLen, 4,
		},
		{
			"sequence_number 0 (§5.1.4)", roleSMSC, CmdBindTransceiver, nil, header(CmdEnquireLink, 0), CmdGenericNack,
			StatusUnknownErr, 0,
		},
		{"submit_sm to an ESME", roleESME, 0, submit, nil, CmdGenericNack, StatusInvCmdID, 2},
		{"bind to an ESME", roleESME, 0, &Bind{ID: CmdBindTransceiver}, nil, CmdGenericNack, StatusInvCmdID, 2},
		{"deliver_sm to an unbound ESME", roleESME, 0, deliver, nil, CmdDeliverSMResp, StatusInvBndSts, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				s *Session
				p *rawPeer
			)

			switch {
			case tc.role == roleESME:
				s, p = pipe(t, roleESME, SessionConfig{}, nil, hooks{})
			case tc.bind != 0:
				s, p = boundSMSC(t, tc.bind, SessionConfig{}, submitOK)
			default:
				s, p = pipe(t, roleSMSC, SessionConfig{}, submitOK, smscHooks())
			}

			if tc.raw != nil {
				p.sendRaw(tc.raw)
			} else {
				p.send(tc.send, StatusOK, 2)
			}

			p.expect(tc.id, tc.status, tc.seq)

			// The session stays up: it answers the next PDU.
			before := s.State()

			p.send(&EnquireLink{}, StatusOK, 10)

			want := StatusOK
			if !before.Bound() {
				want = StatusInvBndSts
			}

			p.expect(CmdEnquireLinkResp, want, 10)
		})
	}
}

// TestHeaderError checks that a command_length outside 16…MaxLength is answered generic_nack ESME_RINVCMDLEN, and the
// session closed, since the PDU boundaries are lost (§4.3).
func TestHeaderError(t *testing.T) {
	for _, length := range []uint32{15, MaxLength + 1} {
		s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, submitOK)

		b := binary.BigEndian.AppendUint32(nil, length)
		b = binary.BigEndian.AppendUint32(b, uint32(CmdSubmitSM))
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 6)

		p.sendRaw(b)
		p.expect(CmdGenericNack, StatusInvCmdLen, 6)
		p.expectClosed()

		if err := waitClosed(t, s); err == nil {
			t.Fatal("no close reason")
		}
	}
}

func TestEnquireLink(t *testing.T) {
	t.Run("answered", func(t *testing.T) {
		_, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, submitOK)
		p.send(&EnquireLink{}, StatusOK, 2)
		p.expect(CmdEnquireLinkResp, StatusOK, 2)
	})

	t.Run("sent when idle, and answered", func(t *testing.T) {
		cfg := SessionConfig{EnquireLinkInterval: 50 * time.Millisecond, ResponseTimeout: time.Second}
		s, p := boundSMSC(t, CmdBindTransceiver, cfg, submitOK)

		for range 3 {
			h, _ := p.read()
			if h.ID != CmdEnquireLink {
				t.Fatalf("got %s, want enquire_link", h.ID)
			}

			p.send(&EnquireLinkResp{}, StatusOK, h.Sequence)
		}

		if s.State() != StateBoundTRX {
			t.Fatalf("state %s", s.State())
		}
	})

	t.Run("sent when idle, unanswered", func(t *testing.T) {
		cfg := SessionConfig{EnquireLinkInterval: 50 * time.Millisecond, ResponseTimeout: 100 * time.Millisecond}
		s, p := boundSMSC(t, CmdBindTransceiver, cfg, submitOK)

		if h, _ := p.read(); h.ID != CmdEnquireLink {
			t.Fatalf("got %s, want enquire_link", h.ID)
		}

		if err := waitClosed(t, s); !errors.Is(err, ErrEnquireLinkTimeout) {
			t.Fatalf("closed for %v", err)
		}
	})

	t.Run("Session.EnquireLink", func(t *testing.T) {
		s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, submitOK)

		errc := make(chan error, 1)

		go func() { errc <- s.EnquireLink(context.Background()) }()

		h, _ := p.read()
		p.send(&EnquireLinkResp{}, StatusOK, h.Sequence)

		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	})
}

func TestTimers(t *testing.T) {
	t.Run("session_init_timer", func(t *testing.T) {
		s, p := pipe(t, roleSMSC, SessionConfig{SessionInitTimeout: 50 * time.Millisecond}, submitOK, smscHooks())

		if err := waitClosed(t, s); !errors.Is(err, ErrSessionInitTimeout) {
			t.Fatalf("closed for %v", err)
		}

		p.expectClosed()
	})

	t.Run("session_init_timer stops at bind", func(t *testing.T) {
		s, _ := boundSMSC(t, CmdBindTransceiver, SessionConfig{SessionInitTimeout: 50 * time.Millisecond}, submitOK)
		time.Sleep(150 * time.Millisecond)

		if s.State() != StateBoundTRX {
			t.Fatalf("state %s, err %v", s.State(), s.Err())
		}
	})

	t.Run("no session_init_timer on an ESME", func(t *testing.T) {
		s, _ := pipe(t, roleESME, SessionConfig{SessionInitTimeout: 20 * time.Millisecond}, nil, hooks{})
		time.Sleep(100 * time.Millisecond)

		if s.State() != StateOpen {
			t.Fatalf("state %s", s.State())
		}
	})

	t.Run("inactivity_timer", func(t *testing.T) {
		cfg := SessionConfig{InactivityTimeout: 100 * time.Millisecond, EnquireLinkInterval: -1}
		s, p := boundSMSC(t, CmdBindTransceiver, cfg, submitOK)

		// Traffic keeps the session up.
		for i := range 4 {
			time.Sleep(40 * time.Millisecond)
			p.send(&EnquireLink{}, StatusOK, uint32(i+2))
			p.expect(CmdEnquireLinkResp, StatusOK, uint32(i+2))
		}

		if err := waitClosed(t, s); !errors.Is(err, ErrInactivityTimeout) {
			t.Fatalf("closed for %v", err)
		}
	})
}

// blockingHandler holds each submit_sm until release is closed.
type blockingHandler struct {
	started chan uint32
	release chan struct{}
}

func (b *blockingHandler) ServeSMPP(_ context.Context, _ *Session, req Request) Response {
	b.started <- req.Header.Sequence

	<-b.release

	return Response{PDU: &SubmitSMResp{MessageID: "id"}}
}

func TestInboundWindow(t *testing.T) {
	h := &blockingHandler{started: make(chan uint32, 10), release: make(chan struct{})}
	_, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{InboundWindow: 2}, h)

	p.send(&SubmitSM{Message: message()}, StatusOK, 2)
	p.send(&SubmitSM{Message: message()}, StatusOK, 3)
	<-h.started
	<-h.started

	// The third is over the window: answered at once.
	p.send(&SubmitSM{Message: message()}, StatusOK, 4)
	p.expect(CmdSubmitSMResp, StatusThrottled, 4)

	// enquire_link is answered by the session, outside the window.
	p.send(&EnquireLink{}, StatusOK, 5)
	p.expect(CmdEnquireLinkResp, StatusOK, 5)

	close(h.release)

	seen := map[uint32]bool{}

	for range 2 {
		h, d := p.read()
		if h.ID != CmdSubmitSMResp || h.Status != StatusOK || d.(*SubmitSMResp).MessageID != "id" {
			t.Fatalf("got %+v %+v", h, d)
		}

		seen[h.Sequence] = true
	}

	if !seen[2] || !seen[3] {
		t.Fatalf("responses to %v", seen)
	}
}

func TestOutboundWindow(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{OutboundWindow: 1}, submitOK)

	errs := make(chan error, 2)

	for range 2 {
		go func() { errs <- s.DeliverSM(context.Background(), &DeliverSM{Message: message()}) }()
	}

	h1, _ := p.read()

	// The second waits for the window: it is not sent.
	time.Sleep(100 * time.Millisecond)

	s.mu.Lock()
	n := len(s.pending)
	s.mu.Unlock()

	if n != 1 {
		t.Fatalf("%d requests outstanding beyond a window of 1", n)
	}

	p.send(&DeliverSMResp{}, StatusOK, h1.Sequence)

	h2, _ := p.read()
	p.send(&DeliverSMResp{}, StatusOK, h2.Sequence)

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if h1.Sequence == h2.Sequence {
		t.Fatalf("both used sequence_number %d", h1.Sequence)
	}
}

// TestOutOfOrderResponses checks that responses are matched by sequence_number, in any order (§2.5.2).
func TestOutOfOrderResponses(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, submitOK)

	type res struct {
		n   int
		err error
	}

	results := make(chan res, 3)

	for i := range 3 {
		go func() {
			m := &DeliverSM{Message: message()}

			m.ShortMessage = []byte{byte(i)}
			results <- res{i, s.DeliverSM(context.Background(), m)}
		}()
	}

	statusOf := map[int]CommandStatus{0: StatusOK, 1: StatusXPAppn, 2: StatusXTAppn}
	seqs := map[int]uint32{}

	for range 3 {
		h, d := p.read()
		seqs[int(d.(*DeliverSM).ShortMessage[0])] = h.Sequence
	}

	p.send(&DeliverSMResp{}, statusOf[2], seqs[2])
	p.send(&GenericNack{}, statusOf[1], seqs[1])
	p.send(&DeliverSMResp{}, statusOf[0], seqs[0])

	for range 3 {
		r := <-results

		var se *StatusError

		switch {
		case r.n == 0 && r.err != nil:
			t.Fatalf("message 0: %v", r.err)
		case r.n == 1 && (!errors.As(r.err, &se) || se.Command != CmdGenericNack || se.Status != StatusXPAppn):
			t.Fatalf("message 1: %v", r.err)
		case r.n == 2 && (!errors.As(r.err, &se) || se.Command != CmdDeliverSMResp || se.Status != StatusXTAppn):
			t.Fatalf("message 2: %v", r.err)
		}
	}
}

func TestResponseTimeout(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{ResponseTimeout: 50 * time.Millisecond, EnquireLinkInterval: -1},
		submitOK)

	errc := make(chan error, 1)

	go func() { errc <- s.DeliverSM(context.Background(), &DeliverSM{Message: message()}) }()

	h, _ := p.read()

	if err := <-errc; !errors.Is(err, ErrResponseTimeout) {
		t.Fatalf("err = %v", err)
	}

	// A late response is discarded, and the session stays bound.
	p.send(&DeliverSMResp{}, StatusOK, h.Sequence)
	p.send(&EnquireLink{}, StatusOK, 20)
	p.expect(CmdEnquireLinkResp, StatusOK, 20)

	if s.State() != StateBoundTRX {
		t.Fatalf("state %s", s.State())
	}
}

func TestContextCancel(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, submitOK)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)

	go func() { errc <- s.DeliverSM(ctx, &DeliverSM{Message: message()}) }()

	p.read()
	cancel()

	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// TestSequenceWrap checks that sequence_number runs from 1 to 0x7FFFFFFF and back to 1, skipping a number still in
// use (§5.1.4).
func TestSequenceWrap(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, submitOK)

	s.mu.Lock()
	s.seq = MaxSequence - 1
	s.pending[1] = &pending{id: CmdDeliverSM, ch: make(chan result, 1)} // 1 is still outstanding
	s.mu.Unlock()

	for _, want := range []uint32{MaxSequence, 2, 3} {
		errc := make(chan error, 1)

		go func() { errc <- s.DeliverSM(context.Background(), &DeliverSM{Message: message()}) }()

		h, _ := p.read()
		if h.Sequence != want {
			t.Fatalf("sequence_number 0x%08x, want 0x%08x", h.Sequence, want)
		}

		p.send(&DeliverSMResp{}, StatusOK, h.Sequence)

		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPeerUnbind(t *testing.T) {
	h := &blockingHandler{started: make(chan uint32, 1), release: make(chan struct{})}
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, h)

	p.send(&SubmitSM{Message: message()}, StatusOK, 2)
	<-h.started
	p.send(&Unbind{}, StatusOK, 3)

	// The submit_sm in progress is answered first, then unbind_resp.
	time.Sleep(20 * time.Millisecond)
	close(h.release)
	p.expect(CmdSubmitSMResp, StatusOK, 2)
	p.expect(CmdUnbindResp, StatusOK, 3)

	_ = p.c.Close()

	if err := waitClosed(t, s); !errors.Is(err, ErrUnbound) {
		t.Fatalf("closed for %v", err)
	}
}

func TestUnbind(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, submitOK)

	errc := make(chan error, 1)

	go func() { errc <- s.Unbind(context.Background()) }()

	h, _ := p.read()
	if h.ID != CmdUnbind {
		t.Fatalf("got %s", h.ID)
	}

	// Nothing new goes out while unbinding.
	if err := s.DeliverSM(context.Background(), &DeliverSM{Message: message()}); err == nil {
		t.Fatal("deliver_sm sent while unbinding")
	}

	p.send(&UnbindResp{}, StatusOK, h.Sequence)

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	if err := waitClosed(t, s); !errors.Is(err, ErrUnbound) {
		t.Fatalf("closed for %v", err)
	}

	p.expectClosed()

	if err := s.DeliverSM(context.Background(), &DeliverSM{Message: message()}); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestUnbindUnanswered(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{ResponseTimeout: 50 * time.Millisecond}, submitOK)

	errc := make(chan error, 1)

	go func() { errc <- s.Unbind(context.Background()) }()

	p.read()

	if err := <-errc; !errors.Is(err, ErrResponseTimeout) {
		t.Fatalf("err = %v", err)
	}

	_ = waitClosed(t, s)
}

func TestHandlerPanic(t *testing.T) {
	h := HandlerFunc(func(context.Context, *Session, Request) Response { panic("boom") })
	_, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, h)

	p.send(&SubmitSM{Message: message()}, StatusOK, 2)
	p.expect(CmdSubmitSMResp, StatusSysErr, 2)
}

func TestHandlerWrongResponse(t *testing.T) {
	h := HandlerFunc(func(context.Context, *Session, Request) Response { return Response{PDU: &DeliverSMResp{}} })
	_, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, h)

	p.send(&SubmitSM{Message: message()}, StatusOK, 2)
	p.expect(CmdSubmitSMResp, StatusSysErr, 2)
}

// TestWritten checks that Written is called after the response is on the wire.
func TestWritten(t *testing.T) {
	written := make(chan error, 1)
	h := HandlerFunc(func(context.Context, *Session, Request) Response {
		return Response{PDU: &SubmitSMResp{MessageID: "42"}, Written: func(err error) { written <- err }}
	})
	_, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{}, h)

	p.send(&SubmitSM{Message: message()}, StatusOK, 2)

	select {
	case err := <-written:
		t.Fatalf("Written(%v) before the response was read", err)
	case <-time.After(20 * time.Millisecond):
	}

	p.expect(CmdSubmitSMResp, StatusOK, 2)

	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

// TestNoOptionalParametersToV33 checks that no optional parameter goes to an ESME of interface_version below 0x34
// (§3.4).
func TestNoOptionalParametersToV33(t *testing.T) {
	s, p := pipe(t, roleSMSC, SessionConfig{}, submitOK, smscHooks())
	p.send(&Bind{ID: CmdBindReceiver, SystemID: "old", InterfaceVersion: 0x33}, StatusOK, 1)
	p.expect(CmdBindReceiverResp, StatusOK, 1)
	waitState(t, s, StateBoundRX)

	m := &DeliverSM{Message: message()}
	m.Options.MessageState = new(StateDelivered)

	if err := s.DeliverSM(context.Background(), m); !errors.Is(err, ErrNoOptionalParameters) {
		t.Fatalf("err = %v", err)
	}
}

func TestESMEBind(t *testing.T) {
	var delivered []*DeliverSM

	var mu sync.Mutex

	h := HandlerFunc(func(_ context.Context, _ *Session, req Request) Response {
		mu.Lock()

		delivered = append(delivered, req.PDU.(*DeliverSM))
		mu.Unlock()

		return Response{}
	})
	s, p := pipe(t, roleESME, SessionConfig{}, h, hooks{})

	type bindRes struct {
		r   *BindResp
		err error
	}

	res := make(chan bindRes, 1)

	go func() {
		r, err := s.bind(context.Background(), &Bind{ID: CmdBindTransceiver, SystemID: "esme", InterfaceVersion: 0x34})
		res <- bindRes{r, err}
	}()

	h1, d := p.read()
	if b := d.(*Bind); b.SystemID != "esme" || h1.ID != CmdBindTransceiver {
		t.Fatalf("bind %+v", b)
	}

	// The SMSC's deliver_sm right after its bind response is handled in BOUND_TRX.
	p.send(&BindResp{ID: CmdBindTransceiverResp, SystemID: "SMSC", Options: Options{SCInterfaceVersion: new(byte(0x34))}},
		StatusOK, h1.Sequence)
	p.send(&DeliverSM{Message: message()}, StatusOK, 1)
	p.expect(CmdDeliverSMResp, StatusOK, 1)

	if r := <-res; r.err != nil || r.r.SystemID != "SMSC" {
		t.Fatalf("bind = %+v, %v", r.r, r.err)
	}

	if s.State() != StateBoundTRX || s.SystemID() != "SMSC" || len(delivered) != 1 {
		t.Fatalf("state %s, system_id %q, %d delivered", s.State(), s.SystemID(), len(delivered))
	}

	// submit_sm goes out and its message_id comes back.
	idc := make(chan string, 1)

	go func() {
		id, _ := s.SubmitSM(context.Background(), &SubmitSM{Message: message()})
		idc <- id
	}()

	h2, _ := p.read()
	p.send(&SubmitSMResp{MessageID: "77"}, StatusOK, h2.Sequence)

	if id := <-idc; id != "77" {
		t.Fatalf("message_id %q", id)
	}

	// An ESME cannot send deliver_sm.
	var se *StateError
	if err := s.DeliverSM(context.Background(), &DeliverSM{Message: message()}); !errors.As(err, &se) {
		t.Fatalf("deliver_sm from an ESME: %v", err)
	}
}

// TestESMEBindRefused checks that a refused bind leaves the session OPEN, with the status.
func TestESMEBindRefused(t *testing.T) {
	s, p := pipe(t, roleESME, SessionConfig{}, nil, hooks{})
	errc := make(chan error, 1)

	go func() {
		_, err := s.bind(context.Background(), &Bind{ID: CmdBindTransmitter, SystemID: "esme"})
		errc <- err
	}()

	h, _ := p.read()
	p.send(&BindResp{ID: CmdBindTransmitterResp}, StatusInvPaswd, h.Sequence)

	var se *StatusError
	if err := <-errc; !errors.As(err, &se) || se.Status != StatusInvPaswd {
		t.Fatalf("err = %v", err)
	}

	if s.State() != StateOpen {
		t.Fatalf("state %s", s.State())
	}

	// No sc_interface_version: no optional parameters toward this SMSC (§3.4). Bound as a transmitter now.
	go func() {
		_, err := s.bind(context.Background(), &Bind{ID: CmdBindTransmitter, SystemID: "esme"})
		errc <- err
	}()

	h, _ = p.read()
	p.send(&BindResp{ID: CmdBindTransmitterResp, SystemID: "old"}, StatusOK, h.Sequence)

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	m := &SubmitSM{Message: message()}
	m.Options.UserMessageReference = new(uint16(1))

	if _, err := s.SubmitSM(context.Background(), m); !errors.Is(err, ErrNoOptionalParameters) {
		t.Fatalf("err = %v", err)
	}
}

// FuzzSession feeds arbitrary octets to an SMSC session: it must not panic, and must answer or close.
func FuzzSession(f *testing.F) {
	bind, _ := Encode(&Bind{ID: CmdBindTransceiver, SystemID: "esme", InterfaceVersion: 0x34}, StatusOK, 1)
	submit, _ := Encode(&SubmitSM{Message: message()}, StatusOK, 2)

	f.Add(append(append([]byte{}, bind...), submit...))
	f.Add(submit)
	f.Add([]byte{0, 0, 0, 16, 0, 0, 0, 0x15, 0, 0, 0, 0, 0, 0, 0, 1})
	f.Add([]byte{0, 0, 0, 1})

	f.Fuzz(func(t *testing.T, in []byte) {
		a, b := net.Pipe()
		s := newSession(a, roleSMSC, SessionConfig{ResponseTimeout: 100 * time.Millisecond}, submitOK, smscHooks())
		s.start()

		go func() {
			_, _ = b.Write(in)
			_ = b.Close()
		}()

		go func() {
			buf := make([]byte, 1024)
			for {
				if _, err := b.Read(buf); err != nil {
					return
				}
			}
		}()

		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the session did not close")
		}
	})
}

// TestAlertNotificationUnanswered checks that alert_notification gets no response (§2.8), not generic_nack.
func TestAlertNotificationUnanswered(t *testing.T) {
	_, p := pipe(t, roleESME, SessionConfig{}, nil, hooks{})

	alert := binary.BigEndian.AppendUint32(nil, HeaderLength+6)
	alert = binary.BigEndian.AppendUint32(alert, uint32(CmdAlertNotification))
	alert = binary.BigEndian.AppendUint32(alert, 0)
	alert = binary.BigEndian.AppendUint32(alert, 7)
	alert = append(alert, 0, 0, 0, 0, 0, 0)

	p.sendRaw(alert)
	p.send(&EnquireLink{}, StatusOK, 8)

	// The next PDU answers the enquire_link: nothing answered the alert_notification.
	p.expect(CmdEnquireLinkResp, StatusInvBndSts, 8)
}

// TestSendRefusesBindAndUnbind checks that Send does not send bind or unbind, which would change the state behind
// the session's back: an unbind_resp would leave the session CLOSED with its connection open.
func TestSendRefusesBindAndUnbind(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{EnquireLinkInterval: -1}, submitOK)

	var se *StateError
	if _, _, err := s.Send(context.Background(), &Unbind{}); !errors.As(err, &se) || se.Command != CmdUnbind {
		t.Fatalf("Send(unbind) = %v", err)
	}

	esme, _ := pipe(t, roleESME, SessionConfig{}, nil, hooks{})
	if _, _, err := esme.Send(context.Background(), &Bind{ID: CmdBindTransceiver, SystemID: "e"}); !errors.As(err, &se) ||
		se.Command != CmdBindTransceiver {
		t.Fatalf("Send(bind) = %v", err)
	}

	// Nothing was sent: the next PDU is the enquire_link's response, and the session is still bound.
	go func() { _ = s.EnquireLink(context.Background()) }()

	h, _ := p.read()
	if h.ID != CmdEnquireLink {
		t.Fatalf("got %s", h.ID)
	}

	p.send(&EnquireLinkResp{}, StatusOK, h.Sequence)

	if st := s.State(); st != StateBoundTRX {
		t.Fatalf("state %s", st)
	}
}

// TestNoRequestAfterUnbind checks that a deliver_sm waiting for the outbound window is not sent after unbind
// (Table 2-1, §4.2).
func TestNoRequestAfterUnbind(t *testing.T) {
	s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{OutboundWindow: 1, EnquireLinkInterval: -1}, submitOK)

	first := make(chan error, 1)

	go func() { first <- s.DeliverSM(context.Background(), &DeliverSM{Message: message()}) }()

	h1, _ := p.read()
	if h1.ID != CmdDeliverSM {
		t.Fatalf("got %s", h1.ID)
	}

	second := make(chan error, 1)

	go func() { second <- s.DeliverSM(context.Background(), &DeliverSM{Message: message()}) }()

	time.Sleep(50 * time.Millisecond) // the second waits for the window

	unbound := make(chan error, 1)

	go func() { unbound <- s.Unbind(context.Background()) }()

	h2, _ := p.read()
	if h2.ID != CmdUnbind {
		t.Fatalf("got %s, want unbind", h2.ID)
	}

	// The window opens after unbind: the second deliver_sm is refused, not sent.
	p.send(&DeliverSMResp{}, StatusOK, h1.Sequence)

	if err := <-first; err != nil {
		t.Fatal(err)
	}

	var se *StateError
	if err := <-second; !errors.As(err, &se) {
		t.Fatalf("second deliver_sm: %v", err)
	}

	p.send(&UnbindResp{}, StatusOK, h2.Sequence)

	if err := <-unbound; err != nil {
		t.Fatal(err)
	}

	_ = p.c.SetReadDeadline(time.Now().Add(2 * time.Second))

	for {
		h, _, err := ReadPDU(p.r)
		if err != nil {
			break
		}

		t.Errorf("%s sent after unbind", h.ID)
	}
}

// TestUnbindRacingSubmit runs a local unbind against a submit_sm arriving at the same time: the submit_sm gets one
// response (§2.8), and a submit_sm accepted for handling is answered before the unbind.
func TestUnbindRacingSubmit(t *testing.T) {
	h := HandlerFunc(func(context.Context, *Session, Request) Response {
		time.Sleep(time.Millisecond)

		return Response{PDU: &SubmitSMResp{MessageID: "m1"}}
	})

	for i := range 50 {
		s, p := boundSMSC(t, CmdBindTransceiver, SessionConfig{EnquireLinkInterval: -1}, h)

		submit, err := Encode(&SubmitSM{Message: message()}, StatusOK, 2)
		if err != nil {
			t.Fatal(err)
		}

		written := make(chan error, 1)

		go func() {
			_, err := p.c.Write(submit)
			written <- err
		}()

		unbound := make(chan error, 1)

		go func() { unbound <- s.Unbind(context.Background()) }()

		var (
			unbind      bool
			submitResps int
		)

		_ = p.c.SetReadDeadline(time.Now().Add(2 * time.Second))

		for {
			hd, _, err := ReadPDU(p.r)
			if err != nil {
				break
			}

			switch hd.ID {
			case CmdSubmitSMResp:
				submitResps++

				if hd.Status == StatusOK && unbind {
					t.Fatalf("run %d: submit_sm_resp ESME_ROK after unbind", i)
				}
			case CmdUnbind:
				unbind = true

				// net.Pipe has no buffer: answer on another goroutine, after the submit_sm, while this one reads.
				go func() {
					if err := <-written; err != nil {
						return
					}

					resp, _ := Encode(&UnbindResp{}, StatusOK, hd.Sequence)
					_, _ = p.c.Write(resp)
				}()
			}
		}

		if err := <-unbound; err != nil {
			t.Fatalf("run %d: Unbind = %v", i, err)
		}

		if submitResps != 1 {
			t.Fatalf("run %d: %d responses to the submit_sm", i, submitResps)
		}
	}
}
