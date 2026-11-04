package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

const (
	defaultPerPage        = 25
	maxPerPage            = 100
	maxE164Digits         = 15
	attemptOutcomeSuccess = "success"
)

type CreateMessageParams struct {
	From            string        `json:"from"`
	To              string        `json:"to"`
	Text            string        `json:"text"`
	Binary          *BinaryParams `json:"binary"`
	ValiditySeconds *int64        `json:"validity_seconds"`
}

type BinaryParams struct {
	ProtocolIdentifier *uint8 `json:"protocol_identifier"`
	DataCodingScheme   *uint8 `json:"data_coding_scheme"`
	UserDataHeader     Hex    `json:"user_data_header"`
	UserData           Hex    `json:"user_data"`
}

type Hex []byte

func (h Hex) MarshalJSON() ([]byte, error) {
	return json.Marshal(hex.EncodeToString(h))
}

func (h *Hex) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}

	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}

	v, err := hex.DecodeString(s)
	if err != nil {
		return err
	}

	*h = v

	return nil
}

type MessageContent struct {
	Text           *string         `json:"text,omitempty"`
	Binary         *BinaryContent  `json:"binary,omitempty"`
	DeliverReports []DeliverReport `json:"deliver_reports,omitempty"`
}

type BinaryContent struct {
	ProtocolIdentifier uint8 `json:"protocol_identifier"`
	DataCodingScheme   uint8 `json:"data_coding_scheme"`
	UserDataHeader     Hex   `json:"user_data_header,omitempty"`
	UserData           Hex   `json:"user_data"`
}

type DeliverReport struct {
	AttemptID          int64  `json:"attempt_id"`
	TPDU               Hex    `json:"tpdu"`
	ProtocolIdentifier *uint8 `json:"protocol_identifier,omitempty"`
	DataCodingScheme   *uint8 `json:"data_coding_scheme,omitempty"`
	UserDataHeader     Hex    `json:"user_data_header,omitempty"`
	UserData           Hex    `json:"user_data,omitempty"`
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
	SubmittedBy   string         `json:"submitted_by,omitempty"`
	SystemID      string         `json:"system_id,omitempty"`
}

// TS 23.040 clause 3.2.9
type StatusReports struct {
	Requested bool           `json:"requested"`
	Reports   []StatusReport `json:"reports"`
}

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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params CreateMessageParams

		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeError(w, r, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
			return
		}

		originator, ok := originatorOf(w, r, params.From, cfg)
		if !ok {
			return
		}

		to, ok := e164Digits(params.To)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "to must be an E.164 number such as +15551230002", nil, cfg.Logger)
			return
		}

		switch {
		case params.Text != "" && params.Binary != nil:
			writeError(w, r, http.StatusBadRequest, "text and binary are mutually exclusive", nil, cfg.Logger)
			return
		case params.Text == "" && params.Binary == nil:
			writeError(w, r, http.StatusBadRequest, "text or binary is required", nil, cfg.Logger)
			return
		}

		if params.ValiditySeconds != nil && !validValidity(*params.ValiditySeconds) {
			writeError(w, r, http.StatusBadRequest, fmt.Sprintf("validity_seconds must be between 1 and %d", maxValiditySeconds), nil, cfg.Logger)
			return
		}

		caller, _ := PrincipalFrom(r.Context())

		req := submission.Request{
			Originator:  originator,
			Recipient:   db.Address{Digits: to, TypeOfNumber: tpdu.TypeOfNumberInternational, NumberingPlan: tpdu.NumberingPlanISDN},
			MSISDN:      to,
			Origin:      db.OriginAPI,
			SubmittedBy: caller.UserID,
		}

		if params.Binary != nil {
			data, msg := binaryUserData(*params.Binary)
			if msg != "" {
				writeError(w, r, http.StatusBadRequest, msg, nil, cfg.Logger)
				return
			}

			req.Data = &data
		} else {
			req.Text = &params.Text
		}

		if params.ValiditySeconds != nil {
			req.ExpiresAt = cfg.Now().Add(time.Duration(*params.ValiditySeconds) * time.Second)
		}

		ids, err := cfg.Submitter.Submit(r.Context(), req)
		if err != nil {
			status, msg := submitError(err)
			writeError(w, r, status, msg, err, cfg.Logger)

			return
		}

		cfg.Notify()

		stored := make([]db.Message, 0, len(ids))

		for _, id := range ids {
			m, err := cfg.Store.GetMessage(r.Context(), id)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
				return
			}

			stored = append(stored, m)
		}

		items, err := messagesOf(r.Context(), cfg, stored)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
			return
		}

		writeResponse(w, r, CreateMessageResponse{Items: items}, http.StatusCreated, cfg.Logger)
	})
}

