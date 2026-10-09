package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func answer(avps ...diameter.AVP) *diameter.Message {
	return &diameter.Message{AVPs: avps}
}

func resultCode(code uint32) diameter.AVP {
	return diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, code)
}

func experimentalResult(code uint32) diameter.AVP {
	return diameter.Grouped(diameter.AVPExperimentalResult, 0, 0,
		diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, tgpp.VendorID),
		diameter.Unsigned32(diameter.AVPExperimentalResultCode, diameter.AVPFlagMandatory, 0, code))
}

func TestPeerResult(t *testing.T) {
	for _, tt := range []struct {
		name string
		ans  *diameter.Message
		err  error
		want string
	}{
		{"success", answer(resultCode(diameter.ResultSuccess)), nil, peerSuccess},
		{"failure", answer(resultCode(diameter.ResultUnableToDeliver)), nil, peerFailure},
		{"experimental failure", answer(experimentalResult(tgpp.ResultErrorUserUnknown)), nil, peerFailure},
		{"absent user", answer(experimentalResult(tgpp.ResultErrorAbsentUser)), nil, peerAbsentUser},
		{"no result", answer(), nil, peerError},
		{"not connected", nil, diameter.ErrNotConnected, peerError},
		{"timeout", nil, fmt.Errorf("TFR: %w", context.DeadlineExceeded), peerTimeout},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerResult(tt.ans, tt.err); got != tt.want {
				t.Fatalf("peerResult = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPeerMetrics(t *testing.T) {
	m := newPeerMetrics()

	m.request(interfaceSGd, peerAbsentUser, 2*time.Second)

	if got := testutil.ToFloat64(m.requests.WithLabelValues(interfaceSGd, peerAbsentUser)); got != 1 {
		t.Fatalf("requests{sgd, absent_user} = %v, want 1", got)
	}

	if n := testutil.CollectAndCount(m.requests); n != len(peerInterfaces)*len(peerResults) {
		t.Fatalf("requests has %d series, want %d", n, len(peerInterfaces)*len(peerResults))
	}

	for _, c := range m.collectors() {
		problems, err := testutil.CollectAndLint(c)
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range problems {
			t.Errorf("lint: %s: %s", p.Metric, p.Text)
		}
	}
}

func TestHSSPeersCollector(t *testing.T) {
	c := hssPeersCollector{count: func() int { return 2 }}

	if got := testutil.ToFloat64(c); got != 2 {
		t.Fatalf("hss peers = %v, want 2", got)
	}

	problems, err := testutil.CollectAndLint(c)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range problems {
		t.Errorf("lint: %s: %s", p.Metric, p.Text)
	}
}
