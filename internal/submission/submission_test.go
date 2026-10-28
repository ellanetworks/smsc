package submission

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/settings"
)

type fakeStore struct {
	messages []db.NewMessage
	err      error
}

func (s *fakeStore) CreateMessages(_ context.Context, messages []db.NewMessage) ([]int64, error) {
	if s.err != nil {
		return nil, s.err
	}

	ids := make([]int64, len(messages))

	for i, m := range messages {
		s.messages = append(s.messages, m)
		ids[i] = int64(len(s.messages))
	}

	return ids, nil
}

var now = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func newSubmitter(store Store) *Submitter {
	return &Submitter{
		Store: store,
		Settings: func() settings.Settings {
			return settings.Settings{Delivery: settings.Delivery{DefaultValidity: time.Hour}}
		},
		Received: intake.New(),
		Now:      func() time.Time { return now },
	}
}

func request(d UserData) Request {
	return Request{
		Originator: db.Address{Digits: "15550001000", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:  db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:     "15551230002",
		Origin:     db.OriginSMPP,
		Data:       &d,
	}
}

func TestSubmitUserData(t *testing.T) {
	tests := []struct {
		name string
		data UserData
		want string
		err  error
	}{
		{
			name: "8-bit", data: UserData{PID: 0x7f, DCS: 0xf6, Header: []byte{2, 0x70, 0}, Octets: []byte{0xaa}},
			want: "41000b915155210300f27ff604027000aa",
		},
		{
			name: "septets after a header", data: UserData{DCS: 0x00, Header: []byte{5, 0, 3, 1, 2, 1}, Septets: []byte{0x68, 0x69}},
			want: "41000b915155210300f2000009050003010201d069",
		},
		{name: "telematic", data: UserData{PID: 0x21, DCS: 0x04}, err: ErrTelematicInterworking},
		{name: "not class 2", data: UserData{PID: 0x7c, DCS: 0x04}, err: ErrNotClass2},
		{name: "malformed header", data: UserData{DCS: 0x04, Header: []byte{3, 0}}, err: ErrUserDataHeader},
		{name: "empty header", data: UserData{DCS: 0x04, Header: []byte{}}, err: ErrUserDataHeader},
		{name: "device trigger", data: UserData{PID: 0x48, DCS: 0x04}, err: ErrNoApplicationPort},
		{name: "141 octets", data: UserData{DCS: 0x04, Octets: make([]byte, 141)}, err: ErrUserDataTooLong},
		{name: "140 octets", data: UserData{DCS: 0x04, Octets: make([]byte, 140)}},
		{
			name: "154 septets after a 6-octet header", data: UserData{Header: []byte{5, 0, 3, 1, 2, 1}, Septets: make([]byte, 154)},
			err: ErrUserDataTooLong,
		},
		{name: "153 septets after a 6-octet header", data: UserData{Header: []byte{5, 0, 3, 1, 2, 1}, Septets: make([]byte, 153)}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}

			ids, err := newSubmitter(store).Submit(context.Background(), request(tc.data))
			if !errors.Is(err, tc.err) {
				t.Fatalf("Submit = %v, want %v", err, tc.err)
			}

			if tc.err != nil {
				if len(store.messages) != 0 {
					t.Fatal("stored a refused message")
				}

				return
			}

			m := store.messages[0]
			if len(ids) != 1 || m.Origin != db.OriginSMPP || !m.ExpiresAt.Equal(now.Add(time.Hour)) {
				t.Fatalf("stored %+v", m)
			}

			if got := hex.EncodeToString(m.TPDU); tc.want != "" && got != tc.want {
				t.Fatalf("SMS-SUBMIT = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSubmitText(t *testing.T) {
	store := &fakeStore{}
	text := "hi"

	r := request(UserData{})
	r.Data, r.Text, r.Origin = nil, &text, db.OriginAPI

	if _, err := newSubmitter(store).Submit(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	if got := hex.EncodeToString(store.messages[0].TPDU); got != "01000b915155210300f2000002e834" {
		t.Fatalf("SMS-SUBMIT = %s", got)
	}
}

func TestSubmitReplaceType(t *testing.T) {
	store := &fakeStore{}

	if _, err := newSubmitter(store).Submit(context.Background(), request(UserData{PID: 0x41, DCS: 0x04})); err != nil {
		t.Fatal(err)
	}

	if m := store.messages[0]; !m.Replace || m.ReportReplaced == nil {
		t.Fatalf("a replace type message is stored with Replace %t", m.Replace)
	}
}

func TestSubmitStoreFailure(t *testing.T) {
	_, err := newSubmitter(&fakeStore{err: errors.New("disk full")}).Submit(context.Background(), request(UserData{DCS: 0x04}))
	if !errors.Is(err, ErrStore) {
		t.Fatalf("Submit = %v, want ErrStore", err)
	}
}

func TestIsOriginatorName(t *testing.T) {
	for s, want := range map[string]bool{
		"Ella": true, "Ella SMSC": true, "12345": false, "": false, " Ella": false, "Ella  SMSC": false,
		"Ella Network": false, "Ellä": false,
	} {
		if got := IsOriginatorName(s); got != want {
			t.Errorf("IsOriginatorName(%q) = %t, want %t", s, got, want)
		}
	}
}
