package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"

	"github.com/ellanetworks/smsc/internal/settings"
)

func (d *DB) peers(ctx context.Context) ([]settings.Peer, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, host, port FROM diameter_peers ORDER BY id`)
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

	if err := d.peerAddresses(ctx, peers, index); err != nil {
		return nil, err
	}

	if err := d.peerRoutes(ctx, peers, index); err != nil {
		return nil, err
	}

	return peers, nil
}

func (d *DB) peerAddresses(ctx context.Context, peers []settings.Peer, index map[int64]int) error {
	rows, err := d.conn.QueryContext(ctx, `SELECT peer_id, address FROM diameter_peer_addresses ORDER BY peer_id, position`)
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

		a, err := netip.ParseAddr(address)
		if err != nil {
			return fmt.Errorf("get peer addresses: peer %d: %w", id, err)
		}

		p := &peers[index[id]]
		p.Addresses = append(p.Addresses, a)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("get peer addresses: %w", err)
	}

	return nil
}

func (d *DB) peerRoutes(ctx context.Context, peers []settings.Peer, index map[int64]int) error {
	rows, err := d.conn.QueryContext(ctx, `SELECT peer_id, realm, application, priority FROM diameter_routes
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

// CreatePeer stores a new peer and returns its ID. It returns ErrAlreadyExists for a host that another peer has.
func (d *DB) CreatePeer(ctx context.Context, p settings.Peer) (_ int64, err error) {
	defer d.observe(&err)()

	var id int64

	err = d.inTx(ctx, "create peer", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO diameter_peers (host, port) VALUES (?, ?)`, p.Host, p.Port)
		if err != nil {
			return err
		}

		if id, err = res.LastInsertId(); err != nil {
			return err
		}

		return insertPeerDetails(ctx, tx, id, p)
	})
	if isUnique(err) {
		return 0, fmt.Errorf("create peer: %w", ErrAlreadyExists)
	}

	return id, err
}

// UpdatePeer replaces the peer of p.ID. It returns ErrNotFound for an unknown ID, and ErrAlreadyExists for a host
// that another peer has.
func (d *DB) UpdatePeer(ctx context.Context, p settings.Peer) (err error) {
	defer d.observe(&err)()

	err = d.inTx(ctx, "update peer", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE diameter_peers SET host = ?, port = ? WHERE id = ?`, p.Host, p.Port, p.ID)
		if err != nil {
			return err
		}

		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM diameter_peer_addresses WHERE peer_id = ?`, p.ID); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM diameter_routes WHERE peer_id = ?`, p.ID); err != nil {
			return err
		}

		return insertPeerDetails(ctx, tx, p.ID, p)
	})
	if isUnique(err) {
		return fmt.Errorf("update peer: %w", ErrAlreadyExists)
	}

	return err
}

func insertPeerDetails(ctx context.Context, tx *sql.Tx, id int64, p settings.Peer) error {
	for i, a := range p.Addresses {
		if _, err := tx.ExecContext(ctx, `INSERT INTO diameter_peer_addresses (peer_id, position, address)
			VALUES (?, ?, ?)`, id, i, a.String()); err != nil {
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

// DeletePeer deletes a peer and its routes. It returns ErrNotFound for an unknown ID.
func (d *DB) DeletePeer(ctx context.Context, id int64) (err error) {
	defer d.observe(&err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM diameter_peers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete peer: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("delete peer: %w", err)
	} else if n == 0 {
		return fmt.Errorf("delete peer: %w", ErrNotFound)
	}

	return nil
}
