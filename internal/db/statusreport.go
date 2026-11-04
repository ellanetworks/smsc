package db

import (
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/tpdu"
)

func StatusReportFor(m Message, status tpdu.Status, discharge time.Time, serviceCentre string,
	validity time.Duration,
) (*NewStatusReport, error) {
	if m.Kind != KindMessage || m.Origin != OriginMobile {
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

	return &NewStatusReport{
		Originator: Address{
			Digits:        serviceCentre,
			TypeOfNumber:  tpdu.TypeOfNumberInternational,
			NumberingPlan: tpdu.NumberingPlanISDN,
		},
		Status:    uint8(status),
		TPDU:      report,
		CreatedAt: discharge,
		ExpiresAt: discharge.Add(validity),
	}, nil
}
