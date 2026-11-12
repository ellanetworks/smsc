package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	maxDialledRoute = 20
	maxE164Route    = 15
)

const routingSchema = 2

const (
	tonUnknown         = 0x0
	tonInternational   = 0x1
	tonNational        = 0x2
	tonNetworkSpecific = 0x3
	tonSubscriber      = 0x4
	tonAbbreviated     = 0x6
)

var ErrRouteOverlap = errors.New("the route overlaps a route of an ESME")

type Route struct {
	Low  string
	High string
}

func ParseRoute(s string) (Route, error) {
	low, high, ranged := strings.Cut(s, "-")
	if !ranged {
		high = low
	}

	if !validRouteNumber(low) || !validRouteNumber(high) {
		return Route{}, fmt.Errorf("%w: route %q: each end is 1 to 20 digits, or + and 1 to 15 digits", ErrInvalid, s)
	}

	r := Route{Low: low, High: high}

	switch {
	case strings.HasPrefix(low, "+") != strings.HasPrefix(high, "+"):
		return Route{}, fmt.Errorf("%w: route %q: both ends are dialled numbers, or both international", ErrInvalid, s)
	case len(low) != len(high):
		return Route{}, fmt.Errorf("%w: route %q: both ends have the same number of digits", ErrInvalid, s)
	case low > high:
		return Route{}, fmt.Errorf("%w: route %q: the first end is not after the last", ErrInvalid, s)
	}

	return r, nil
}

func (r Route) String() string {
	if r.Low == r.High {
		return r.Low
	}

	return r.Low + "-" + r.High
}

func (r Route) International() bool {
	return strings.HasPrefix(r.Low, "+")
}

func (r Route) Overlaps(o Route) bool {
	return r.International() == o.International() && len(r.Low) == len(o.Low) && r.Low <= o.High && o.Low <= r.High
}

func validRouteNumber(s string) bool {
	if e164, ok := strings.CutPrefix(s, "+"); ok {
		return digitsOnly(e164, maxE164Route)
	}

	return digitsOnly(s, maxDialledRoute)
}

func digitsOnly(s string, maxLen int) bool {
	if len(s) < 1 || len(s) > maxLen {
		return false
	}

	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}

type esmeRoute struct {
	esmeID   int64
	systemID string
	route    string
}

func (d *DB) Routing() bool {
	return d.Schema() >= routingSchema
}

func DialledRoute(route string) bool {
	return route != "" && !strings.HasPrefix(route, "+")
}

func (d *DB) RoutesFor(ctx context.Context, messageIDs []int64) (_ map[int64]string, err error) {
	defer d.observe(&err)()

	out := map[int64]string{}
	if len(messageIDs) == 0 || !d.Routing() {
		return out, nil
	}

	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		args[i] = id
	}

	rows, err := d.reader().QueryContext(ctx, `SELECT message_id, route FROM esme_deliveries
		WHERE message_id IN (?`+strings.Repeat(`, ?`, len(messageIDs)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("get routes: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id    int64
			route string
		)

		if err := rows.Scan(&id, &route); err != nil {
			return nil, fmt.Errorf("get routes: %w", err)
		}

		out[id] = route
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get routes: %w", err)
	}

	return out, nil
}

func insertRoutes(ctx context.Context, tx *sql.Tx, id int64, routes []string) error {
	if len(routes) > maxESMERoutes {
		return fmt.Errorf("%w: an ESME has at most %d routes", ErrInvalid, maxESMERoutes)
	}

	for _, s := range routes {
		r, err := ParseRoute(s)
		if err != nil {
			return err
		}

		var taken bool

		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM esme_routes
			WHERE length(low) = length(?1) AND (substr(low, 1, 1) = '+') = (substr(?1, 1, 1) = '+')
			AND low <= ?2 AND high >= ?1)`, r.Low, r.High).Scan(&taken); err != nil {
			return err
		}

		if taken {
			return fmt.Errorf("%w: %s", ErrRouteOverlap, r)
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO esme_routes (low, high, esme_id) VALUES (?, ?, ?)`,
			r.Low, r.High, id); err != nil {
			if isUnique(err) {
				return fmt.Errorf("%w: %s", ErrRouteOverlap, r)
			}

			return err
		}
	}

	return nil
}

// TS 23.040 clause 9.1.2.5
func ServedByHSS(typeOfNumber uint8) bool {
	switch typeOfNumber {
	case tonUnknown, tonInternational, tonNational:
		return true
	default:
		return false
	}
}

// TS 23.040 clause 9.1.2.5
// TS 23.040 clause 6.2.1
func dialledNumber(a Address) string {
	switch a.TypeOfNumber {
	case tonUnknown, tonNational, tonNetworkSpecific, tonSubscriber, tonAbbreviated:
		digits, _, _ := strings.Cut(a.Digits, "#")
		return digits
	default:
		return ""
	}
}

func e164Number(m NewMessage) string {
	if !ServedByHSS(m.Recipient.TypeOfNumber) || m.MSISDN == "" {
		return ""
	}

	return "+" + m.MSISDN
}

func routeOf(a *applyCtx, m NewMessage) (*esmeRoute, error) {
	if a.schema < routingSchema {
		return nil, nil
	}

	var (
		r         esmeRoute
		low, high string
	)

	err := a.tx.QueryRowContext(a.ctx, `SELECT r.esme_id, e.system_id, r.low, r.high FROM esme_routes r
		JOIN esmes e ON e.id = r.esme_id
		WHERE substr(r.low, 1, 1) != '+' AND length(r.low) = length(?1) AND ?1 BETWEEN r.low AND r.high
		OR substr(r.low, 1, 1) = '+' AND length(r.low) = length(?2) AND ?2 BETWEEN r.low AND r.high
		ORDER BY substr(r.low, 1, 1) = '+', r.id LIMIT 1`,
		dialledNumber(m.Recipient), e164Number(m)).Scan(&r.esmeID, &r.systemID, &low, &high)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	r.route = Route{Low: low, High: high}.String()

	return &r, nil
}
