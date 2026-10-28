// Package submission stores the short messages that an application submits to the SC: over the HTTP API, and as
// submit_sm from an ESME over SMPP (SMPP 3.4 section 4.4). Both take the same path, so that the same parameters get
// the same decision and the same stored SMS-SUBMIT, which the delivery turns into the SMS-DELIVER (TS 23.040 clause
// 9.2.2).
package submission

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/statusreport"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

const (
	// maxOctets is the most octets of TP-UD in 8-bit data or UCS2 (TS 23.038 clause 4).
	maxOctets = 140
	// maxSeptets is the most septets of TP-UD in the GSM 7 bit default alphabet, the header's included (TS 23.038
	// clause 4, TS 23.040 clause 9.2.3.16).
	maxSeptets = 160
	// maxOriginatorName is the most characters of an alphanumeric originator (TS 23.040 clause 9.1.2.5).
	maxOriginatorName = 11
)

// The reasons that the user data of a message are invalid.
var (
	// ErrTelematicInterworking is a TP-PID that asks for a conversion for a telematic device, which the SC does not
	// have (TS 23.040 clause 9.2.3.9).
	ErrTelematicInterworking = errors.New("protocol identifier requests telematic interworking")
	// ErrNotClass2 is a (U)SIM data download or ANSI-136 R-DATA message that is not message class 2, which an ME
	// would handle as a normal message (TS 23.040 clause 9.2.3.9).
	ErrNotClass2 = errors.New("data coding scheme is not message class 2")
	// ErrUserDataHeader is a malformed user data header (TS 23.040 clause 9.2.3.24).
	ErrUserDataHeader = errors.New("malformed user data header")
	// ErrNoApplicationPort is a device trigger without the application port addressing information element that names
	// its application (TS 23.040 clause 9.2.3.9).
	ErrNoApplicationPort = errors.New("no application port for this protocol identifier")
	// ErrUserDataTooLong is user data that do not fit one TPDU: 140 octets, or 160 septets (TS 23.038 clause 4).
	ErrUserDataTooLong = errors.New("user data exceed one short message")
	// ErrTextTooLong is text that needs more than 255 short messages.
	ErrTextTooLong = tpdu.ErrTextTooLong
	// ErrEncodeText is a failure to encode text.
	ErrEncodeText = errors.New("encode text")
	// ErrEncode is a failure to encode the SMS-SUBMIT.
	ErrEncode = errors.New("encode short message")
	// ErrStore is a failure to store the messages.
	ErrStore = errors.New("store message")
)

// UserData are the TPDU parameters of a message that the caller codes itself, which make exactly one TPDU.
type UserData struct {
	PID, DCS byte
	// Header is the TP-UDH with its length octet, or nil.
	Header []byte
	// Octets are the user data after the header, in 8-bit data or UCS2.
	Octets []byte
	// Septets are the user data after the header in the GSM 7 bit default alphabet, unpacked, one per octet. They are
	// set only for a TP-DCS that codes the GSM 7 bit default alphabet (tpdu.IsSeptetCoded), and packed by the SC.
	Septets []byte
}

// Request is a message to store.
type Request struct {
	Originator db.Address
	// Recipient is the destination address as submitted.
	Recipient db.Address
	// MSISDN is the recipient's MSISDN in international format.
	MSISDN      string
	Origin      db.MessageOrigin
	SubmittedBy int64
	// ExpiresAt is when the SC stops attempting delivery; zero is now plus the default validity period.
	ExpiresAt time.Time
	// Text is text, which the SC codes, in several TPDUs if it needs to. Text and Data are mutually exclusive.
	Text *string
	// Data is user data that make exactly one TPDU.
	Data *UserData
	// SMPP is what the SC tells the ESME that submits the message, stored in the same transaction.
	SMPP *db.NewSMPPMessage
}

// Store stores messages.
type Store interface {
	CreateMessages(ctx context.Context, messages []db.NewMessage) ([]int64, error)
}

// Submitter stores the messages that applications submit. The caller tells the delivery that they are stored: the API
// at once, SMPP once the ESME has the message_id, so that a deliver_sm about a message never precedes it.
type Submitter struct {
	Store    Store
	Settings func() settings.Settings
	// Received counts the messages stored, and those that the store failed to store.
	Received *intake.Received
	Now      func() time.Time

	// reference is the last concatenated short message reference (TS 23.040 clause 9.2.3.24.1). It starts at a random
	// value, so that a restart does not reuse the last ones.
	referenceOnce sync.Once
	reference     atomic.Uint32
}

func (s *Submitter) nextReference() uint8 {
	s.referenceOnce.Do(func() { s.reference.Store(rand.Uint32()) })

	return uint8(s.reference.Add(1))
}

