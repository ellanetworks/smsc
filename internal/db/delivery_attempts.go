package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type AttemptStep string

const (
	StepRouting  AttemptStep = "routing"
	StepDelivery AttemptStep = "delivery"
)

type NodeType string

const (
	NodeTypeMME         NodeType = "mme"
	NodeTypeSGSN        NodeType = "sgsn"
	NodeTypeSMSF3GPP    NodeType = "smsf_3gpp"
	NodeTypeSMSFNon3GPP NodeType = "smsf_non_3gpp"
)

type DeliveryAttempt struct {
	ID          int64
	MessageID   int64
	StartedAt   time.Time
	CompletedAt time.Time
	Step        AttemptStep
	Node        string
	NodeType    NodeType
	Outcome     string
	ResultCode  *uint32
	VendorID    *uint32

	FailureCause          string
	TPFailureCause        string
	AbsentUserDiagnostics AbsentUserDiagnostics
}

type AbsentUserDiagnostics struct {
	MME         string
	MSC         string
	SGSN        string
	SMSF3GPP    string
	SMSFNon3GPP string
}

const deliveryAttemptColumns = `id, message_id, started_at, completed_at, step, node, node_type, outcome, result_code, vendor_id,
	failure_cause, tp_failure_cause, absent_user_diagnostic_mme, absent_user_diagnostic_msc,
	absent_user_diagnostic_sgsn, absent_user_diagnostic_smsf_3gpp, absent_user_diagnostic_smsf_non_3gpp`

func (d *DB) CreateDeliveryAttempt(ctx context.Context, a DeliveryAttempt) (int64, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO delivery_attempts (message_id, started_at, completed_at, step, node, node_type, outcome, result_code, vendor_id,
			failure_cause, tp_failure_cause, absent_user_diagnostic_mme, absent_user_diagnostic_msc,
			absent_user_diagnostic_sgsn, absent_user_diagnostic_smsf_3gpp, absent_user_diagnostic_smsf_non_3gpp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.MessageID, a.StartedAt.UTC().UnixNano(), a.CompletedAt.UTC().UnixNano(), a.Step, a.Node,
		nullableString(string(a.NodeType)), a.Outcome, nullable(a.ResultCode), nullable(a.VendorID),
		nullableString(a.FailureCause), nullableString(a.TPFailureCause),
		nullableString(a.AbsentUserDiagnostics.MME), nullableString(a.AbsentUserDiagnostics.MSC), nullableString(a.AbsentUserDiagnostics.SGSN),
		nullableString(a.AbsentUserDiagnostics.SMSF3GPP), nullableString(a.AbsentUserDiagnostics.SMSFNon3GPP))
	if err != nil {
		return 0, fmt.Errorf("create delivery attempt: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create delivery attempt: %w", err)
	}

	return id, nil
}

func (d *DB) ListDeliveryAttempts(ctx context.Context, messageID int64, page, perPage int) ([]DeliveryAttempt, int, error) {
	var total int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_attempts WHERE message_id = ?`, messageID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list delivery attempts: %w", err)
	}

	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+deliveryAttemptColumns+` FROM delivery_attempts WHERE message_id = ? ORDER BY id LIMIT ? OFFSET ?`,
		messageID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list delivery attempts: %w", err)
	}

	defer func() { _ = rows.Close() }()

	attempts := []DeliveryAttempt{}

	for rows.Next() {
		a, err := scanDeliveryAttempt(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list delivery attempts: %w", err)
		}

		attempts = append(attempts, a)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list delivery attempts: %w", err)
	}

	return attempts, total, nil
}

func scanDeliveryAttempt(rows *sql.Rows) (DeliveryAttempt, error) {
	var (
		a                                 DeliveryAttempt
		startedAt, completedAt            int64
		nodeType                          sql.NullString
		resultCode, vendorID              sql.Null[uint32]
		failureCause, tpFailureCause      sql.NullString
		absentMME, absentMSC, absentSGSN  sql.NullString
		absentSMSF3GPP, absentSMSFNon3GPP sql.NullString
	)

	if err := rows.Scan(&a.ID, &a.MessageID, &startedAt, &completedAt, &a.Step, &a.Node, &nodeType, &a.Outcome,
		&resultCode, &vendorID, &failureCause, &tpFailureCause,
		&absentMME, &absentMSC, &absentSGSN, &absentSMSF3GPP, &absentSMSFNon3GPP); err != nil {
		return DeliveryAttempt{}, err
	}

	a.StartedAt = time.Unix(0, startedAt).UTC()
	a.CompletedAt = time.Unix(0, completedAt).UTC()
	a.NodeType = NodeType(nodeType.String)
	a.ResultCode = pointer(resultCode)
	a.VendorID = pointer(vendorID)
	a.FailureCause = failureCause.String
	a.TPFailureCause = tpFailureCause.String
	a.AbsentUserDiagnostics = AbsentUserDiagnostics{
		MME:         absentMME.String,
		MSC:         absentMSC.String,
		SGSN:        absentSGSN.String,
		SMSF3GPP:    absentSMSF3GPP.String,
		SMSFNon3GPP: absentSMSFNon3GPP.String,
	}

	return a, nil
}

func nullable(v *uint32) sql.Null[uint32] {
	if v == nil {
		return sql.Null[uint32]{}
	}

	return sql.Null[uint32]{V: *v, Valid: true}
}

func pointer(v sql.Null[uint32]) *uint32 {
	if !v.Valid {
		return nil
	}

	return &v.V
}

func nullableString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
