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

type DeliveryAttempt struct {
	ID          int64
	MessageID   int64
	AttemptedAt time.Time
	Step        AttemptStep
	Node        string
	Outcome     string
	ResultCode  *uint32
	VendorID    *uint32

	FailureCause      string
	TPFailureCause    string
	AbsentDiagnostic  string
	AbsentDiagnostics AbsentDiagnostics
}

type AbsentDiagnostics struct {
	MME         string
	MSC         string
	SGSN        string
	SMSF3GPP    string
	SMSFNon3GPP string
}

func (d *DB) CreateDeliveryAttempt(ctx context.Context, a DeliveryAttempt) (int64, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO delivery_attempts (message_id, attempted_at, step, node, outcome, result_code, vendor_id,
			failure_cause, tp_failure_cause, absent_diagnostic, absent_diagnostic_mme, absent_diagnostic_msc,
			absent_diagnostic_sgsn, absent_diagnostic_smsf_3gpp, absent_diagnostic_smsf_non_3gpp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.MessageID, a.AttemptedAt.UTC().UnixNano(), a.Step, a.Node, a.Outcome, nullable(a.ResultCode), nullable(a.VendorID),
		nullableString(a.FailureCause), nullableString(a.TPFailureCause), nullableString(a.AbsentDiagnostic),
		nullableString(a.AbsentDiagnostics.MME), nullableString(a.AbsentDiagnostics.MSC), nullableString(a.AbsentDiagnostics.SGSN),
		nullableString(a.AbsentDiagnostics.SMSF3GPP), nullableString(a.AbsentDiagnostics.SMSFNon3GPP))
	if err != nil {
		return 0, fmt.Errorf("create delivery attempt: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create delivery attempt: %w", err)
	}

	return id, nil
}

func (d *DB) ListDeliveryAttempts(ctx context.Context, messageID int64) ([]DeliveryAttempt, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT id, message_id, attempted_at, step, node, outcome, result_code, vendor_id,
			failure_cause, tp_failure_cause, absent_diagnostic, absent_diagnostic_mme, absent_diagnostic_msc,
			absent_diagnostic_sgsn, absent_diagnostic_smsf_3gpp, absent_diagnostic_smsf_non_3gpp
		FROM delivery_attempts WHERE message_id = ? ORDER BY id`,
		messageID)
	if err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var attempts []DeliveryAttempt

	for rows.Next() {
		var (
			a                                              DeliveryAttempt
			attemptedAt                                    int64
			resultCode, vendorID                           sql.Null[uint32]
			failureCause, tpFailureCause, absentDiagnostic sql.NullString
			absentMME, absentMSC, absentSGSN               sql.NullString
			absentSMSF3GPP, absentSMSFNon3GPP              sql.NullString
		)

		if err := rows.Scan(&a.ID, &a.MessageID, &attemptedAt, &a.Step, &a.Node, &a.Outcome, &resultCode, &vendorID,
			&failureCause, &tpFailureCause, &absentDiagnostic,
			&absentMME, &absentMSC, &absentSGSN, &absentSMSF3GPP, &absentSMSFNon3GPP); err != nil {
			return nil, fmt.Errorf("list delivery attempts: %w", err)
		}

		a.AttemptedAt = time.Unix(0, attemptedAt).UTC()
		a.ResultCode = pointer(resultCode)
		a.VendorID = pointer(vendorID)
		a.FailureCause = failureCause.String
		a.TPFailureCause = tpFailureCause.String
		a.AbsentDiagnostic = absentDiagnostic.String
		a.AbsentDiagnostics = AbsentDiagnostics{
			MME:         absentMME.String,
			MSC:         absentMSC.String,
			SGSN:        absentSGSN.String,
			SMSF3GPP:    absentSMSF3GPP.String,
			SMSFNon3GPP: absentSMSFNon3GPP.String,
		}
		attempts = append(attempts, a)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}

	return attempts, nil
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