// Submit stores the messages of a request and returns their IDs. It returns one of the errors of this package that
// tells why it did not.
func (s *Submitter) Submit(ctx context.Context, r Request) ([]int64, error) {
	destination := tpdu.Address{
		TypeOfNumber:  tpdu.TypeOfNumberInternational,
		NumberingPlan: tpdu.NumberingPlanISDN,
		Digits:        r.MSISDN,
	}

	var submits []tpdu.Submit

	switch {
	case r.Data != nil:
		submit, err := r.Data.submit()
		if err != nil {
			return nil, err
		}

		submit.Destination = destination
		submits = []tpdu.Submit{submit}
	case r.Text != nil:
		parts, _, err := tpdu.EncodeText(*r.Text, s.nextReference())
		if errors.Is(err, tpdu.ErrTextTooLong) {
			return nil, err
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrEncodeText, err)
		}

		for _, p := range parts {
			submits = append(submits, tpdu.Submit{
				Destination:      destination,
				UserDataHeader:   p.UserDataHeader,
				DataCodingScheme: p.DataCodingScheme,
				UserDataLength:   p.UserDataLength,
				UserData:         p.UserData,
			})
		}
	default:
		return nil, fmt.Errorf("%w: no text or user data", ErrEncode)
	}

	now := s.Now()
	current := s.Settings()

	expiresAt := r.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(current.Delivery.DefaultValidity)
	}

	messages := make([]db.NewMessage, 0, len(submits))

	for _, submit := range submits {
		b, err := submit.Encode()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrEncode, err)
		}

		m := db.NewMessage{
			Originator:         r.Originator,
			Recipient:          r.Recipient,
			MSISDN:             r.MSISDN,
			Origin:             r.Origin,
			SubmittedBy:        r.SubmittedBy,
			ProtocolIdentifier: submit.ProtocolIdentifier,
			TPDU:               b,
			SubmittedAt:        now,
			ExpiresAt:          expiresAt,
			SMPP:               r.SMPP,
		}

		// The SC replaces a message from any source as it does one from an MS (TS 23.040 clause 9.2.3.9). A replaced
		// message from an MS still gets the status report it asked for.
		if tpdu.IsReplaceType(submit.ProtocolIdentifier) {
			m.Replace = true
			m.ReportReplaced = func(replaced db.Message) (*db.NewStatusReport, error) {
				return statusreport.For(replaced, tpdu.StatusReplaced, now, current)
			}
		}

		messages = append(messages, m)
	}

	origin := receivedOrigin(r.Origin)

	ids, err := s.Store.CreateMessages(ctx, messages)
	if err != nil {
		s.Received.Add(origin, intake.Error, len(messages))
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}

	s.Received.Add(origin, intake.Accepted, len(ids))

	return ids, nil
}

func receivedOrigin(o db.MessageOrigin) string {
	if o == db.OriginSMPP {
		return intake.OriginSMPP
	}

	return intake.OriginAPI
}

// submit returns the SMS-SUBMIT, without its destination, of the user data, or the reason that they are invalid.
func (d UserData) submit() (tpdu.Submit, error) {
	// The SC has no function to convert a message for a telematic device (TS 23.040 clause 9.2.3.9).
	if tpdu.RequestsTelematicInterworking(d.PID) {
		return tpdu.Submit{}, ErrTelematicInterworking
	}

	// An ME handles a (U)SIM data download or ANSI-136 R-DATA message that is not class 2 as a normal message
	// (TS 23.040 clause 9.2.3.9), which would show the user what was meant for the (U)SIM.
	if class, ok := tpdu.MessageClass(d.DCS); (d.PID == tpdu.ProtocolUSIMDataDownload || d.PID == tpdu.ProtocolANSI136RData) &&
		(!ok || class != 2) {
		return tpdu.Submit{}, ErrNotClass2
	}

	if d.Header != nil && !tpdu.ValidUserDataHeader(d.Header) {
		return tpdu.Submit{}, ErrUserDataHeader
	}

	// The application port addressing information element names the application that a device trigger is for
	// (TS 23.040 clause 9.2.3.9).
	if tpdu.IsDeviceTrigger(d.PID) && !tpdu.HasApplicationPort(d.Header) {
		return tpdu.Submit{}, ErrNoApplicationPort
	}

	submit := tpdu.Submit{UserDataHeader: d.Header != nil, ProtocolIdentifier: d.PID, DataCodingScheme: d.DCS}

	// The TP-UDL counts septets for the GSM 7 bit default alphabet, and octets otherwise (TS 23.040 clause 9.2.3.16).
	if tpdu.IsSeptetCoded(d.DCS) {
		if tpdu.HeaderSeptets(d.Header)+len(d.Septets) > maxSeptets {
			return tpdu.Submit{}, ErrUserDataTooLong
		}

		submit.UserDataLength, submit.UserData = tpdu.PackSeptets(d.Header, d.Septets)

		return submit, nil
	}

	ud := append(append([]byte{}, d.Header...), d.Octets...)
	if len(ud) > maxOctets {
		return tpdu.Submit{}, ErrUserDataTooLong
	}

	submit.UserDataLength, submit.UserData = uint8(len(ud)), ud

	return submit, nil
}

// IsOriginatorName reports whether s is 1 to 11 ASCII letters and digits, with single spaces between words, not all
// digits. An all-digit value is a number, not a name.
func IsOriginatorName(s string) bool {
	if s == "" || len(s) > maxOriginatorName || s[0] == ' ' || s[len(s)-1] == ' ' || strings.Contains(s, "  ") {
		return false
	}

	digits := true

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == ' ':
			digits = false
		default:
			return false
		}
	}

	return !digits
}
