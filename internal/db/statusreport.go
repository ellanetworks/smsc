package db

import (
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/tpdu"
)

// TS 23.040 clause 9.2.2.3
// TS 23.040 clause 9.2.3.14
func StatusReportFor(m Message, status tpdu.Status, discharge time.Time, serviceCentre string,
	validity time.Duration,
) (*NewStatusReport, error) {
	if m.Kind != KindMessage || m.Origin != OriginMobile || !m.StatusReportRequest {
		return nil, nil
	}

	report, err := tpdu.StatusReport{
		MessageReference: m.MessageReference,
		Recipient: tpdu.Address{
			TypeOfNumber:  m.Recipient.TypeOfNumber,
			NumberingPlan: m.Recipient.NumberingPlan,
			Digits:        m.Recipient.Digits,
		},
		ServiceCentreTimeStamp: m.SubmittedAt,
		DischargeTime:          discharge,
		Status:                 status,
	}.Encode()
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
