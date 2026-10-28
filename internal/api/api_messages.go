package api

import (
	"context"
	"encoding/hex"
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
	"github.com/ellanetworks/smsc/internal/statusreport"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

const (
	defaultPerPage = 25
	maxPerPage     = 100
	maxE164Digits  = 15
	// maxOriginatorName is the most characters of an alphanumeric originator (TS 23.040 clause 9.1.2.5).
	maxOriginatorName = 11
	// maxBinaryOctets is the most octets of 8-bit TP-UD (TS 23.038 clause 4).
	maxBinaryOctets = 140
	// attemptOutcomeSuccess is the outcome of a delivery attempt that the serving node acknowledged.
	attemptOutcomeSuccess = "success"
)

type CreateMessageParams struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Text and Binary are mutually exclusive.
	Text   string        `json:"text"`
	Binary *BinaryParams `json:"binary"`
}

// BinaryParams are the TPDU parameters of a short message that the caller codes itself, such as a (U)SIM data
// download message (TS 23.040 clause 9.2.3.9). The SC passes them on unchanged.
type BinaryParams struct {
	ProtocolIdentifier *uint8 `json:"protocol_identifier"`
	DataCodingScheme   *uint8 `json:"data_coding_scheme"`
	// UserDataHeader is the TP-UDH, its length octet included. It is nil when absent.
	UserDataHeader Hex `json:"user_data_header"`
	UserData       Hex `json:"user_data"`
}

// Hex is octets that JSON carries as a lowercase hexadecimal string.
type Hex []byte

func (h Hex) MarshalJSON() ([]byte, error) {
	return json.Marshal(hex.EncodeToString(h))
}

// UnmarshalJSON leaves h unchanged for null, which is then absent as an omitted value is.
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

// MessageContent is the content of a short message, which the other responses leave out: reading it is audited.
type MessageContent struct {
	// Text is the text of a message that is not binary.
	Text *string `json:"text,omitempty"`
	// Binary is the TPDU parameters of a binary message.
	Binary *BinaryContent `json:"binary,omitempty"`
	// DeliverReports are the SMS-DELIVER-REPORTs that the recipient answered with, whose user data is for the
	// originator (TS 23.040 clause 9.2.2.1a).
	DeliverReports []DeliverReport `json:"deliver_reports,omitempty"`
}

type BinaryContent struct {
	ProtocolIdentifier uint8 `json:"protocol_identifier"`
	DataCodingScheme   uint8 `json:"data_coding_scheme"`
	UserDataHeader     Hex   `json:"user_data_header,omitempty"`
	UserData           Hex   `json:"user_data"`
}

// DeliverReport is the SMS-DELIVER-REPORT of a delivery attempt: its octets, and its parameters when they decode.
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

		originator, ok := originatorOf(w, params.From, cfg)
		if !ok {
			return
		}

		to, ok := e164Digits(params.To)
		if !ok {
			writeError(w, http.StatusBadRequest, "to must be an E.164 number such as +15551230002", nil, cfg.Logger)
			return
		}

		switch {
		case params.Text != "" && params.Binary != nil:
			writeError(w, http.StatusBadRequest, "text and binary are mutually exclusive", nil, cfg.Logger)
			return
		case params.Text == "" && params.Binary == nil:
			writeError(w, http.StatusBadRequest, "text or binary is required", nil, cfg.Logger)
			return
		}

		destination := tpdu.Address{TypeOfNumber: tpdu.TypeOfNumberInternational, NumberingPlan: tpdu.NumberingPlanISDN, Digits: to}

		var submits []tpdu.Submit

		if params.Binary != nil {
			submit, msg := binarySubmit(*params.Binary)
			if msg != "" {
				writeError(w, http.StatusBadRequest, msg, nil, cfg.Logger)
				return
			}

			submit.Destination = destination
			submits = []tpdu.Submit{submit}
		} else {
			parts, _, err := tpdu.EncodeText(params.Text, uint8(reference.Add(1)))
			if errors.Is(err, tpdu.ErrTextTooLong) {
				writeError(w, http.StatusBadRequest, "text is too long", err, cfg.Logger)
				return
			}

			if err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to encode text", err, cfg.Logger)
				return
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
		}

		caller, _ := PrincipalFrom(r.Context())
		recipient := db.Address{Digits: to, TypeOfNumber: destination.TypeOfNumber, NumberingPlan: destination.NumberingPlan}
		now := cfg.Now()
		current := cfg.Settings.Get()

		messages := make([]db.NewMessage, 0, len(submits))

		for _, submit := range submits {
			b, err := submit.Encode()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to encode short message", err, cfg.Logger)
				return
			}

			m := db.NewMessage{
				Originator:         originator,
				Recipient:          recipient,
				MSISDN:             to,
				Origin:             db.OriginAPI,
				SubmittedBy:        caller.UserID,
				ProtocolIdentifier: submit.ProtocolIdentifier,
				TPDU:               b,
				SubmittedAt:        now,
				ExpiresAt:          now.Add(current.Delivery.DefaultValidity),
			}

			// The SC replaces a message from any source as it does one from an MS (TS 23.040 clause 9.2.3.9). A
			// replaced message from an MS still gets the status report it asked for.
			if tpdu.IsReplaceType(submit.ProtocolIdentifier) {
				m.Replace = true
				m.ReportReplaced = func(replaced db.Message) (*db.NewStatusReport, error) {
					return statusreport.For(replaced, tpdu.StatusReplaced, now, current)
				}
			}

			messages = append(messages, m)
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

