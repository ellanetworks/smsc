package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
)

const appliedRequestsWindow = 1_000_000

type Events struct {
	Replaced      bool
	Messages      bool
	Notifications bool
	Settings      bool
	OIDC          bool
	ClosedESMEs   []int64
	Alerted       []string
}

func (e *Events) merge(o Events) {
	e.Replaced = e.Replaced || o.Replaced
	e.Messages = e.Messages || o.Messages
	e.Notifications = e.Notifications || o.Notifications
	e.Settings = e.Settings || o.Settings
	e.OIDC = e.OIDC || o.OIDC
	e.ClosedESMEs = append(e.ClosedESMEs, o.ClosedESMEs...)
	e.Alerted = append(e.Alerted, o.Alerted...)
}

func (e Events) empty() bool {
	return !e.Replaced && !e.Messages && !e.Notifications && !e.Settings && !e.OIDC && len(e.ClosedESMEs) == 0 && len(e.Alerted) == 0
}

type applyCtx struct {
	ctx        context.Context
	tx         *sql.Tx
	index      uint64
	term       uint64
	leaderTime int64
	events     *Events
}

type handler func(a *applyCtx, payload json.RawMessage) (any, error)

func handle[P, R any](fn func(a *applyCtx, p P) (R, error)) handler {
	return func(a *applyCtx, payload json.RawMessage) (any, error) {
		var p P
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		return fn(a, p)
	}
}

type Proposer interface {
	Propose(ctx context.Context, cmds []Command) ([]Result, error)
}

type FatalError struct {
	Index uint64
	Err   error
}

func (e *FatalError) Error() string {
	return fmt.Sprintf("apply entry %d: %v", e.Index, e.Err)
}

func (e *FatalError) Unwrap() error { return e.Err }

type fsmState struct {
	appliedIndex uint64
	appliedTerm  uint64
	hash         []byte
	leaderTime   int64
}

func readFSMState(ctx context.Context, q querier) (fsmState, error) {
	var s fsmState

	err := q.QueryRowContext(ctx, `SELECT applied_index, applied_term, hash, leader_time FROM fsm_state WHERE id = 1`).
		Scan(&s.appliedIndex, &s.appliedTerm, &s.hash, &s.leaderTime)

	return s, err
}

func (d *DB) AppliedIndex() uint64 {
	return d.applied.Load()
}

func (d *DB) Hash(ctx context.Context) (_ []byte, _ uint64, err error) {
	defer d.observe(&err)()

	s, err := readFSMState(ctx, d.reader())
	if err != nil {
		return nil, 0, fmt.Errorf("read state: %w", err)
	}

	return s.hash, s.appliedIndex, nil
}

func (d *DB) Apply(index, term uint64, entry []byte) ([]Result, error) {
	b, err := DecodeBatch(entry)
	if err != nil {
		return nil, &FatalError{Index: index, Err: err}
	}

	d.applyMu.Lock()

	results, events, err := d.applyBatch(index, term, b)

	d.applyMu.Unlock()

	if err != nil {
		return nil, &FatalError{Index: index, Err: err}
	}

	d.publish(events)

	return results, nil
}