// TS 23.040 clause 9.2.3.16
func binaryUserData(p BinaryParams) (submission.UserData, string) {
	if p.DataCodingScheme == nil {
		return submission.UserData{}, "binary.data_coding_scheme is required"
	}

	if p.UserData == nil {
		return submission.UserData{}, "binary.user_data is required"
	}

	var pid uint8
	if p.ProtocolIdentifier != nil {
		pid = *p.ProtocolIdentifier
	}

	dcs := *p.DataCodingScheme

	if tpdu.IsSeptetCoded(dcs) || tpdu.IsUCS2(dcs) {
		return submission.UserData{}, "binary.data_coding_scheme must not code GSM 7-bit or UCS2; send text instead"
	}

	var header []byte
	if p.UserDataHeader != nil {
		header = []byte(p.UserDataHeader)
	}

	return submission.UserData{PID: pid, DCS: dcs, Header: header, Octets: p.UserData}, ""
}

func submitError(err error) (int, string) {
	switch {
	case errors.Is(err, submission.ErrTelematicInterworking):
		return http.StatusBadRequest, "binary.protocol_identifier requests telematic interworking, which is not supported"
	case errors.Is(err, submission.ErrNotClass2):
		return http.StatusBadRequest, "binary.data_coding_scheme must be message class 2 for this protocol_identifier"
	case errors.Is(err, submission.ErrUserDataHeader):
		return http.StatusBadRequest, "binary.user_data_header is malformed"
	case errors.Is(err, submission.ErrNoApplicationPort):
		return http.StatusBadRequest, "binary.user_data_header must have an application port for this protocol_identifier"
	case errors.Is(err, submission.ErrUserDataTooLong):
		return http.StatusBadRequest, "binary user data exceeds 140 octets"
	case errors.Is(err, submission.ErrTextTooLong):
		return http.StatusBadRequest, "text is too long"
	case errors.Is(err, submission.ErrEncodeText):
		return http.StatusInternalServerError, "Failed to encode text"
	case errors.Is(err, submission.ErrStore):
		return http.StatusInternalServerError, "Failed to store message"
	default:
		return http.StatusInternalServerError, "Failed to encode short message"
	}
}

func GetMessage(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, ok := lookupMessage(w, r, cfg)
		if !ok {
			return
		}

		items, err := messagesOf(r.Context(), cfg, []db.Message{m})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
			return
		}

		writeResponse(w, r, items[0], http.StatusOK, cfg.Logger)
	})
}

func GetMessageContent(cfg Config, a *auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, ok := lookupMessage(w, r, cfg)
		if !ok {
			return
		}

		out := contentOf(m)

		attempts, err := cfg.Store.ListDeliverReports(r.Context(), m.ID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
			return
		}

		for _, attempt := range attempts {
			out.DeliverReports = append(out.DeliverReports, deliverReportOf(attempt))
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "read_message_content", p.Email, fmt.Sprintf("Content of message %d read", m.ID))

		writeResponse(w, r, out, http.StatusOK, cfg.Logger)
	})
}

func contentOf(m db.Message) MessageContent {
	var out MessageContent

	submit, err := tpdu.DecodeSubmit(m.TPDU)
	if err != nil {
		return out
	}

	content, err := submit.Content()
	if err != nil {
		return out
	}

	if content.Encoding != tpdu.EncodingBinary {
		out.Text = &content.Text
		return out
	}

	header, rest, err := tpdu.SplitUserData(submit.UserDataHeader, submit.UserData)
	if err != nil {
		return out
	}

	out.Binary = &BinaryContent{
		ProtocolIdentifier: submit.ProtocolIdentifier,
		DataCodingScheme:   submit.DataCodingScheme,
		UserDataHeader:     header,
		UserData:           rest,
	}

	return out
}