// binarySubmit returns the SMS-SUBMIT, without its destination, of a binary message, or the reason that its parameters
// are invalid. The TP-UDL counts octets, so the TP-DCS must not code the GSM 7 bit default alphabet (TS 23.040 clause
// 9.2.3.16).
func binarySubmit(p BinaryParams) (tpdu.Submit, string) {
	if p.DataCodingScheme == nil {
		return tpdu.Submit{}, "binary.data_coding_scheme is required"
	}

	if p.UserData == nil {
		return tpdu.Submit{}, "binary.user_data is required"
	}

	var pid uint8
	if p.ProtocolIdentifier != nil {
		pid = *p.ProtocolIdentifier
	}

	dcs := *p.DataCodingScheme

	// The API takes octets: the TP-UDL of GSM 7 bit default alphabet counts septets (TS 23.040 clause 9.2.3.16), and
	// UCS2 user data reads back as text.
	if tpdu.IsSeptetCoded(dcs) || tpdu.IsUCS2(dcs) {
		return tpdu.Submit{}, "binary.data_coding_scheme must not code GSM 7-bit or UCS2; send text instead"
	}

	// The SC has no function to convert a message for a telematic device (TS 23.040 clause 9.2.3.9).
	if tpdu.RequestsTelematicInterworking(pid) {
		return tpdu.Submit{}, "binary.protocol_identifier requests telematic interworking, which is not supported"
	}

	// An ME handles a (U)SIM data download or ANSI-136 R-DATA message that is not class 2 as a normal message
	// (TS 23.040 clause 9.2.3.9), which would show the user what was meant for the (U)SIM.
	if class, ok := tpdu.MessageClass(dcs); (pid == tpdu.ProtocolUSIMDataDownload || pid == tpdu.ProtocolANSI136RData) &&
		(!ok || class != 2) {
		return tpdu.Submit{}, "binary.data_coding_scheme must be message class 2 for this protocol_identifier"
	}

	if p.UserDataHeader != nil && !tpdu.ValidUserDataHeader(p.UserDataHeader) {
		return tpdu.Submit{}, "binary.user_data_header is malformed"
	}

	// The application port addressing information element names the application that a device trigger is for
	// (TS 23.040 clause 9.2.3.9).
	if tpdu.IsDeviceTrigger(pid) && !tpdu.HasApplicationPort(p.UserDataHeader) {
		return tpdu.Submit{}, "binary.user_data_header must have an application port for this protocol_identifier"
	}

	ud := append(append([]byte{}, p.UserDataHeader...), p.UserData...)
	if len(ud) > maxBinaryOctets {
		return tpdu.Submit{}, "binary user data exceeds 140 octets"
	}

	return tpdu.Submit{
		UserDataHeader:     p.UserDataHeader != nil,
		ProtocolIdentifier: pid,
		DataCodingScheme:   dcs,
		UserDataLength:     uint8(len(ud)),
		UserData:           ud,
	}, ""
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

		out := contentOf(m)

		attempts, err := cfg.Store.ListDeliverReports(r.Context(), m.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to retrieve message", err, cfg.Logger)
			return
		}

		for _, attempt := range attempts {
			out.DeliverReports = append(out.DeliverReports, deliverReportOf(attempt))
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "read_message_content", p.Email, fmt.Sprintf("Content of message %d read", m.ID))

		writeResponse(w, out, http.StatusOK, cfg.Logger)
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

// deliverReportOf returns the deliver report of an attempt: for RP-ACK when the attempt succeeded, and for RP-ERROR
// otherwise. A report that does not decode has only its octets.
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
			originator, ok := originatorOf(w, from, cfg)
			if !ok {
				return
			}

			filter.Originator, filter.OriginatorTON = originator.Digits, &originator.TypeOfNumber
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

// originatorOf returns the originator a from value names: an E.164 number when it starts with '+', and an
// alphanumeric name (TS 23.040 clause 9.1.2.5) otherwise.
func originatorOf(w http.ResponseWriter, from string, cfg Config) (db.Address, bool) {
	if strings.HasPrefix(from, "+") {
		digits, ok := e164Digits(from)
		if !ok {
			writeError(w, http.StatusBadRequest, "from must be an E.164 number such as +15551230001", nil, cfg.Logger)
			return db.Address{}, false
		}

		return db.Address{Digits: digits, TypeOfNumber: tpdu.TypeOfNumberInternational, NumberingPlan: tpdu.NumberingPlanISDN}, true
	}

	if !isOriginatorName(from) {
		writeError(w, http.StatusBadRequest,
			"from must be an E.164 number such as +15551230001, or a name of 1 to 11 letters and digits, with single spaces between words", nil, cfg.Logger)

		return db.Address{}, false
	}

	return db.Address{Digits: from, TypeOfNumber: tpdu.TypeOfNumberAlphanumeric}, true
}

// isOriginatorName reports whether s is 1 to 11 ASCII letters and digits, with single spaces between words, not all
// digits. An all-digit value is a number, not a name.
func isOriginatorName(s string) bool {
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
