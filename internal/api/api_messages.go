package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

const (
	defaultPerPage = 25
	maxPerPage     = 100
	maxE164Digits  = 15
)

type CreateMessageParams struct {
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
}

// MessageContent is the content of a short message, which the other responses leave out: reading it is audited.
type MessageContent struct {
	// Text is the text of a message that is not binary.
	Text *string `json:"text,omitempty"`
}

type Concatenation struct {
	Reference uint16 `json:"reference"`
	Part      uint8  `json:"part"`
	Total     uint8  `json:"total"`
}

type Message struct {
	ID            int64          `json:"id"`
	From          string         `json:"from"`
	To            string         `json:"to"`
	Encoding      string         `json:"encoding"`
	Concatenation *Concatenation `json:"concatenation,omitempty"`
	Status        string         `json:"status"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	ExpiresAt     string         `json:"expires_at"`
	NextAttemptAt string         `json:"next_attempt_at,omitempty"`
	StatusReport  StatusReports  `json:"status_report"`
	// SubmittedBy is the email of the user that submitted a message over the API.
	SubmittedBy string `json:"submitted_by,omitempty"`
}

// StatusReports are whether the originator of a message asked for status reports, and the reports that the SC returned
// to it (TS 23.040 clause 3.2.9). Only a phone asks for status reports.
type StatusReports struct {
	Requested bool           `json:"requested"`
	Reports   []StatusReport `json:"reports"`
}

// StatusReport is an SMS-STATUS-REPORT, with the TP-Status that it tells the originator, and the state of its delivery.
type StatusReport struct {
	ID            int64  `json:"id"`
	TPStatus      uint8  `json:"tp_status"`
	TPStatusName  string `json:"tp_status_name"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	ExpiresAt     string `json:"expires_at"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
}

type Attempt struct {
	ID          int64   `json:"id"`
	StartedAt   string  `json:"started_at"`
	CompletedAt string  `json:"completed_at"`
	Step        string  `json:"step"`
	Node        string  `json:"node,omitempty"`
	NodeType    string  `json:"node_type,omitempty"`
	Outcome     string  `json:"outcome"`
	ResultCode  *uint32 `json:"result_code,omitempty"`
	VendorID    *uint32 `json:"vendor_id,omitempty"`

	FailureCause          string                 `json:"failure_cause,omitempty"`
	TPFailureCause        string                 `json:"tp_failure_cause,omitempty"`
	AbsentUserDiagnostics *AbsentUserDiagnostics `json:"absent_user_diagnostics,omitempty"`
}

type AbsentUserDiagnostics struct {
	MME         string `json:"mme,omitempty"`
	MSC         string `json:"msc,omitempty"`
	SGSN        string `json:"sgsn,omitempty"`
	IPSMGW      string `json:"ip_sm_gw,omitempty"`
	SMSF3GPP    string `json:"smsf_3gpp,omitempty"`
	SMSFNon3GPP string `json:"smsf_non_3gpp,omitempty"`
}

type CreateMessageResponse struct {
	Items []Message `json:"items"`
}

type ListMessagesResponse struct {
	Items      []Message `json:"items"`
	Page       int       `json:"page"`
	PerPage    int       `json:"per_page"`
	TotalCount int       `json:"total_count"`
}

type ListAttemptsResponse struct {
	Items      []Attempt `json:"items"`
	Page       int       `json:"page"`
	PerPage    int       `json:"per_page"`
	TotalCount int       `json:"total_count"`
}

func CreateMessage(cfg Config) http.Handler {
	var reference atomic.Uint32

	reference.Store(rand.Uint32())

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params CreateMessageParams

		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
			return
		}

		from, ok := e164Digits(params.From)
		if !ok {
			writeError(w, http.StatusBadRequest, "from must be an E.164 number such as +15551230001", nil, cfg.Logger)
			return
		}

		to, ok := e164Digits(params.To)
		if !ok {
			writeError(w, http.StatusBadRequest, "to must be an E.164 number such as +15551230002", nil, cfg.Logger)
			return
		}

		if params.Text == "" {
			writeError(w, http.StatusBadRequest, "text is required", nil, cfg.Logger)
			return
		}

		parts, _, err := tpdu.EncodeText(params.Text, uint8(reference.Add(1)))
		if errors.Is(err, tpdu.ErrTextTooLong) {
			writeError(w, http.StatusBadRequest, "text is too long", err, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to encode text", err, cfg.Logger)
			return
		}

		caller, _ := PrincipalFrom(r.Context())
		recipient := db.Address{Digits: to, TypeOfNumber: tpdu.TypeOfNumberInternational, NumberingPlan: tpdu.NumberingPlanISDN}
		now := cfg.Now()
		validity := cfg.Settings.Get().Delivery.DefaultValidity

		messages := make([]db.NewMessage, 0, len(parts))

		for _, p := range parts {
			b, err := tpdu.Submit{
				Destination: tpdu.Address{
					TypeOfNumber:  recipient.TypeOfNumber,
					NumberingPlan: recipient.NumberingPlan,
					Digits:        recipient.Digits,
				},
				UserDataHeader:   p.UserDataHeader,
				DataCodingScheme: p.DataCodingScheme,
				UserDataLength:   p.UserDataLength,
				UserData:         p.UserData,
			}.Encode()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to encode short message", err, cfg.Logger)
				return
			}

			messages = append(messages, db.NewMessage{
				Originator:  db.Address{Digits: from, TypeOfNumber: tpdu.TypeOfNumberInternational, NumberingPlan: tpdu.NumberingPlanISDN},
				Recipient:   recipient,
				MSISDN:      to,
				Origin:      db.OriginAPI,
				SubmittedBy: caller.UserID,
				TPDU:        b,
				SubmittedAt: now,
				ExpiresAt:   now.Add(validity),
			})
		}

		ids, err := cfg.Store.CreateMessages(r.Context(), messages)
		if err != nil {
			cfg.Received.Add(intake.OriginAPI, intake.Error, len(messages))
			writeError(w, http.StatusInternalServerError, "Failed to store message", err, cfg.Logger)

			return
		}

		cfg.Received.Add(intake.OriginAPI, intake.Accepted, len(ids))
		cfg.Notify()

		stored := make([]db.Message, 0, len(ids))

		for _, id := range ids {
			m, err := cfg.Store.GetMessage(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
				return
			}

			stored = append(stored, m)
		}

		items, err := messagesOf(r.Context(), cfg, stored)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
			return
		}

		writeResponse(w, CreateMessageResponse{Items: items}, http.StatusCreated, cfg.Logger)
	})
}

func GetMessage(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, ok := lookupMessage(w, r, cfg)
		if !ok {
			return
		}

		items, err := messagesOf(r.Context(), cfg, []db.Message{m})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
			return
		}

		writeResponse(w, items[0], http.StatusOK, cfg.Logger)
	})
}

// GetMessageContent returns the content of a short message, and logs an audit event that the caller read it.
func GetMessageContent(cfg Config, a *auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, ok := lookupMessage(w, r, cfg)
		if !ok {
			return
		}

		var out MessageContent

		if submit, err := tpdu.DecodeSubmit(m.TPDU); err == nil {
			if content, err := submit.Content(); err == nil && content.Encoding != tpdu.EncodingBinary {
				out.Text = &content.Text
			}
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "read_message_content", p.Email, fmt.Sprintf("Content of message %d read", m.ID))

		writeResponse(w, out, http.StatusOK, cfg.Logger)
	})
}

func ListMessageAttempts(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		m, ok := lookupMessage(w, r, cfg)
		if !ok {
			return
		}

		attempts, total, err := cfg.Store.ListDeliveryAttempts(r.Context(), m.ID, page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list delivery attempts", err, cfg.Logger)
			return
		}

		resp := ListAttemptsResponse{Items: make([]Attempt, 0, len(attempts)), Page: page, PerPage: perPage, TotalCount: total}

		for _, a := range attempts {
			resp.Items = append(resp.Items, attemptOf(a))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

// ownSubmissions returns the user whose submissions are the only messages the caller may read, or zero when it may read
// all of them.
func ownSubmissions(ctx context.Context) int64 {
	if p, _ := PrincipalFrom(ctx); p.Role == db.RoleSubmitter {
		return p.UserID
	}

	return 0
}

// visible is whether the caller may read a message. A Submitter may read only those it submitted; to it, the others
// do not exist.
func visible(ctx context.Context, m db.Message) bool {
	only := ownSubmissions(ctx)
	return only == 0 || m.SubmittedBy == only
}

// lookupMessage returns the short message of the request. A status report is not one: it is under the message it is
// on.
func lookupMessage(w http.ResponseWriter, r *http.Request, cfg Config) (db.Message, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid message ID", err, cfg.Logger)
		return db.Message{}, false
	}

	m, err := cfg.Store.GetMessage(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) || err == nil && (m.Kind != db.KindMessage || !visible(r.Context(), m)) {
		writeError(w, http.StatusNotFound, "Message not found", err, cfg.Logger)
		return db.Message{}, false
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
		return db.Message{}, false
	}

	return m, true
}

func pagination(w http.ResponseWriter, r *http.Request, cfg Config) (int, int, bool) {
	q := r.URL.Query()

	page, ok := atoiDefault(q.Get("page"), 1)
	if !ok || page < 1 {
		writeError(w, http.StatusBadRequest, "page must be an integer >= 1", nil, cfg.Logger)
		return 0, 0, false
	}

	perPage, ok := atoiDefault(q.Get("per_page"), defaultPerPage)
	if !ok || perPage < 1 || perPage > maxPerPage {
		writeError(w, http.StatusBadRequest, "per_page must be an integer between 1 and 100", nil, cfg.Logger)
		return 0, 0, false
	}

	return page, perPage, true
}

func attemptOf(a db.DeliveryAttempt) Attempt {
	return Attempt{
		ID:          a.ID,
		StartedAt:   formatTime(a.StartedAt),
		CompletedAt: formatTime(a.CompletedAt),
		Step:        string(a.Step),
		Node:        a.Node,
		NodeType:    string(a.NodeType),
		Outcome:     a.Outcome,
		ResultCode:  a.ResultCode,
		VendorID:    a.VendorID,

		FailureCause:          a.FailureCause,
		TPFailureCause:        a.TPFailureCause,
		AbsentUserDiagnostics: absentUserDiagnosticsOf(a.AbsentUserDiagnostics),
	}
}

func ListMessages(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		q := r.URL.Query()

		filter := db.MessageFilter{SubmittedBy: ownSubmissions(r.Context())}

		if to := q.Get("to"); to != "" {
			if filter.MSISDN, ok = e164Digits(to); !ok {
				writeError(w, http.StatusBadRequest, "to must be an E.164 number such as +15551230002", nil, cfg.Logger)
				return
			}
		}

		if from := q.Get("from"); from != "" {
			if filter.Originator, ok = e164Digits(from); !ok {
				writeError(w, http.StatusBadRequest, "from must be an E.164 number such as +15551230001", nil, cfg.Logger)
				return
			}
		}

		if status := db.MessageStatus(q.Get("status")); status != "" {
			switch status {
			case db.StatusPending, db.StatusDelivered, db.StatusFailed, db.StatusExpired, db.StatusReplaced:
				filter.Status = status
			default:
				writeError(w, http.StatusBadRequest, "status must be one of pending, delivered, failed, expired, replaced", nil,
					cfg.Logger)

				return
			}
		}

		if filter.Start, ok = timeParam(w, q.Get("start"), "start", cfg); !ok {
			return
		}

		if filter.End, ok = timeParam(w, q.Get("end"), "end", cfg); !ok {
			return
		}

		if !filter.Start.IsZero() && !filter.End.IsZero() && filter.End.Before(filter.Start) {
			writeError(w, http.StatusBadRequest, "end must not be before start", nil, cfg.Logger)
			return
		}

		messages, total, err := cfg.Store.ListMessages(r.Context(), filter, page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list messages", err, cfg.Logger)
			return
		}

		items, err := messagesOf(r.Context(), cfg, messages)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list messages", err, cfg.Logger)
			return
		}

		writeResponse(w, ListMessagesResponse{Items: items, Page: page, PerPage: perPage, TotalCount: total}, http.StatusOK,
			cfg.Logger)
	})
}

// messagesOf returns the messages, each with the status reports on it.
func messagesOf(ctx context.Context, cfg Config, messages []db.Message) ([]Message, error) {
	ids := make([]int64, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.ID)
	}

	reports, err := cfg.Store.StatusReportsFor(ctx, ids)
	if err != nil {
		return nil, err
	}

	emails := map[int64]string{}

	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		msg := messageOf(m, reports[m.ID])

		if id := m.SubmittedBy; id != 0 {
			email, ok := emails[id]
			if !ok {
				u, err := cfg.Auth.GetUserByID(ctx, id)
				if err != nil && !errors.Is(err, db.ErrNotFound) {
					return nil, err
				}

				email, emails[id] = u.Email, u.Email
			}

			msg.SubmittedBy = email
		}

		out = append(out, msg)
	}

	return out, nil
}

func messageOf(m db.Message, reports []db.Message) Message {
	out := Message{
		ID:        m.ID,
		From:      formatAddress(m.Originator),
		To:        "+" + m.MSISDN,
		Encoding:  string(tpdu.EncodingBinary),
		Status:    string(m.Status),
		CreatedAt: formatTime(m.SubmittedAt),
		UpdatedAt: formatTime(m.UpdatedAt),
		ExpiresAt: formatTime(m.ExpiresAt),
	}

	if m.Status == db.StatusPending {
		out.NextAttemptAt = formatTime(m.NextAttemptAt)
	}

	out.StatusReport = StatusReports{Reports: make([]StatusReport, 0, len(reports))}

	for _, r := range reports {
		out.StatusReport.Reports = append(out.StatusReport.Reports, statusReportOf(r))
	}

	submit, err := tpdu.DecodeSubmit(m.TPDU)
	if err != nil {
		return out
	}

	// Only a phone asks for status reports; the API never does.
	out.StatusReport.Requested = m.Origin == db.OriginMobile && submit.StatusReportRequest

	content, err := submit.Content()
	if err != nil {
		return out
	}

	out.Encoding = string(content.Encoding)

	if c := content.Concatenation; c != nil {
		out.Concatenation = &Concatenation{Reference: c.Reference, Part: c.Part, Total: c.Total}
	}

	return out
}

func statusReportOf(r db.Message) StatusReport {
	status := tpdu.Status(r.ReportStatus)

	out := StatusReport{
		ID:           r.ID,
		TPStatus:     r.ReportStatus,
		TPStatusName: status.String(),
		Status:       string(r.Status),
		CreatedAt:    formatTime(r.SubmittedAt),
		UpdatedAt:    formatTime(r.UpdatedAt),
		ExpiresAt:    formatTime(r.ExpiresAt),
	}

	if r.Status == db.StatusPending {
		out.NextAttemptAt = formatTime(r.NextAttemptAt)
	}

	return out
}

func formatAddress(a db.Address) string {
	if a.TypeOfNumber == tpdu.TypeOfNumberInternational {
		return "+" + a.Digits
	}

	return a.Digits
}

// timeParam parses the query parameter name, an RFC 3339 time, and returns the zero time when it is absent.
func timeParam(w http.ResponseWriter, value, name string, cfg Config) (time.Time, bool) {
	if value == "" {
		return time.Time{}, true
	}

	// The database stores times as Unix nanoseconds, which cover the years 1678 to 2262.
	t, err := time.Parse(time.RFC3339Nano, value)
	if err == nil && (t.Before(time.Unix(0, math.MinInt64)) || t.After(time.Unix(0, math.MaxInt64))) {
		err = errors.New("out of range")
	}

	if err != nil {
		writeError(w, http.StatusBadRequest, name+" must be an RFC 3339 time such as 2026-10-01T12:00:00Z", nil, cfg.Logger)
		return time.Time{}, false
	}

	return t, true
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func e164Digits(s string) (string, bool) {
	digits, ok := strings.CutPrefix(s, "+")
	if !ok || digits == "" || len(digits) > maxE164Digits || digits[0] == '0' {
		return "", false
	}

	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return "", false
		}
	}

	return digits, true
}

func atoiDefault(s string, def int) (int, bool) {
	if s == "" {
		return def, true
	}

	v, err := strconv.Atoi(s)

	return v, err == nil
}

func absentUserDiagnosticsOf(d db.AbsentUserDiagnostics) *AbsentUserDiagnostics {
	if d == (db.AbsentUserDiagnostics{}) {
		return nil
	}

	out := AbsentUserDiagnostics(d)

	return &out
}
