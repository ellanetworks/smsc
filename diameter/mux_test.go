package diameter

import (
	"context"
	"testing"
)

func TestMuxDispatchesOnApplicationAndCommand(t *testing.T) {
	var called bool

	m := NewMux()
	m.Handle(16777313, 8388645, HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
		called = true
		return c.Answer(req, ResultSuccess)
	}))

	c := newConn(&Node{Identity: Identity{OriginHost: "h", OriginRealm: "r"}}, nil, false)

	ans := m.ServeDiameter(context.Background(), c, &Message{CommandCode: 8388645, ApplicationID: 16777313})
	if !called || resultValue(ans.AVPs[len(ans.AVPs)-1]) != ResultSuccess {
		t.Fatalf("registered pair: called=%v answer=%+v", called, ans)
	}

	called = false

	ans = m.ServeDiameter(context.Background(), c, &Message{CommandCode: 8388645, ApplicationID: 16777312})
	if called || ans.Flags&FlagError == 0 {
		t.Fatalf("same command under another application must not reach the handler: %+v", ans)
	}

	rc, _ := ans.Find(AVPResultCode, 0)
	if resultValue(rc) != ResultCommandUnsupported {
		t.Fatalf("Result-Code = %d", resultValue(rc))
	}
}