func (d *DB) applyBatch(index, term uint64, b Batch) ([]Result, Events, error) {
	ctx := context.Background()

	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, Events{}, err
	}

	defer func() { _ = tx.Rollback() }()

	state, err := readFSMState(ctx, tx)
	if err != nil {
		return nil, Events{}, err
	}

	results := make([]Result, len(b.Commands))

	if index <= state.appliedIndex {
		for i := range results {
			results[i] = Result{Code: "already_applied", Message: ErrAlreadyApplied.Error()}
		}

		return results, Events{}, nil
	}

	var events Events

	hash := state.hash

	for i, c := range b.Commands {
		a := &applyCtx{ctx: ctx, tx: tx, index: index, term: term, leaderTime: b.LeaderTime}

		r, ev, err := applyCommand(a, c)
		if err != nil {
			return nil, Events{}, fmt.Errorf("command %d (type %d): %w", i, c.Type, err)
		}

		results[i] = r

		events.merge(ev)

		hash = chain(hash, index, c, r)
	}

	if index > appliedRequestsWindow {
		if _, err := tx.ExecContext(ctx, `DELETE FROM applied_requests WHERE applied_index < ?`,
			index-appliedRequestsWindow); err != nil {
			return nil, Events{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE fsm_state SET applied_index = ?, applied_term = ?, hash = ?,
		leader_time = MAX(leader_time, ?) WHERE id = 1`, index, term, hash, b.LeaderTime); err != nil {
		return nil, Events{}, err
	}

	if err := tx.Commit(); err != nil {
		return nil, Events{}, err
	}

	d.applied.Store(index)

	return results, events, nil
}

func applyCommand(a *applyCtx, c Command) (Result, Events, error) {
	ctx, tx := a.ctx, a.tx

	if c.Retryable {
		var stored []byte

		err := tx.QueryRowContext(ctx, `SELECT result FROM applied_requests WHERE request_id = ?`, c.RequestID[:]).
			Scan(&stored)
		if err == nil {
			var r Result
			if err := json.Unmarshal(stored, &r); err != nil {
				return Result{}, Events{}, err
			}

			r.Replayed = true

			return r, Events{}, nil
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return Result{}, Events{}, err
		}
	}

	h, ok := handlers[c.Type]
	if !ok || c.Version != 1 {
		return Result{}, Events{}, fmt.Errorf("unknown command type %d version %d", c.Type, c.Version)
	}

	if _, err := tx.ExecContext(ctx, `SAVEPOINT command`); err != nil {
		return Result{}, Events{}, err
	}

	var events Events

	a.events = &events

	value, herr := h(a, c.Payload)

	var r Result

	if herr != nil {
		code, ok := codeOf(herr)
		if !ok {
			return Result{}, Events{}, herr
		}

		if _, err := tx.ExecContext(ctx, `ROLLBACK TO command`); err != nil {
			return Result{}, Events{}, err
		}

		events = Events{}
		r = Result{Code: code, Message: herr.Error()}
	} else if value != nil {
		v, err := json.Marshal(value)
		if err != nil {
			return Result{}, Events{}, err
		}

		r.Value = v
	}

	if _, err := tx.ExecContext(ctx, `RELEASE command`); err != nil {
		return Result{}, Events{}, err
	}

	if c.Retryable {
		stored, err := json.Marshal(r)
		if err != nil {
			return Result{}, Events{}, err
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO applied_requests (request_id, result, applied_index)
			VALUES (?, ?, ?)`, c.RequestID[:], stored, a.index); err != nil {
			return Result{}, Events{}, err
		}
	}

	return r, events, nil
}

func chain(prev []byte, index uint64, c Command, r Result) []byte {
	h := sha256.New()
	h.Write(prev)

	var head [12]byte
	binary.BigEndian.PutUint64(head[:8], index)
	binary.BigEndian.PutUint16(head[8:10], uint16(c.Type))
	binary.BigEndian.PutUint16(head[10:], c.Version)
	h.Write(head[:])
	h.Write(c.RequestID[:])

	h.Write([]byte(r.Code))
	h.Write([]byte{0})
	h.Write(r.Value)

	return h.Sum(nil)
}

func isConstraint(err error) bool {
	var e sqlite3.Error

	return errors.As(err, &e) && e.Code == sqlite3.ErrConstraint
}

func (d *DB) publish(ev Events) {
	if ev.empty() {
		return
	}

	for _, f := range d.subscribers {
		f(ev)
	}
}

func (d *DB) OnEvents(f func(Events)) {
	d.subscribers = append(d.subscribers, f)
}

func (d *DB) SetProposer(p Proposer) {
	d.proposer = p
}

type localProposer struct {
	d    *DB
	now  func() time.Time
	mu   sync.Mutex
	last int64
}

func (l *localProposer) Propose(_ context.Context, cmds []Command) ([]Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.last = max(l.last+1, l.now().UnixNano())

	entry, err := EncodeBatch(Batch{LeaderTime: l.last, Commands: cmds})
	if err != nil {
		return nil, err
	}

	return l.d.Apply(l.d.AppliedIndex()+1, 1, entry)
}

func submit[R any](ctx context.Context, d *DB, t CommandType, payload any) (R, error) {
	r, _, err := submitVia[R](ctx, d.proposer, t, payload)
	return r, err
}

func submitReplay[R any](ctx context.Context, d *DB, t CommandType, payload any) (R, bool, error) {
	return submitVia[R](ctx, d.proposer, t, payload)
}

func submitVia[R any](ctx context.Context, p Proposer, t CommandType, payload any) (R, bool, error) {
	var zero R

	raw, err := json.Marshal(payload)
	if err != nil {
		return zero, false, fmt.Errorf("encode command: %w", err)
	}

	id, retryable := requestID(ctx, t)

	results, err := p.Propose(ctx, []Command{{Type: t, Version: 1, RequestID: id, Retryable: retryable, Payload: raw}})
	if err != nil {
		return zero, false, err
	}

	if len(results) != 1 {
		return zero, false, fmt.Errorf("command %d: %d results", t, len(results))
	}

	if err := results[0].Err(); err != nil {
		return zero, results[0].Replayed, err
	}

	var r R

	if len(results[0].Value) > 0 {
		if err := json.Unmarshal(results[0].Value, &r); err != nil {
			return zero, false, fmt.Errorf("decode result: %w", err)
		}
	}

	return r, results[0].Replayed, nil
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var handlers = map[CommandType]handler{}

func register(t CommandType, h handler) {
	if _, ok := handlers[t]; ok {
		panic(fmt.Sprintf("command type %d registered twice", t))
	}

	handlers[t] = h
}
