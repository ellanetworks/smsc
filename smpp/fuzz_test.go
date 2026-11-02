package smpp

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func framed(in []byte) bool {
	for len(in) > 0 {
		h, err := DecodeHeader(in)
		if err != nil || uint32(len(in)) < h.Length {
			return false
		}

		in = in[h.Length:]
	}

	return true
}

func feed(c net.Conn, in []byte) {
	defer func() { _ = c.Close() }()

	finished := make(chan struct{})

	go func() {
		defer close(finished)

		for {
			h, _, err := ReadPDU(c)
			if err != nil || h.ID == CmdUnbindResp || h.ID.IsResponse() && h.Sequence == MaxSequence {
				return
			}
		}
	}()

	if _, err := c.Write(in); err != nil || !framed(in) {
		return
	}

	sentinel, _ := Encode(&EnquireLink{}, StatusOK, MaxSequence)
	if _, err := c.Write(sentinel); err != nil {
		return
	}

	select {
	case <-finished:
	case <-time.After(time.Second):
	}
}

func FuzzDecode(f *testing.F) {
	for _, v := range pduVectors {
		b, err := Encode(v.pdu, v.status, v.seq)
		if err != nil {
			f.Fatal(err)
		}

		f.Add(b)
	}

	f.Add([]byte{0, 0, 0, 16, 0, 0, 0, 0x15, 0, 0, 0, 0, 0, 0, 0, 1})
	f.Add([]byte{0, 0, 0, 1})

	f.Fuzz(func(t *testing.T, b []byte) {
		h, p, err := Decode(b)
		if err != nil || h.Status != StatusOK {
			return
		}

		out, err := Encode(p, h.Status, h.Sequence)
		if err != nil {
			return
		}

		h2, p2, err := Decode(out)
		if err != nil {
			t.Fatalf("Decode(%x), re-encoded from %x: %v", out, b, err)
		}

		if h2.ID != h.ID || h2.Status != h.Status || h2.Sequence != h.Sequence {
			t.Fatalf("header of %x round trips to %+v, want %+v", b, h2, h)
		}

		if !reflect.DeepEqual(p2, p) {
			t.Fatalf("round trip of %x:\n got %#v\nwant %#v", b, p2, p)
		}
	})
}

// SMPP 3.4 section 7.1.1
func FuzzParseTime(f *testing.F) {
	for _, s := range []string{
		"261102120000000+",
		"261102120000348-",
		"991231235959948+",
		"000001000000000R",
		"990000000000000R",
		"260230120000000+",
	} {
		f.Add(s)
	}

	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, s string) {
		v, err := ParseTime(s, now)
		if err != nil {
			return
		}

		_ = v.At(now)

		var out string

		if v.Relative {
			out, err = FormatRelative(v.Period)
		} else {
			out, err = FormatAbsolute(v.Absolute)
		}

		if err != nil {
			t.Fatalf("format %q: %v", s, err)
		}

		again, err := ParseTime(out, now)
		if err != nil {
			t.Fatalf("ParseTime(%q), formatted from %q: %v", out, s, err)
		}

		if again.Relative != v.Relative || again.Period != v.Period || !again.Absolute.Equal(v.Absolute) {
			t.Fatalf("%q round trips through %q to %+v, want %+v", s, out, again, v)
		}
	})
}

func FuzzSessionESME(f *testing.F) {
	bindResp, _ := Encode(&BindResp{ID: CmdBindTransceiverResp, SystemID: "SMSC"}, StatusOK, 1)
	deliver, _ := Encode(&DeliverSM{Message: message()}, StatusOK, 1)
	enquire, _ := Encode(&EnquireLink{}, StatusOK, 2)
	unbind, _ := Encode(&Unbind{}, StatusOK, 3)
	refused, _ := Encode(&BindResp{ID: CmdBindTransceiverResp}, StatusBindFail, 1)

	f.Add(append(append(append(append([]byte{}, bindResp...), deliver...), enquire...), unbind...))
	f.Add(bindResp)
	f.Add(refused)
	f.Add(deliver)
	f.Add([]byte{0, 0, 0, 16, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})

	delivered := HandlerFunc(func(context.Context, *Session, Request) Response { return Response{} })

	f.Fuzz(func(t *testing.T, in []byte) {
		a, b := net.Pipe()
		s := newSession(a, roleESME, SessionConfig{ResponseTimeout: 100 * time.Millisecond}, delivered, hooks{})
		s.start()

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			_, _ = s.bind(ctx, &Bind{ID: CmdBindTransceiver, SystemID: "esme", InterfaceVersion: InterfaceVersion})
		}()

		go func() {
			if _, _, err := ReadPDU(b); err != nil {
				_ = b.Close()
				return
			}

			feed(b, in)
		}()

		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the session did not close")
		}
	})
}