func deliverReportOf(a db.DeliveryAttempt) DeliverReport {
	out := DeliverReport{AttemptID: a.ID, TPDU: a.DeliverReport}

	r, err := tpdu.DecodeDeliverReport(a.DeliverReport, a.Outcome == attemptOutcomeSuccess)
	if err != nil {
		return out
	}

	out.ProtocolIdentifier, out.DataCodingScheme = r.ProtocolIdentifier, r.DataCodingScheme

	if r.UserDataLength != nil {
		header, rest, err := tpdu.SplitUserData(r.UserDataHeader, r.UserData)
		if err != nil {
			return out
		}

		out.UserDataHeader, out.UserData = header, rest
	}

	return out
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
			writeError(w, r, http.StatusInternalServerError, "Failed to list delivery attempts", err, cfg.Logger)
			return
		}

		resp := ListAttemptsResponse{Items: make([]Attempt, 0, len(attempts)), Page: page, PerPage: perPage, TotalCount: total}

		for _, a := range attempts {
			resp.Items = append(resp.Items, attemptOf(a))
		}

		writeResponse(w, r, resp, http.StatusOK, cfg.Logger)
	})
}

func ownSubmissions(ctx context.Context) int64 {
	if p, _ := PrincipalFrom(ctx); p.Role == db.RoleSubmitter {
		return p.UserID
	}

	return 0
}

func visible(ctx context.Context, m db.Message) bool {
	only := ownSubmissions(ctx)
	return only == 0 || m.SubmittedBy == only
}

func lookupMessage(w http.ResponseWriter, r *http.Request, cfg Config) (db.Message, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid message ID", err, cfg.Logger)
		return db.Message{}, false
	}

	m, err := cfg.Store.GetMessage(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) || err == nil && (m.Kind != db.KindMessage || !visible(r.Context(), m)) {
		writeError(w, r, http.StatusNotFound, "Message not found", err, cfg.Logger)
		return db.Message{}, false
	}

	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
		return db.Message{}, false
	}

	return m, true
}

func pagination(w http.ResponseWriter, r *http.Request, cfg Config) (int, int, bool) {
	q := r.URL.Query()

	page, ok := atoiDefault(q.Get("page"), 1)
	if !ok || page < 1 {
		writeError(w, r, http.StatusBadRequest, "page must be an integer >= 1", nil, cfg.Logger)
		return 0, 0, false
	}

	perPage, ok := atoiDefault(q.Get("per_page"), defaultPerPage)
	if !ok || perPage < 1 || perPage > maxPerPage {
		writeError(w, r, http.StatusBadRequest, "per_page must be an integer between 1 and 100", nil, cfg.Logger)
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
			if filter.MSISDN, ok = partialNumber(to); !ok {
				writeError(w, r, http.StatusBadRequest, "to must be all or part of an E.164 number, such as +1555123 or 1230002", nil,
					cfg.Logger)

				return
			}
		}

		if from := q.Get("from"); from != "" {
			if digits, isNumber := partialNumber(from); isNumber {
				ton := uint8(tpdu.TypeOfNumberInternational)
				filter.Originator, filter.OriginatorContains, filter.OriginatorTON = digits, true, &ton
			} else if submission.IsOriginatorName(from) {
				ton := uint8(tpdu.TypeOfNumberAlphanumeric)
				filter.Originator, filter.OriginatorTON = from, &ton
			} else {
				writeError(w, r, http.StatusBadRequest,
					"from must be all or part of an E.164 number, such as +1555123 or 1230001, or a name of 1 to 11 letters and digits, with single spaces between words",
					nil, cfg.Logger)

				return
			}
		}

		if status := db.MessageStatus(q.Get("status")); status != "" {
			switch status {
			case db.StatusPending, db.StatusDelivered, db.StatusFailed, db.StatusExpired, db.StatusReplaced:
				filter.Status = status
			default:
				writeError(w, r, http.StatusBadRequest, "status must be one of pending, delivered, failed, expired, replaced", nil,
					cfg.Logger)

				return
			}
		}

		if filter.Start, filter.End, ok = timeRange(w, r, cfg); !ok {
			return
		}

		messages, total, err := cfg.Store.ListMessages(r.Context(), filter, page, perPage)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to list messages", err, cfg.Logger)
			return
		}

		items, err := messagesOf(r.Context(), cfg, messages)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to list messages", err, cfg.Logger)
			return
		}

		writeResponse(w, r, ListMessagesResponse{Items: items, Page: page, PerPage: perPage, TotalCount: total}, http.StatusOK,
			cfg.Logger)
	})
}

