package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/smsc/internal/settings"
)

var (
	ErrTokenUsed    = errors.New("the join token was already used")
	ErrTokenExpired = errors.New("the join token expired")
)

type Member struct {
	NodeID        string    `json:"node_id"`
	Address       string    `json:"address"`
	Fingerprint   string    `json:"fingerprint,omitempty"`
	Certificate   string    `json:"certificate,omitempty"`
	DiameterLabel string    `json:"diameter_label,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	Version       string    `json:"version"`
	JoinedAt      time.Time `json:"joined_at"`
}

func memberLabels(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT diameter_label FROM cluster_members ORDER BY diameter_label`)
	if err != nil {
		return nil, fmt.Errorf("get diameter labels: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var labels []string

	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, fmt.Errorf("get diameter labels: %w", err)
		}

		labels = append(labels, l)
	}

	return labels, rows.Err()
}

const memberColumns = `node_id, address, fingerprint, certificate, diameter_label, schema_version, version, joined_at`

func scanMember(scan func(...any) error) (Member, error) {
	var (
		m           Member
		fingerprint sql.NullString
		certificate sql.NullString
		joined      int64
	)

	if err := scan(&m.NodeID, &m.Address, &fingerprint, &certificate, &m.DiameterLabel, &m.SchemaVersion, &m.Version,
		&joined); err != nil {
		return Member{}, err
	}

	m.Fingerprint, m.Certificate, m.JoinedAt = fingerprint.String, certificate.String, time.Unix(0, joined).UTC()

	return m, nil
}

func (d *DB) Members(ctx context.Context) (_ []Member, err error) {
	defer d.observe(&err)()

	return members(ctx, d.reader())
}

func members(ctx context.Context, q querier) ([]Member, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+memberColumns+` FROM cluster_members ORDER BY joined_at, node_id`)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []Member

	for rows.Next() {
		m, err := scanMember(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("list members: %w", err)
		}

		out = append(out, m)
	}

	return out, rows.Err()
}

func (d *DB) Member(ctx context.Context, nodeID string) (_ Member, err error) {
	defer d.observe(&err)()

	return member(ctx, d.reader(), nodeID)
}

func member(ctx context.Context, q querier, nodeID string) (Member, error) {
	m, err := scanMember(q.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM cluster_members WHERE node_id = ?`,
		nodeID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, fmt.Errorf("member %s: %w", nodeID, ErrNotFound)
	}

	return m, err
}

func (d *DB) RegisterMember(ctx context.Context, m Member) error {
	return d.RegisterMemberVia(ctx, d.proposer, m)
}

func (d *DB) RegisterMemberVia(ctx context.Context, via Proposer, m Member) (err error) {
	defer d.observe(&err)()

	_, _, err = submitVia[struct{}](ctx, via, cmdRegisterMember, m)

	return err
}

func applyRegisterMember(a *applyCtx, m Member) (struct{}, error) {
	existing, err := member(a.ctx, a.tx, m.NodeID)

	switch {
	case errors.Is(err, ErrNotFound):
		return struct{}{}, insertMember(a, m)
	case err != nil:
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE cluster_members SET address = ?, fingerprint = ?, certificate = ?,
		schema_version = ?, version = ? WHERE node_id = ?`, m.Address, nullString(m.Fingerprint),
		nullString(m.Certificate), m.SchemaVersion, m.Version, m.NodeID); err != nil {
		if isUnique(err) {
			return struct{}{}, fmt.Errorf("%w: another member has the address %s or the certificate", settings.ErrConflict,
				m.Address)
		}

		return struct{}{}, err
	}

	if existing.Fingerprint != m.Fingerprint || existing.Address != m.Address {
		a.events.Members = true
	}

	return struct{}{}, nil
}

