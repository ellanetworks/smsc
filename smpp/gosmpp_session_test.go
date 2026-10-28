package smpp

// Interoperability with github.com/linxGnu/gosmpp (Apache-2.0), a test-only dependency: a gosmpp ESME session bound to
// our Server, and our Client bound to a scripted SMSC that frames its PDUs with gosmpp's codec. gosmpp has no SMSC
// session, so the second proves codec interop and our Client's session behaviour, not gosmpp's.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/data"
	"github.com/linxGnu/gosmpp/pdu"
)

// TestGosmppESME binds a gosmpp transceiver to our server: submit_sm, deliver_sm with optional parameters, the
// server's enquire_link, and gosmpp's unbind.
func TestGosmppESME(t *testing.T) {
	// An enquire_link every 50 ms, closing the session if one is unanswered within 300 ms.
	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Session.EnquireLinkInterval = 50 * time.Millisecond
		cfg.Session.ResponseTimeout = 300 * time.Millisecond
	})

	received := make(chan pdu.PDU, 10)

	s, err := gosmpp.NewSession(
		gosmpp.TRXConnector(gosmpp.NonTLSDialer, gosmpp.Auth{SMSC: ts.addr, SystemID: "simota", Password: "secret"}),
		gosmpp.Settings{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			OnPDU:        func(p pdu.PDU, _ bool) { received <- p },
		}, -1)
	if err != nil {
		t.Fatal(err)
	}

	sess := <-ts.bound
	if sess.State() != StateBoundTRX || sess.SystemID() != "simota" {
		t.Fatalf("session %+v", sess.Info())
	}

	g := pdu.NewSubmitSM().(*pdu.SubmitSM)
	g.SourceAddr = gosmppAddress(t, "15550001000")
	g.DestAddr = gosmppAddress(t, "15551230002")
	g.EsmClass, g.ProtocolID, g.RegisteredDelivery = 0x40, 0x7f, 0x09

	msg, err := pdu.NewBinaryShortMessageWithEncoding(otaCommand, data.BINARY8BIT1)
	if err != nil {
		t.Fatal(err)
	}

	msg.SetUDH(pdu.UDH{{ID: 0x70, Data: []byte{}}})
	g.Message = msg

	if err := s.Transceiver().Submit(g); err != nil {
		t.Fatal(err)
	}

	m := <-ts.submitted
	if m.ESMClass != 0x40 || m.ProtocolID != 0x7f || m.RegisteredDelivery != 0x09 || m.DataCoding != 0x02 ||
		!bytes.Equal(m.ShortMessage, append([]byte{2, 0x70, 0}, otaCommand...)) {
		t.Fatalf("submitted %+v", m)
	}

	resp := next[*pdu.SubmitSMResp](t, received)
	if resp.CommandStatus != data.ESME_ROK || resp.MessageID != "1" {
		t.Fatalf("submit_sm_resp %+v", resp)
	}

	// The server's enquire_links, answered by gosmpp, keep the session bound past the response_timer: an unanswered
	// one would close it with ErrEnquireLinkTimeout.
	time.Sleep(time.Second)

	if sess.State() != StateBoundTRX {
		t.Fatalf("state %s, err %v", sess.State(), sess.Err())
	}

	answer := &DeliverSM{Message: message()}
	answer.ESMClass, answer.ProtocolID, answer.DataCoding = 0x50, 0x7f, 0xf6
	answer.ShortMessage = []byte{2, 0x71, 0, 0, 0x0b, 0x0a, 0xb0, 0, 1, 0, 0, 0, 0, 0, 0, 0}
	answer.Options.ReceiptedMessageID = new("1")
	answer.Options.UserMessageReference = new(uint16(7))

	if _, err := ts.Deliver(context.Background(), "simota", answer); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	d := next[*pdu.DeliverSM](t, received)

	ud, _ := d.Message.GetMessageData()
	udh, _ := d.Message.UDH().MarshalBinary()

	if d.EsmClass != 0x50 || d.ProtocolID != 0x7f || !bytes.Equal(append(udh, ud...), answer.ShortMessage) ||
		!bytes.Equal(d.OptionalParameters[pdu.TagReceiptedMessageID].Data, []byte("1\x00")) {
		t.Fatalf("gosmpp got %+v", d)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the session did not close after gosmpp's unbind")
	}

	if !errors.Is(sess.Err(), ErrUnbound) {
		t.Fatalf("session closed for %v, want ErrUnbound", sess.Err())
	}
}

// next returns the next PDU of type T from ch, skipping others.
func next[T pdu.PDU](t *testing.T, ch chan pdu.PDU) T {
	t.Helper()

	timeout := time.After(3 * time.Second)

	for {
		select {
		case p := <-ch:
			if v, ok := p.(T); ok {
				return v
			}
		case <-timeout:
			var zero T

			t.Fatalf("no %T received", zero)

			return zero
		}
	}
}

// gosmppSMSC is a minimal SMSC built from gosmpp's codec: it accepts one connection, binds it, answers submit_sm,
// enquire_link and unbind, and sends a deliver_sm after the bind.
type gosmppSMSC struct {
	addr     string
	got      chan pdu.PDU
	deliver  *pdu.DeliverSM
	finished chan error
}

func newGosmppSMSC(t *testing.T, deliver *pdu.DeliverSM) *gosmppSMSC {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	g := &gosmppSMSC{addr: l.Addr().String(), got: make(chan pdu.PDU, 20), deliver: deliver, finished: make(chan error, 1)}

	go func() {
		defer func() { _ = l.Close() }()

		c, err := l.Accept()
		if err != nil {
			g.finished <- err

			return
		}

		defer func() { _ = c.Close() }()

		g.finished <- g.serve(c)
	}()

	t.Cleanup(func() { _ = l.Close() })

	return g
}