type MessageCounts struct {
	Pending   int `json:"pending"`
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
	Expired   int `json:"expired"`
	Replaced  int `json:"replaced"`
}

func CountMessages(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filter := db.MessageFilter{SubmittedBy: ownSubmissions(r.Context())}

		var ok bool
		if filter.Start, filter.End, ok = timeRange(w, r, cfg); !ok {
			return
		}

		counts, err := cfg.Store.CountMessages(r.Context(), filter)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to count messages", err, cfg.Logger)
			return
		}

		writeResponse(w, r, MessageCounts{
			Pending:   counts[db.StatusPending],
			Delivered: counts[db.StatusDelivered],
			Failed:    counts[db.StatusFailed],
			Expired:   counts[db.StatusExpired],
			Replaced:  counts[db.StatusReplaced],
		}, http.StatusOK, cfg.Logger)
	})
}

func messagesOf(ctx context.Context, cfg Config, messages []db.Message) ([]Message, error) {
	ids := make([]int64, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.ID)
	}

	reports, err := cfg.Store.StatusReportsFor(ctx, ids)
	if err != nil {
		return nil, err
	}

	systemIDs, err := cfg.Store.SystemIDsFor(ctx, ids)
	if err != nil {
		return nil, err
	}

	emails := map[int64]string{}

	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		msg := messageOf(m, reports[m.ID])
		msg.SystemID = systemIDs[m.ID]

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

// RFC 3339
func timeRange(w http.ResponseWriter, r *http.Request, cfg Config) (time.Time, time.Time, bool) {
	q := r.URL.Query()

	start, ok := timeParam(w, r, q.Get("start"), "start", cfg)
	if !ok {
		return time.Time{}, time.Time{}, false
	}

	end, ok := timeParam(w, r, q.Get("end"), "end", cfg)
	if !ok {
		return time.Time{}, time.Time{}, false
	}

	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		writeError(w, r, http.StatusBadRequest, "end must not be before start", nil, cfg.Logger)
		return time.Time{}, time.Time{}, false
	}

	return start, end, true
}

func timeParam(w http.ResponseWriter, r *http.Request, value, name string, cfg Config) (time.Time, bool) {
	if value == "" {
		return time.Time{}, true
	}

	t, err := time.Parse(time.RFC3339Nano, value)
	if err == nil && (t.Before(time.Unix(0, math.MinInt64)) || t.After(time.Unix(0, math.MaxInt64))) {
		err = errors.New("out of range")
	}

	if err != nil {
		writeError(w, r, http.StatusBadRequest, name+" must be an RFC 3339 time such as 2026-10-01T12:00:00Z", nil, cfg.Logger)
		return time.Time{}, false
	}

	return t, true
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// TS 23.040 clause 9.1.2.5
func originatorOf(w http.ResponseWriter, r *http.Request, from string, cfg Config) (db.Address, bool) {
	if strings.HasPrefix(from, "+") {
		digits, ok := e164Digits(from)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "from must be an E.164 number such as +15551230001", nil, cfg.Logger)
			return db.Address{}, false
		}

		return db.Address{Digits: digits, TypeOfNumber: tpdu.TypeOfNumberInternational, NumberingPlan: tpdu.NumberingPlanISDN}, true
	}

	if !submission.IsOriginatorName(from) {
		writeError(w, r, http.StatusBadRequest,
			"from must be an E.164 number such as +15551230001, or a name of 1 to 11 letters and digits, with single spaces between words", nil, cfg.Logger)

		return db.Address{}, false
	}

	return db.Address{Digits: from, TypeOfNumber: tpdu.TypeOfNumberAlphanumeric}, true
}

func partialNumber(s string) (string, bool) {
	digits := strings.TrimPrefix(s, "+")
	if digits == "" || len(digits) > maxE164Digits {
		return "", false
	}

	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return "", false
		}
	}

	return digits, true
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
