package intake_test

import (
	"strings"
	"testing"

	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestReceived(t *testing.T) {
	r := intake.New()

	r.Add(intake.OriginMobile, intake.Accepted, 1)
	r.Add(intake.OriginAPI, intake.Accepted, 3)
	r.Add(intake.OriginAPI, intake.Error, 0)

	want := `
# HELP ellasmsc_messages_received_total Short messages received, one per TPDU, by origin (mobile, api) and result (accepted, rejected, error).
# TYPE ellasmsc_messages_received_total counter
ellasmsc_messages_received_total{origin="api",result="accepted"} 3
ellasmsc_messages_received_total{origin="api",result="error"} 0
ellasmsc_messages_received_total{origin="mobile",result="accepted"} 1
ellasmsc_messages_received_total{origin="mobile",result="error"} 0
ellasmsc_messages_received_total{origin="mobile",result="rejected"} 0
`

	if err := testutil.CollectAndCompare(r.Collectors()[0], strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}

	problems, err := testutil.CollectAndLint(r.Collectors()[0])
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range problems {
		t.Errorf("lint: %s: %s", p.Metric, p.Text)
	}
}

func TestNilReceivedCountsNothing(t *testing.T) {
	var r *intake.Received

	r.Add(intake.OriginMobile, intake.Accepted, 1)
}