func (g *gosmppSMSC) serve(c net.Conn) error {
	write := func(p pdu.PDU) error {
		b := pdu.NewBuffer(nil)
		p.Marshal(b)
		_, err := c.Write(b.Bytes())

		return err
	}

	for {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))

		p, err := pdu.Parse(c)
		if err != nil {
			return err
		}

		g.got <- p

		switch p := p.(type) {
		case *pdu.BindRequest:
			r := p.GetResponse().(*pdu.BindResp)
			r.SystemID = "gosmpp"
			r.RegisterOptionalParam(pdu.Field{Tag: 0x0210, Data: []byte{0x34}})

			if err := write(r); err != nil {
				return err
			}

			if err := write(g.deliver); err != nil {
				return err
			}
		case *pdu.SubmitSM:
			r := p.GetResponse().(*pdu.SubmitSMResp)
			r.MessageID = "abc123"

			if err := write(r); err != nil {
				return err
			}
		case *pdu.EnquireLink, *pdu.Unbind:
			if err := write(p.GetResponse()); err != nil {
				return err
			}

			if _, ok := p.(*pdu.Unbind); ok {
				return nil
			}
		}
	}
}

// TestClientToGosmppSMSC binds our client to the scripted SMSC: submit_sm with a user data header and an optional
// parameter, deliver_sm answered, unbind. It checks that each PDU decodes on the other side, and that Close ends the
// session with unbind and its unbind_resp.
func TestClientToGosmppSMSC(t *testing.T) {
	d := pdu.NewDeliverSM().(*pdu.DeliverSM)
	d.SourceAddr = gosmppAddress(t, "15551230002")
	d.DestAddr = gosmppAddress(t, "15550001000")
	d.EsmClass = 0x04

	receipt, err := pdu.NewShortMessageWithEncoding("id:abc123 stat:DELIVRD", data.ASCII)
	if err != nil {
		t.Fatal(err)
	}

	d.Message = receipt
	d.RegisterOptionalParam(pdu.Field{Tag: pdu.TagReceiptedMessageID, Data: []byte("abc123\x00")})
	d.RegisterOptionalParam(pdu.Field{Tag: pdu.TagMessageStateOption, Data: []byte{2}})

	g := newGosmppSMSC(t, d)

	delivered := make(chan *DeliverSM, 1)
	c := newTestClient(t, g.addr, func(cfg *ClientConfig) {
		cfg.Handler = HandlerFunc(func(_ context.Context, _ *Session, req Request) Response {
			delivered <- req.PDU.(*DeliverSM)

			return Response{}
		})
	})
	waitBound(t, c)

	if st := c.State(); st.SystemID != "gosmpp" {
		t.Fatalf("state %+v", st)
	}

	b, ok := (<-g.got).(*pdu.BindRequest)
	if !ok || b.BindingType != pdu.Transceiver || b.SystemID != "simota" || b.Password != "secret" || b.InterfaceVersion != 0x34 {
		t.Fatalf("bind %+v", b)
	}

	got := <-delivered
	if got.ESMClass != 0x04 || got.Options.ReceiptedMessageID == nil || *got.Options.ReceiptedMessageID != "abc123" ||
		got.Options.MessageState == nil || *got.Options.MessageState != StateDelivered {
		t.Fatalf("delivered %+v", got)
	}

	if r := next[*pdu.DeliverSMResp](t, g.got); r.CommandStatus != data.ESME_ROK || r.SequenceNumber != d.SequenceNumber {
		t.Fatalf("deliver_sm_resp %+v", r)
	}

	m := &SubmitSM{Message: message()}
	m.ESMClass, m.ProtocolID, m.DataCoding, m.RegisteredDelivery = 0x40, 0x7f, 0xf6, 0x09
	m.ValidityPeriod = "000001000000000R"

	m.ShortMessage = append([]byte{2, 0x70, 0}, otaCommand...)
	m.Options.UserMessageReference = new(uint16(9))

	id, err := c.SubmitSM(context.Background(), m)
	if err != nil || id != "abc123" {
		t.Fatalf("SubmitSM = %q, %v", id, err)
	}

	s := next[*pdu.SubmitSM](t, g.got)

	ud, _ := s.Message.GetMessageData()
	udh, _ := s.Message.UDH().MarshalBinary()

	// gosmpp maps data_coding to its encodings and has none for 0xF6 (GSM message class control, §5.2.19), so it
	// keeps no value for it.
	if s.EsmClass != 0x40 || s.ProtocolID != 0x7f || s.RegisteredDelivery != 0x09 || s.ValidityPeriod != "000001000000000R" ||
		s.Message.Encoding() != nil || !bytes.Equal(append(udh, ud...), m.ShortMessage) ||
		!bytes.Equal(s.OptionalParameters[pdu.TagUserMessageReference].Data, []byte{0, 9}) {
		t.Fatalf("gosmpp got %+v, UDH %x, UD %x", s, udh, ud)
	}

	sess := c.Session()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if !errors.Is(sess.Err(), ErrUnbound) {
		t.Fatalf("client session closed for %v, want ErrUnbound", sess.Err())
	}

	if err := <-g.finished; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("gosmpp SMSC: %v", err)
	}

	next[*pdu.Unbind](t, g.got)
}
