package db

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"

	"github.com/ellanetworks/smsc/internal/encryption"
)

const cipherIDLength = 16

func tpduAt(cipherID []byte, msisdn string, originator Address) encryption.Location {
	return encryption.Location{Table: "messages", Column: "tpdu", Row: fmt.Sprintf("%x/%d/%s/%d/%d/%s", cipherID,
		len(msisdn), msisdn, originator.TypeOfNumber, originator.NumberingPlan, originator.Digits)}
}

func deliverReportAt(cipherID []byte, messageID int64) encryption.Location {
	return encryption.Location{Table: "delivery_attempts", Column: "deliver_report", Row: fmt.Sprintf("%x/%d", cipherID,
		messageID)}
}

func (d *DB) sealContent(plaintext []byte, at func(cipherID []byte) encryption.Location) ([]byte, []byte, error) {
	if !d.unlocked() {
		return nil, nil, ErrKeysLocked
	}

	cipherID := make([]byte, cipherIDLength)
	if _, err := rand.Read(cipherID); err != nil {
		return nil, nil, err
	}

	sealed, err := d.keys.Encrypt(plaintext, at(cipherID))
	if err != nil {
		return nil, nil, err
	}

	return sealed, cipherID, nil
}

func (d *DB) openContent(stored, cipherID []byte, at encryption.Location) ([]byte, error) {
	if cipherID == nil {
		return nil, fmt.Errorf("%w: no cipher ID", encryption.ErrNotEncrypted)
	}

	if !d.unlocked() {
		return nil, ErrKeysLocked
	}

	return d.keys.Decrypt(stored, at)
}

func (d *DB) sealMessage(r *messageRecord) error {
	sealed, cipherID, err := d.sealContent(r.TPDU, func(id []byte) encryption.Location {
		return tpduAt(id, r.MSISDN, r.Originator)
	})
	if err != nil {
		return fmt.Errorf("encrypt the message: %w", err)
	}

	r.TPDU, r.CipherID = sealed, cipherID

	return nil
}

func (d *DB) openMessage(m *Message) error {
	if m.Kind != KindMessage {
		return nil
	}

	tpdu, err := d.openContent(m.TPDU, m.CipherID, tpduAt(m.CipherID, m.MSISDN, m.Originator))
	if err != nil {
		return fmt.Errorf("decrypt message %d: %w", m.ID, err)
	}

	m.TPDU, m.CipherID = tpdu, nil

	return nil
}

func (d *DB) sealAttempts(plain []DeliveryAttempt) ([]DeliveryAttempt, error) {
	attempts := slices.Clone(plain)

	for i := range attempts {
		a := &attempts[i]
		a.CipherID = nil

		if a.DeliverReport == nil {
			continue
		}

		sealed, cipherID, err := d.sealContent(a.DeliverReport, func(id []byte) encryption.Location {
			return deliverReportAt(id, a.MessageID)
		})
		if err != nil {
			return nil, fmt.Errorf("encrypt the deliver report: %w", err)
		}

		a.DeliverReport, a.CipherID = sealed, cipherID
	}

	return attempts, nil
}

func (d *DB) openAttempt(a *DeliveryAttempt) error {
	if a.DeliverReport == nil {
		return nil
	}

	report, err := d.openContent(a.DeliverReport, a.CipherID, deliverReportAt(a.CipherID, a.MessageID))
	if err != nil {
		return fmt.Errorf("decrypt the deliver report of attempt %d: %w", a.ID, err)
	}

	a.DeliverReport, a.CipherID = report, nil

	return nil
}

func (d *DB) openNotification(n *Notification) error {
	if err := d.openMessageOrLeaveOut(&n.Message); err != nil {
		return err
	}

	if n.DeliverReport == nil {
		return nil
	}

	report, err := d.openContent(n.DeliverReport, n.ReportCipherID, deliverReportAt(n.ReportCipherID, n.Message.ID))
	if err != nil && d.leaveOut(err) {
		n.DeliverReport, n.ReportCipherID = nil, nil

		return nil
	}

	if err != nil {
		return fmt.Errorf("decrypt the deliver report of notification %d: %w", n.ID, err)
	}

	n.DeliverReport, n.ReportCipherID = report, nil

	return nil
}

func undecryptable(err error) bool {
	return errors.Is(err, encryption.ErrAuthentication) || errors.Is(err, encryption.ErrMalformed) ||
		errors.Is(err, encryption.ErrNotEncrypted) || errors.Is(err, encryption.ErrUnsupportedVersion)
}

func (d *DB) leaveOut(err error) bool {
	if !undecryptable(err) {
		return false
	}

	if d.metrics.undecryptable != nil {
		d.metrics.undecryptable.Inc()
	}

	return true
}

func (d *DB) openMessageOrLeaveOut(m *Message) error {
	err := d.openMessage(m)
	if err != nil && d.leaveOut(err) {
		m.TPDU, m.CipherID = nil, nil

		return nil
	}

	return err
}

func requireCipherID(cipherID []byte, what string) error {
	if len(cipherID) != cipherIDLength {
		return fmt.Errorf("%w: %s has a cipher ID of %d bytes, want %d", ErrInvalid, what, len(cipherID), cipherIDLength)
	}

	return nil
}
