package sgd

import (
	"context"
	"errors"
	"testing"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/prometheus/client_golang/prometheus"
)

// received is the count of messages from phones with a result.
func received(t *testing.T, r *intake.Received, result string) float64 {
	t.Helper()

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(r.Collectors()...)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range families {
		for _, m := range f.GetMetric() {
			l := map[string]string{}
			for _, p := range m.GetLabel() {
				l[p.GetName()] = p.GetValue()
			}

			if l["origin"] == intake.OriginMobile && l["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}

	t.Fatalf("no series for result %q", result)

	return 0
}

func TestMOForwardCountsReceivedMessages(t *testing.T) {
	for _, tt := range []struct {
		name   string
		store  *fakeStore
		submit string
		want   string
	}{
		{"accepted", &fakeStore{}, validSubmit, intake.Accepted},
		{"rejected submit", &fakeStore{}, "03" + "00", intake.Rejected},
		{"rejected duplicate", &fakeStore{err: db.ErrDuplicate}, validSubmit, intake.Rejected},
		{"store failure", &fakeStore{err: errors.New("disk full")}, validSubmit, intake.Error},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			h.Received = intake.New()

			h.ServeDiameter(context.Background(), nil, ofrWithSubmit(t, tt.submit))

			for _, result := range []string{intake.Accepted, intake.Rejected, intake.Error} {
				want := 0.0
				if result == tt.want {
					want = 1
				}

				if got := received(t, h.Received, result); got != want {
					t.Errorf("received{result=%q} = %v, want %v", result, got, want)
				}
			}
		})
	}
}

func TestMOForwardCountsMalformedRequests(t *testing.T) {
	h := newTestHandler(&fakeStore{})
	h.Received = intake.New()

	// A request without its mandatory AVPs is refused before it is decoded.
	h.ServeDiameter(context.Background(), nil, ofr())

	if got := received(t, h.Received, intake.Rejected); got != 1 {
		t.Fatalf("received{result=rejected} = %v, want 1", got)
	}
}
