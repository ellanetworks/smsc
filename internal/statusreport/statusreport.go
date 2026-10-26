// Package statusreport makes the SMS-STATUS-REPORTs that the SC returns to an MS on the short messages that the MS
// submitted (TS 23.040 clause 3.2.9).
package statusreport

import (
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

// For returns the status report on a message, with the status that the SC concluded at discharge. It returns nil when
// the message wants no report: only a short message from an MS asks for reports, with the TP-SRR of its SMS-SUBMIT, and
// no report is on a report. The SC tries to deliver the report for as long as the default validity period.
func For(m db.Message, status tpdu.Status, discharge time.Time, s settings.Settings) (*db.NewStatusReport, error) {
	if m.Kind != db.KindMessage || m.Origin != db.OriginMobile {
		return nil, nil
	}

	submit, err := tpdu.DecodeSubmit(m.TPDU)
	if err != nil {
		return nil, fmt.Errorf("status report on message %d: %w", m.ID, err)
	}

	if !submit.StatusReportRequest {
		return nil, nil
	}

	report, err := submit.StatusReport(m.SubmittedAt, discharge, status).Encode()
	if err != nil {
		return nil, fmt.Errorf("status report on message %d: %w", m.ID, err)
	}

	return &db.NewStatusReport{
		Originator: db.Address{
			Digits:        s.Operator.ServiceCentreAddress,
			TypeOfNumber:  tpdu.TypeOfNumberInternational,
			NumberingPlan: tpdu.NumberingPlanISDN,
		},
		Status:    uint8(status),
		TPDU:      report,
		CreatedAt: discharge,
		ExpiresAt: discharge.Add(s.Delivery.DefaultValidity),
	}, nil
}
