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

// TemporaryError is a temporary error, which the SC reports either while it still tries to transfer the short message,
// or once it has given up.
type TemporaryError byte

const (
	TemporaryCongestion      TemporaryError = 0x00
	TemporarySMEBusy         TemporaryError = 0x01
	TemporaryNoResponse      TemporaryError = 0x02
	TemporaryServiceRejected TemporaryError = 0x03
	TemporaryErrorInSME      TemporaryError = 0x05
)

// StillTrying is the status of the temporary error while the SC still tries to transfer the short message.
func (e TemporaryError) StillTrying() Status {
	return Status(0x20 | e)
}

// GaveUp is the status of the temporary error once the SC makes no more transfer attempts.
func (e TemporaryError) GaveUp() Status {
	return Status(0x60 | e)
}

// StillTrying returns, for a temporary error that the SC gave up on, the status of the same error while the SC still
// tries to transfer the short message. Any other status is returned as is.
func (s Status) StillTrying() Status {
	if s&0xe0 != 0x60 {
		return s
	}

	return TemporaryError(s & 0x1f).StillTrying()
}

// StatusClass is what a status says of the short message, which decides whether a status report is wanted.
type StatusClass int

const (
	ClassCompleted StatusClass = iota
	ClassStillTrying
	ClassPermanent
	ClassGaveUp
)

// String returns the name of the class.
func (c StatusClass) String() string {
	switch c {
	case ClassCompleted:
		return "completed"
	case ClassStillTrying:
		return "still_trying"
	case ClassPermanent:
		return "permanent"
	case ClassGaveUp:
		return "gave_up"
	default:
		return "unknown"
	}
}

// statusNames are the meanings of the statuses that TS 23.040 clause 9.2.3.15 defines. A temporary error has the same
// meaning whether the SC still tries or has given up, which the class of the status tells.
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

	if s.Class() == ClassGaveUp {
		s = s.StillTrying()
	}

	if name, ok := statusNames[s]; ok {
		return name
	}

	if s&0x10 != 0 {
		return "sc_specific"
	}

	return "reserved"
}

// Class returns the class of the status. A reserved status, with bit 7 set, is a service rejected, as the MS reads it.
func (s Status) Class() StatusClass {
	if s&0x80 != 0 {
		return ClassGaveUp
	}

	switch s & 0x60 {
	case 0x00:
		return ClassCompleted
	case 0x20:
		return ClassStillTrying
	case 0x40:
		return ClassPermanent
	default:
		return ClassGaveUp
	}
}

// StatusReportRequest is what an SMS-SUBMIT asks of the status reports of its short message: TP-SRR, and the Selective
// Status Report of the SMSC Control Parameters in its user data header (TS 23.040 clause 9.2.3.24.5).
type StatusReportRequest struct {
	Requested bool
	selective *byte
}

const (
	selectiveCompleted   = 0x01
	selectivePermanent   = 0x02
	selectiveGaveUp      = 0x04
	selectiveStillTrying = 0x08
	selectiveIncludeUDH  = 0x80
)

// StatusReports returns what the SMS-SUBMIT asks of status reports. A malformed user data header carries no SMSC
// Control Parameters.
func (s Submit) StatusReports() StatusReportRequest {
	r := StatusReportRequest{Requested: s.StatusReportRequest}

	header, err := userDataHeader(s.UserDataHeader, s.UserData)
	if err != nil || header == nil {
		return r
	}

	ies, ok := informationElements(header[1:])
	if !ok {
		return r
	}

	for _, ie := range ies {
		if ie.id == ieiSMSCControlParameters && len(ie.data) > 0 {
			selective := ie.data[0]
			r.selective = &selective
		}
	}

	return r
}

// Wants tells whether the SMS-SUBMIT wants a status report with the status.
func (r StatusReportRequest) Wants(s Status) bool {
	return r.wantsClass(s.Class())
}

// Classes returns the classes of the statuses that the SMS-SUBMIT wants status reports with, in the order of the SMS
// lifecycle. Without a Selective Status Report, it wants those once the SC has concluded the status of the short
// message (TS 23.040 clause 3.2.9), but not while the SC still tries to transfer it.
func (r StatusReportRequest) Classes() []StatusClass {
	var classes []StatusClass

	for _, c := range []StatusClass{ClassStillTrying, ClassCompleted, ClassPermanent, ClassGaveUp} {
		if r.wantsClass(c) {
			classes = append(classes, c)
		}
	}

	return classes
}

func (r StatusReportRequest) wantsClass(c StatusClass) bool {
	if !r.Requested {
		return false
	}

	if r.selective == nil {
		return c != ClassStillTrying
	}

	bit := map[StatusClass]byte{
		ClassCompleted:   selectiveCompleted,
		ClassPermanent:   selectivePermanent,
		ClassGaveUp:      selectiveGaveUp,
		ClassStillTrying: selectiveStillTrying,
	}[c]

	return *r.selective&bit != 0
}

// IncludesUserDataHeader tells whether the SMS-SUBMIT wants its user data header in its status reports.
func (r StatusReportRequest) IncludesUserDataHeader() bool {
	return r.Requested && r.selective != nil && *r.selective&selectiveIncludeUDH != 0
}
