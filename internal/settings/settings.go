package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/smsc/internal/numbering"
)

type Settings struct {
	Operator  Operator
	Delivery  Delivery
	Retention Retention
	// Peers are ordered by ID.
	Peers []Peer
}

type Operator struct {
	MCC                  string
	MNC                  string
	ServiceCentreAddress string
	Numbering            numbering.Plan
}

// Realm is the home network realm of the operator's PLMN (3GPP TS 23.003
// clause 19.2). The SMSC uses it as its own Diameter realm and to address the
// HSS (3GPP TS 29.338 clauses 5.3.1.1.2 and 6.3.1.1).
// Host is the SMSC's Diameter identity, named under the operator-controlled
// node subdomain of the realm (3GPP TS 23.003 clause 19.4.2.8).
func (o Operator) Host() string {
	return "smsc.node." + o.Realm()
}

func (o Operator) Realm() string {
	mnc := o.MNC
	if len(mnc) == 2 {
		mnc = "0" + mnc
	}

	return "epc.mnc" + mnc + ".mcc" + o.MCC + ".3gppnetwork.org"
}

type Delivery struct {
	DefaultValidity time.Duration
	RetryIntervals  []time.Duration
}

// Retention is how long the SMSC keeps a message once it has its final status. 3GPP sets no such period: the SC is
// responsible for a message only until its report is received or its validity period expires (TS 23.040 clause 6.1).
type Retention struct {
	Messages time.Duration
}

// MinRetention is the shortest retention.
const MinRetention = 24 * time.Hour

func (s Settings) Validate() error {
	errs := []error{s.Operator.Validate(), s.Delivery.Validate(), s.Retention.Validate()}

	for i, p := range s.Peers {
		errs = append(errs, p.Validate(), p.conflict(s.Peers[:i], s.Operator.Host()))
	}

	return errors.Join(errs...)
}

func (o Operator) Validate() error {
	switch {
	case !isDigits(o.MCC) || len(o.MCC) != 3:
		return errors.New("mcc must be 3 digits")
	case !isDigits(o.MNC) || len(o.MNC) < 2 || len(o.MNC) > 3:
		return errors.New("mnc must be 2 or 3 digits")
	case !isDigits(o.ServiceCentreAddress) || len(o.ServiceCentreAddress) > 15:
		return errors.New("service_centre_address must be 1 to 15 digits")
	case !isDigits(o.Numbering.CountryCode) || len(o.Numbering.CountryCode) > 3 || o.Numbering.CountryCode[0] == '0':
		return errors.New("numbering.country_code must be 1 to 3 digits, not starting with 0")
	case !isPrefix(o.Numbering.NationalPrefix):
		return errors.New("numbering.national_prefix must be up to 4 digits")
	case !isPrefix(o.Numbering.InternationalPrefix):
		return errors.New("numbering.international_prefix must be up to 4 digits")
	case o.Numbering.NationalPrefix != "" && o.Numbering.NationalPrefix == o.Numbering.InternationalPrefix:
		return errors.New("numbering.national_prefix and numbering.international_prefix must differ")
	}

	return nil
}

func (d Delivery) Validate() error {
	if d.DefaultValidity <= 0 {
		return errors.New("default_validity must be positive")
	}

	for _, interval := range d.RetryIntervals {
		if interval <= 0 {
			return errors.New("retry_intervals must be positive")
		}
	}

	return nil
}

func (r Retention) Validate() error {
	if r.Messages < MinRetention {
		return errors.New("messages retention must be at least a day")
	}

	return nil
}

type Store interface {
	UpdateOperator(ctx context.Context, o Operator) error
	UpdateDelivery(ctx context.Context, d Delivery) error
	UpdateRetention(ctx context.Context, r Retention) error
	PeerStore
}

// Live serves the current settings to the running SMSC and persists changes
// before they take effect.
type Live struct {
	store   Store
	current atomic.Pointer[Settings]

	mu      sync.Mutex
	changed chan struct{}
}

func NewLive(store Store, initial Settings) *Live {
	l := &Live{store: store, changed: make(chan struct{})}

	initial.Delivery.RetryIntervals = slices.Clone(initial.Delivery.RetryIntervals)
	initial.Peers = slices.Clone(initial.Peers)

	for i, p := range initial.Peers {
		initial.Peers[i] = clonePeer(p)
	}

	l.current.Store(&initial)

	return l
}

// Get returns the current settings. Callers must not modify its slices.
func (l *Live) Get() Settings {
	return *l.current.Load()
}

// UpdateOperator changes the operator, unless its new Diameter host is a configured peer's.
func (l *Live) UpdateOperator(ctx context.Context, o Operator) error {
	if err := o.Validate(); err != nil {
		return err
	}

	return l.change(func(s *Settings) error {
		for _, p := range s.Peers {
			if strings.EqualFold(p.Host, o.Host()) {
				return fmt.Errorf("%w: host %s is peer %d's", ErrConflict, o.Host(), p.ID)
			}
		}

		if err := l.store.UpdateOperator(ctx, o); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}

		s.Operator = o

		return nil
	})
}

func (l *Live) UpdateDelivery(ctx context.Context, d Delivery) error {
	if err := d.Validate(); err != nil {
		return err
	}

	d.RetryIntervals = slices.Clone(d.RetryIntervals)

	return l.update(func() error { return l.store.UpdateDelivery(ctx, d) }, func(s *Settings) { s.Delivery = d })
}

func (l *Live) UpdateRetention(ctx context.Context, r Retention) error {
	if err := r.Validate(); err != nil {
		return err
	}

	return l.update(func() error { return l.store.UpdateRetention(ctx, r) }, func(s *Settings) { s.Retention = r })
}

func (l *Live) update(save func() error, apply func(*Settings)) error {
	return l.change(func(s *Settings) error {
		if err := save(); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}

		apply(s)

		return nil
	})
}

// change applies fn to a copy of the current settings, which it must persist, and makes the copy current unless fn
// fails. Changes are serialized, so fn sees the settings it replaces.
func (l *Live) change(fn func(*Settings) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	next := *l.current.Load()
	if err := fn(&next); err != nil {
		return err
	}

	l.current.Store(&next)

	close(l.changed)
	l.changed = make(chan struct{})

	return nil
}

// Changed returns a channel that is closed on the next successful update.
func (l *Live) Changed() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.changed
}

func isPrefix(s string) bool {
	return s == "" || (isDigits(s) && len(s) <= 4)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