func insertMember(a *applyCtx, m Member) error {
	label, err := freeLabel(a)
	if err != nil {
		return err
	}

	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO cluster_members (`+memberColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.NodeID, m.Address, nullString(m.Fingerprint), nullString(m.Certificate), label, m.SchemaVersion, m.Version,
		a.leaderTime); err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: another member has the address %s or the certificate", settings.ErrConflict, m.Address)
		}

		return err
	}

	a.events.Members = true
	a.events.Settings = true

	return nil
}

func freeLabel(a *applyCtx) (string, error) {
	s, err := getSettings(a.ctx, a.tx)
	if err != nil {
		return "", err
	}

	taken := func(label string) bool {
		if s.Operator.Conflict(s.Peers, []string{label}) != nil {
			return true
		}

		for _, l := range s.Labels {
			if l == label {
				return true
			}
		}

		return false
	}

	if !taken(settings.DefaultLabel) {
		return settings.DefaultLabel, nil
	}

	for k := 2; ; k++ {
		if label := settings.DefaultLabel + strconv.Itoa(k); !taken(label) {
			return label, nil
		}
	}
}

type joinToken struct {
	TokenID   string `json:"token_id"`
	ExpiresAt int64  `json:"expires_at"`
}

func (d *DB) CreateJoinToken(ctx context.Context, tokenID string, expiresAt time.Time) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdCreateJoinToken, joinToken{TokenID: tokenID, ExpiresAt: expiresAt.UnixNano()})

	return err
}

func applyCreateJoinToken(a *applyCtx, t joinToken) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `INSERT INTO cluster_join_tokens (token_id, expires_at) VALUES (?, ?)`,
		t.TokenID, t.ExpiresAt)
	if isUnique(err) {
		return struct{}{}, fmt.Errorf("join token %s: %w", t.TokenID, ErrAlreadyExists)
	}

	return struct{}{}, err
}

type admitMember struct {
	TokenID string `json:"token_id"`
	Member  Member `json:"member"`
}

func (d *DB) AdmitMember(ctx context.Context, tokenID string, m Member) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdAdmitMember, admitMember{TokenID: tokenID, Member: m})

	return err
}

func applyAdmitMember(a *applyCtx, p admitMember) (struct{}, error) {
	var (
		expiresAt   int64
		nodeID      sql.NullString
		fingerprint sql.NullString
	)

	err := a.tx.QueryRowContext(a.ctx, `SELECT expires_at, node_id, fingerprint FROM cluster_join_tokens
		WHERE token_id = ?`, p.TokenID).Scan(&expiresAt, &nodeID, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return struct{}{}, fmt.Errorf("join token %s: %w", p.TokenID, ErrNotFound)
	}

	if err != nil {
		return struct{}{}, err
	}

	m := p.Member

	if nodeID.Valid {
		if nodeID.String != m.NodeID || fingerprint.String != m.Fingerprint {
			return struct{}{}, fmt.Errorf("join token %s: %w", p.TokenID, ErrTokenUsed)
		}

		return applyRegisterMember(a, m)
	}

	if a.leaderTime > expiresAt {
		return struct{}{}, fmt.Errorf("join token %s: %w", p.TokenID, ErrTokenExpired)
	}

	if _, err := member(a.ctx, a.tx, m.NodeID); err == nil {
		return struct{}{}, fmt.Errorf("%w: node %s is already a member", settings.ErrConflict, m.NodeID)
	} else if !errors.Is(err, ErrNotFound) {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE cluster_join_tokens SET node_id = ?, fingerprint = ? WHERE token_id = ?`,
		m.NodeID, m.Fingerprint, p.TokenID); err != nil {
		return struct{}{}, err
	}

	return struct{}{}, insertMember(a, m)
}

func (d *DB) RemoveMember(ctx context.Context, nodeID string) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdRemoveMember, nodeID)

	return err
}

func applyRemoveMember(a *applyCtx, nodeID string) (struct{}, error) {
	res, err := a.tx.ExecContext(a.ctx, `DELETE FROM cluster_members WHERE node_id = ?`, nodeID)
	if err != nil {
		return struct{}{}, err
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return struct{}{}, fmt.Errorf("member %s: %w", nodeID, ErrNotFound)
	}

	a.events.Members = true
	a.events.Settings = true
	a.events.Removed = append(a.events.Removed, nodeID)

	return struct{}{}, nil
}

type diameterLabel struct {
	NodeID string `json:"node_id"`
	Label  string `json:"label"`
}

func (d *DB) SetDiameterLabel(ctx context.Context, nodeID, label string) (err error) {
	defer d.observe(&err)()

	if !validLabel(label) {
		return fmt.Errorf("%w: the diameter label must be 1 to 63 lowercase letters, digits or hyphens, "+
			"not starting or ending with a hyphen", ErrInvalid)
	}

	_, err = submit[struct{}](ctx, d, cmdSetDiameterLabel, diameterLabel{NodeID: nodeID, Label: label})

	return err
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}

	return strings.Trim(l, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
}

func applySetDiameterLabel(a *applyCtx, p diameterLabel) (struct{}, error) {
	if !validLabel(p.Label) {
		return struct{}{}, fmt.Errorf("%w: diameter label %q", ErrInvalid, p.Label)
	}

	s, err := getSettings(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	if err := s.Operator.Conflict(s.Peers, []string{p.Label}); err != nil {
		return struct{}{}, err
	}

	res, err := a.tx.ExecContext(a.ctx, `UPDATE cluster_members SET diameter_label = ? WHERE node_id = ?`, p.Label,
		p.NodeID)
	if isUnique(err) {
		return struct{}{}, fmt.Errorf("%w: another member has the diameter label %s", settings.ErrConflict, p.Label)
	}

	if err != nil {
		return struct{}{}, err
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return struct{}{}, fmt.Errorf("member %s: %w", p.NodeID, ErrNotFound)
	}

	a.events.Members = true
	a.events.Settings = true

	return struct{}{}, nil
}

func (d *DB) DeleteExpiredJoinTokens(ctx context.Context, before time.Time) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdDeleteExpiredJoinTokens, before.UnixNano())

	return err
}

func applyDeleteExpiredJoinTokens(a *applyCtx, before int64) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `DELETE FROM cluster_join_tokens WHERE expires_at < ?`, before)

	return struct{}{}, err
}
