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
	// TS 23.038 clause 4
	maxOctets = 140
	// TS 23.038 clause 4
	// TS 23.040 clause 9.2.3.16
	maxSeptets = 160
	// TS 23.040 clause 9.1.2.5
	maxOriginatorName = 11
)

var (
	// TS 23.040 clause 9.2.3.9
	ErrTelematicInterworking = errors.New("protocol identifier requests telematic interworking")
	// TS 23.040 clause 9.2.3.9
	ErrNotClass2 = errors.New("data coding scheme is not message class 2")
	// TS 23.040 clause 9.2.3.24
	ErrUserDataHeader = errors.New("malformed user data header")
	// TS 23.040 clause 9.2.3.9
	ErrNoApplicationPort = errors.New("no application port for this protocol identifier")
	// TS 23.038 clause 4
	ErrUserDataTooLong = errors.New("user data exceed one short message")
	ErrTextTooLong     = tpdu.ErrTextTooLong
	ErrEncodeText      = errors.New("encode text")
	ErrEncode          = errors.New("encode short message")
	ErrStore           = errors.New("store message")
)

type UserData struct {
	PID, DCS byte
	Header   []byte
	Octets   []byte
	Septets  []byte
}

type Request struct {
	Originator  db.Address
	Recipient   db.Address
	MSISDN      string
	Origin      db.MessageOrigin
	SubmittedBy int64
	ExpiresAt   time.Time
	Text        *string
	Data        *UserData
	SMPP        *db.NewSMPPMessage
}

type Store interface {
	CreateMessages(ctx context.Context, messages []db.NewMessage) ([]int64, error)
}

type Submitter struct {
	Store    Store
	Settings func() settings.Settings
	Received *intake.Received
	Now      func() time.Time

	referenceOnce sync.Once
	reference     atomic.Uint32
}

func (s *Submitter) nextReference() uint8 {
	s.referenceOnce.Do(func() { s.reference.Store(rand.Uint32()) })

	return uint8(s.reference.Add(1))
}

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

func (d UserData) submit() (tpdu.Submit, error) {
	if tpdu.RequestsTelematicInterworking(d.PID) {
		return tpdu.Submit{}, ErrTelematicInterworking
	}

	if class, ok := tpdu.MessageClass(d.DCS); (d.PID == tpdu.ProtocolUSIMDataDownload || d.PID == tpdu.ProtocolANSI136RData) &&
		(!ok || class != 2) {
		return tpdu.Submit{}, ErrNotClass2
	}

	if d.Header != nil && !tpdu.ValidUserDataHeader(d.Header) {
		return tpdu.Submit{}, ErrUserDataHeader
	}

	if tpdu.IsDeviceTrigger(d.PID) && !tpdu.HasApplicationPort(d.Header) {
		return tpdu.Submit{}, ErrNoApplicationPort
	}

	submit := tpdu.Submit{UserDataHeader: d.Header != nil, ProtocolIdentifier: d.PID, DataCodingScheme: d.DCS}

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
