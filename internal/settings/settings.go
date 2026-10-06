package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/smsc/internal/numbering"
)

type Settings struct {
	Operator Operator
	Delivery Delivery
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

func (s Settings) Validate() error {
	return errors.Join(s.Operator.Validate(), s.Delivery.Validate())
}

func (o Operator) Validate() error {
	switch {
	case !isDigits(o.MCC) || len(o.MCC) != 3:
		return errors.New("mcc must be 3 digits")
	case !isDigits(o.MNC) || len(o.MNC) < 2 || len(o.MNC) > 3:
		return errors.New("mnc must be 2 or 3 digits")
	case !isDigits(o.ServiceCentreAddress) || len(o.ServiceCentreAddress) > 15:
		return errors.New("service_centre_address must be 1 to 15 digits")
	case !isDigits(o.Numbering.CountryCode) || len(o.Numbering.CountryCode) > 3:
		return errors.New("numbering.country_code must be 1 to 3 digits")
	case o.Numbering.NationalPrefix != "" && !isDigits(o.Numbering.NationalPrefix):
		return errors.New("numbering.national_prefix must be digits")
	case o.Numbering.InternationalPrefix != "" && !isDigits(o.Numbering.InternationalPrefix):
		return errors.New("numbering.international_prefix must be digits")
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

type Store interface {
	UpdateOperator(ctx context.Context, o Operator) error
	UpdateDelivery(ctx context.Context, d Delivery) error
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
	l.current.Store(&initial)

	return l
}

// Get returns the current settings. Callers must not modify its slices.
func (l *Live) Get() Settings {
	return *l.current.Load()
}

func (l *Live) UpdateOperator(ctx context.Context, o Operator) error {
	if err := o.Validate(); err != nil {
		return err
	}

	return l.update(func() error { return l.store.UpdateOperator(ctx, o) }, func(s *Settings) { s.Operator = o })
}

func (l *Live) UpdateDelivery(ctx context.Context, d Delivery) error {
	if err := d.Validate(); err != nil {
		return err
	}

	d.RetryIntervals = slices.Clone(d.RetryIntervals)

	return l.update(func() error { return l.store.UpdateDelivery(ctx, d) }, func(s *Settings) { s.Delivery = d })
}

func (l *Live) update(save func() error, apply func(*Settings)) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := save(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	next := *l.current.Load()
	apply(&next)
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
