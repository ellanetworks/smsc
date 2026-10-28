package tpdu

import (
	"fmt"
	"time"
)

const (
	FailureTelematicInterworkingNotSupported byte = 0x80
	FailureUnspecifiedTPPID                  byte = 0x8f
	FailureCommandUnsupported                byte = 0xa1
	FailureTPDUNotSupported                  byte = 0xb0
	FailureSCSystemFailure                   byte = 0xc2
	FailureInvalidSMEAddress                 byte = 0xc3
	FailureRejectedDuplicate                 byte = 0xc5
	FailureValidityPeriodNotSupported        byte = 0xc7
	FailureUSIMStorageFull                   byte = 0xd0
	FailureMemoryCapacityExceeded            byte = 0xd3
	FailureUSIMToolkitBusy                   byte = 0xd4
	FailureUSIMDataDownloadError             byte = 0xd5
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

// deliverReportUnusedBits are the bits of the first octet of an SMS-DELIVER-REPORT that are presently unused.
const deliverReportUnusedBits = 0xbc

// DeliverReportFailureCause returns the TP-FCS of an SMS-DELIVER-REPORT for RP-ERROR. When unused bits of its first
// octet are set, the receiver treats it as "Unspecified error cause" (TS 23.040 clause 9.2.2.1a).
func DeliverReportFailureCause(b []byte) (byte, bool) {
	if len(b) < 2 || b[0]&0x3 != mtiDeliver {
		return 0, false
	}

	if b[0]&deliverReportUnusedBits != 0 {
		return FailureUnspecified, true
	}

	return b[1], true
}
