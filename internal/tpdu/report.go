package tpdu

import (
	"fmt"
	"time"
)

const (
	FailureTelematicInterworkingNotSupported byte = 0x80
	FailureCommandUnsupported                byte = 0xa1
	FailureTPDUNotSupported                  byte = 0xb0
	FailureSCSystemFailure                   byte = 0xc2
	FailureInvalidSMEAddress                 byte = 0xc3
	FailureRejectedDuplicate                 byte = 0xc5
	FailureValidityPeriodNotSupported        byte = 0xc7
	FailureUnspecified                       byte = 0xff
)

func EncodeSubmitReportError(failureCause byte, serviceCentreTimeStamp time.Time) ([]byte, error) {
	scts, err := encodeTimeStamp(serviceCentreTimeStamp)
	if err != nil {
		return nil, fmt.Errorf("sms-submit-report: %w", err)
	}

	out := []byte{mtiSubmit, failureCause, 0x00}

	return append(out, scts...), nil
}

func DeliverReportFailureCause(b []byte) (byte, bool) {
	if len(b) < 2 || b[0]&0x3 != mtiDeliver {
		return 0, false
	}

	return b[1], true
}
