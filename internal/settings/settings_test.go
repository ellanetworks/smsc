package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/numbering"
)

type fakeStore struct {
	saved []any
	err   error
}

func (f *fakeStore) save(v any) error {
	if f.err != nil {
		return f.err
	}

	f.saved = append(f.saved, v)

	return nil
}

func (f *fakeStore) UpdateOperator(_ context.Context, o Operator) error { return f.save(o) }

func (f *fakeStore) UpdateDelivery(_ context.Context, d Delivery) error { return f.save(d) }

func validOperator() Operator {
	return Operator{MCC: "001", MNC: "01", ServiceCentreAddress: "15550000000", Numbering: numbering.Plan{CountryCode: "1"}}
}

func validSettings() Settings {
	return Settings{
		Operator: validOperator(),
		Delivery: Delivery{DefaultValidity: time.Hour, RetryIntervals: []time.Duration{time.Minute}},
	}
}

func TestValidate(t *testing.T) {
	tests := map[string]func(*Settings){
		"zero validity":        func(s *Settings) { s.Delivery.DefaultValidity = 0 },
		"zero retry interval":  func(s *Settings) { s.Delivery.RetryIntervals = []time.Duration{0} },
		"short mcc":            func(s *Settings) { s.Operator.MCC = "01" },
		"non-digit mcc":        func(s *Settings) { s.Operator.MCC = "0a1" },
		"short mnc":            func(s *Settings) { s.Operator.MNC = "1" },
		"long mnc":             func(s *Settings) { s.Operator.MNC = "0001" },
		"missing country code": func(s *Settings) { s.Operator.Numbering.CountryCode = "" },
		"long sc address":      func(s *Settings) { s.Operator.ServiceCentreAddress = "1234567890123456" },
	}

	if err := validSettings().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			s := validSettings()
			edit(&s)

			if err := s.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestRealm(t *testing.T) {
	tests := []struct{ mcc, mnc, want string }{
		{"001", "01", "epc.mnc001.mcc001.3gppnetwork.org"},
		{"234", "15", "epc.mnc015.mcc234.3gppnetwork.org"},
		{"310", "410", "epc.mnc410.mcc310.3gppnetwork.org"},
	}

	for _, tt := range tests {
		if got := (Operator{MCC: tt.mcc, MNC: tt.mnc}).Realm(); got != tt.want {
			t.Errorf("Realm(%s, %s) = %q, want %q", tt.mcc, tt.mnc, got, tt.want)
		}
	}
}

func TestHost(t *testing.T) {
	if got := (Operator{MCC: "234", MNC: "15"}).Host(); got != "smsc.node.epc.mnc015.mcc234.3gppnetwork.org" {
		t.Fatalf("Host = %q", got)
	}
}

func TestLiveUpdatesOneArea(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings())
	changed := live.Changed()

	operator := validOperator()
	operator.MCC, operator.MNC = "208", "10"

	if err := live.UpdateOperator(context.Background(), operator); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	got := live.Get()
	if got.Operator != operator || got.Delivery.DefaultValidity != time.Hour || len(store.saved) != 1 {
		t.Fatalf("Get = %+v, saved = %+v", got, store.saved)
	}

	select {
	case <-changed:
	default:
		t.Fatal("Changed was not signalled")
	}

	delivery := Delivery{DefaultValidity: 2 * time.Hour, RetryIntervals: []time.Duration{time.Minute}}
	if err := live.UpdateDelivery(context.Background(), delivery); err != nil {
		t.Fatalf("UpdateDelivery: %v", err)
	}

	delivery.RetryIntervals[0] = time.Hour

	got = live.Get()
	if got.Operator != operator || got.Delivery.DefaultValidity != 2*time.Hour || got.Delivery.RetryIntervals[0] != time.Minute {
		t.Fatalf("Get after two updates = %+v", got)
	}
}

func TestLiveKeepsSettingsOnFailure(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings())
	changed := live.Changed()

	invalid := validOperator()
	invalid.MCC = ""

	if err := live.UpdateOperator(context.Background(), invalid); err == nil {
		t.Fatal("expected a validation error")
	}

	store.err = errors.New("disk full")

	next := validOperator()
	next.ServiceCentreAddress = "33600000000"

	if err := live.UpdateOperator(context.Background(), next); err == nil {
		t.Fatal("expected a storage error")
	}

	if live.Get().Operator != validOperator() || len(store.saved) != 0 {
		t.Fatalf("Get = %+v, saved = %+v", live.Get(), store.saved)
	}

	select {
	case <-changed:
		t.Fatal("Changed was signalled without a change")
	default:
	}
}
