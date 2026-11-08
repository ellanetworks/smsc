package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/settings"
)

func peers(ctx context.Context, q querier) ([]settings.Peer, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, host, port FROM diameter_peers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("get peers: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var (
		peers []settings.Peer
		index = make(map[int64]int)
	)

	for rows.Next() {
		var p settings.Peer
		if err := rows.Scan(&p.ID, &p.Host, &p.Port); err != nil {
			return nil, fmt.Errorf("get peers: %w", err)
		}

		index[p.ID] = len(peers)
		peers = append(peers, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get peers: %w", err)
	}

	if err := peerAddresses(ctx, q, peers, index); err != nil {
		return nil, err
	}

	if err := peerRoutes(ctx, q, peers, index); err != nil {
		return nil, err
	}

	return peers, nil
}

func peerAddresses(ctx context.Context, q querier, peers []settings.Peer, index map[int64]int) error {
	rows, err := q.QueryContext(ctx, `SELECT peer_id, address FROM diameter_peer_addresses ORDER BY peer_id, position`)
	if err != nil {
		return fmt.Errorf("get peer addresses: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id      int64
			address string
		)

		if err := rows.Scan(&id, &address); err != nil {
			return fmt.Errorf("get peer addresses: %w", err)
		}

		p := &peers[index[id]]

		a, err := netip.ParseAddr(address)
		switch {
		case err == nil:
			p.Addresses = append(p.Addresses, a)
		case diameter.IsValidHostname(address):
			p.Name = address
		default:
			return fmt.Errorf("get peer addresses: peer %d: %w", id, err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("get peer addresses: %w", err)
	}

	return nil
}

func peerRoutes(ctx context.Context, q querier, peers []settings.Peer, index map[int64]int) error {
	rows, err := q.QueryContext(ctx, `SELECT peer_id, realm, application, priority FROM diameter_routes
		ORDER BY peer_id, position`)
	if err != nil {
		return fmt.Errorf("get peer routes: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id int64
			r  settings.Route
		)

		if err := rows.Scan(&id, &r.Realm, &r.Application, &r.Priority); err != nil {
			return fmt.Errorf("get peer routes: %w", err)
		}

		r.Default = r.Realm == ""
		p := &peers[index[id]]
		p.Routes = append(p.Routes, r)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("get peer routes: %w", err)
	}

	return nil
}

func (d *DB) CreatePeer(ctx context.Context, p settings.Peer) (_ int64, err error) {
	defer d.observe(&err)()

	id, err := submit[int64](ctx, d, cmdCreatePeer, p)
	if err != nil {
		return 0, fmt.Errorf("create peer: %w", err)
	}

	return id, nil
}

func peerConflict(a *applyCtx, p settings.Peer) error {
	all, err := peers(a.ctx, a.tx)
	if err != nil {
		return err
	}

	o, err := getSettings(a.ctx, a.tx)
	if err != nil {
		return err
	}

	others := make([]settings.Peer, 0, len(all))

	for _, other := range all {
		if other.ID != p.ID {
			others = append(others, other)
		}
	}

	return p.Conflict(others, o.Hosts())
}

func applyCreatePeer(a *applyCtx, p settings.Peer) (int64, error) {
	res, err := a.tx.ExecContext(a.ctx, `INSERT INTO diameter_peers (host, port) VALUES (?, ?)`, p.Host, p.Port)
	if isUnique(err) {
		return 0, ErrAlreadyExists
	}

	if err != nil {
		return 0, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if err := insertPeerDetails(a.ctx, a.tx, id, p); err != nil {
		if isUnique(err) {
			return 0, ErrAlreadyExists
		}

		return 0, err
	}

	p.ID = id

	if err := peerConflict(a, p); err != nil {
		return 0, err
	}

	a.events.Settings = true

	return id, nil
}

func (d *DB) UpdatePeer(ctx context.Context, p settings.Peer) (err error) {
	defer d.observe(&err)()

	if _, err := submit[struct{}](ctx, d, cmdUpdatePeer, p); err != nil {
		return fmt.Errorf("update peer: %w", err)
	}

	return nil
}

func applyUpdatePeer(a *applyCtx, p settings.Peer) (struct{}, error) {
	ctx, tx := a.ctx, a.tx

	res, err := tx.ExecContext(ctx, `UPDATE diameter_peers SET host = ?, port = ? WHERE id = ?`, p.Host, p.Port, p.ID)
	if isUnique(err) {
		return struct{}{}, ErrAlreadyExists
	}

	if err != nil {
		return struct{}{}, err
	}

	if n, err := res.RowsAffected(); err != nil {
		return struct{}{}, err
	} else if n == 0 {
		return struct{}{}, ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM diameter_peer_addresses WHERE peer_id = ?`, p.ID); err != nil {
		return struct{}{}, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM diameter_routes WHERE peer_id = ?`, p.ID); err != nil {
		return struct{}{}, err
	}

	if err := insertPeerDetails(ctx, tx, p.ID, p); err != nil {
		if isUnique(err) {
			return struct{}{}, ErrAlreadyExists
		}

		return struct{}{}, err
	}

	if err := peerConflict(a, p); err != nil {
		return struct{}{}, err
	}

	a.events.Settings = true

	return struct{}{}, nil
}

func insertPeerDetails(ctx context.Context, tx *sql.Tx, id int64, p settings.Peer) error {
	addresses := make([]string, 0, len(p.Addresses)+1)
	if p.Name != "" {
		addresses = append(addresses, p.Name)
	}

	for _, a := range p.Addresses {
		addresses = append(addresses, a.String())
	}

	for i, a := range addresses {
		if _, err := tx.ExecContext(ctx, `INSERT INTO diameter_peer_addresses (peer_id, position, address)
			VALUES (?, ?, ?)`, id, i, a); err != nil {
			return err
		}
	}

	for i, r := range p.Routes {
		realm := r.Realm
		if r.Default {
			realm = ""
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO diameter_routes (peer_id, position, realm, application, priority)
			VALUES (?, ?, ?, ?, ?)`, id, i, realm, r.Application, r.Priority); err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) DeletePeer(ctx context.Context, id int64) (err error) {
	defer d.observe(&err)()

	if _, err := submit[struct{}](ctx, d, cmdDeletePeer, id); err != nil {
		return fmt.Errorf("delete peer: %w", err)
	}

	return nil
}

func applyDeletePeer(a *applyCtx, id int64) (struct{}, error) {
	res, err := a.tx.ExecContext(a.ctx, `DELETE FROM diameter_peers WHERE id = ?`, id)
	if err != nil {
		return struct{}{}, err
	}

	if n, err := res.RowsAffected(); err != nil {
		return struct{}{}, err
	} else if n == 0 {
		return struct{}{}, ErrNotFound
	}

	a.events.Settings = true

	return struct{}{}, nil
}
