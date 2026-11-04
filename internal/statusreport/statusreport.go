package statusreport

import (
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

func For(m db.Message, status tpdu.Status, discharge time.Time, s settings.Settings) (*db.NewStatusReport, error) {
	return db.StatusReportFor(m, status, discharge, s.Operator.ServiceCentreAddress, s.Delivery.DefaultValidity)
}
