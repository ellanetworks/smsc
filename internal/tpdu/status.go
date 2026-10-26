package tpdu

// Status is the TP-Status of an SMS-STATUS-REPORT (TS 23.040 clause 9.2.3.15).
type Status byte

// The statuses of a transaction completed, and of permanent errors, after which the SC makes no more transfer attempts.
const (
	StatusReceived                Status = 0x00
	StatusReplaced                Status = 0x02
	StatusRemoteProcedureError    Status = 0x40
	StatusIncompatibleDestination Status = 0x41
	StatusConnectionRejected      Status = 0x42
	StatusNotObtainable           Status = 0x43
	StatusNoInterworking          Status = 0x45
	StatusValidityPeriodExpired   Status = 0x46
)

// TemporaryError is a temporary error, which the SC reports once it has given up transferring the short message.
type TemporaryError byte

const (
	TemporaryCongestion      TemporaryError = 0x00
	TemporarySMEBusy         TemporaryError = 0x01
	TemporaryNoResponse      TemporaryError = 0x02
	TemporaryServiceRejected TemporaryError = 0x03
	TemporaryErrorInSME      TemporaryError = 0x05
)

// GaveUp is the status of the temporary error once the SC makes no more transfer attempts.
func (e TemporaryError) GaveUp() Status {
	return Status(0x60 | e)
}

// statusNames are the meanings of the statuses that TS 23.040 clause 9.2.3.15 defines.
var statusNames = map[Status]string{
	0x00: "received",
	0x01: "forwarded_unconfirmed",
	0x02: "replaced",
	0x20: "congestion",
	0x21: "sme_busy",
	0x22: "no_response",
	0x23: "service_rejected",
	0x24: "quality_of_service_not_available",
	0x25: "error_in_sme",
	0x40: "remote_procedure_error",
	0x41: "incompatible_destination",
	0x42: "connection_rejected",
	0x43: "not_obtainable",
	0x44: "quality_of_service_not_available",
	0x45: "no_interworking",
	0x46: "validity_period_expired",
	0x47: "deleted_by_originating_sme",
	0x48: "deleted_by_sc_administration",
	0x49: "does_not_exist",
}

// String returns the meaning of the status: its name in TS 23.040 clause 9.2.3.15, "sc_specific" for a value specific
// to each SC, or "reserved".
func (s Status) String() string {
	if s&0x80 != 0 {
		return "reserved"
	}

	// A temporary error has the same meaning whether the SC still tries or has given up.
	if s&0x60 == 0x60 {
		s &^= 0x40
	}

	if name, ok := statusNames[s]; ok {
		return name
	}

	if s&0x10 != 0 {
		return "sc_specific"
	}

	return "reserved"
}
