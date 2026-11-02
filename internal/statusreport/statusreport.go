package statusreport

import (
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

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
