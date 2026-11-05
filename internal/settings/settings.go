package settings

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	Peers     []Peer
	Labels    []string
}

type Operator struct {
	MCC                  string
	MNC                  string
	ServiceCentreAddress string
	Numbering            numbering.Plan
}

// TS 23.003 clause 19.4.2.8
func (o Operator) Host(label string) string {
	return label + ".node." + o.Realm()
}

func NodeLabel(nodeID string) string {
	id := strings.ToLower(nodeID)

	return "smsc-" + id[max(len(id)-12, 0):]
}

func (s Settings) Hosts() []string {
	hosts := make([]string, len(s.Labels))
	for i, l := range s.Labels {
		hosts[i] = s.Operator.Host(l)
	}

	return hosts
}

// TS 23.003 clause 19.2
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

type Retention struct {
	Messages time.Duration
}

const MinRetention = 24 * time.Hour

func (s Settings) Validate() error {
	errs := []error{s.Operator.Validate(), s.Delivery.Validate(), s.Retention.Validate()}

	for i, p := range s.Peers {
		errs = append(errs, p.Validate(), p.Conflict(s.Peers[:i], s.Hosts()))
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

type Live struct {
	store   Store
	current atomic.Pointer[Settings]

	mu      sync.Mutex
	stateMu sync.Mutex
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

func (o Operator) Conflict(peers []Peer, labels []string) error {
	s := Settings{Operator: o, Labels: labels}

	for _, host := range s.Hosts() {
		for _, p := range peers {
			if strings.EqualFold(p.Host, host) {
				return fmt.Errorf("%w: host %s is peer %d's", ErrConflict, host, p.ID)
			}
		}
	}

	return nil
}

func (l *Live) Get() Settings {
	return *l.current.Load()
}

func (l *Live) UpdateOperator(ctx context.Context, o Operator) error {
	if err := o.Validate(); err != nil {
		return err
	}

	return l.change(func(s *Settings) error {
		if err := o.Conflict(s.Peers, s.Labels); err != nil {
			return err
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

func (l *Live) change(fn func(*Settings) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	next := *l.current.Load()
	if err := fn(&next); err != nil {
		return err
	}

	l.swap(next)

	return nil
}

func (l *Live) Reload(next Settings) {
	next.Delivery.RetryIntervals = slices.Clone(next.Delivery.RetryIntervals)
	next.Peers = slices.Clone(next.Peers)

	for i, p := range next.Peers {
		next.Peers[i] = clonePeer(p)
	}

	l.swap(next)
}

func (l *Live) swap(next Settings) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

	if reflect.DeepEqual(normalized(*l.current.Load()), normalized(next)) {
		return
	}

	l.current.Store(&next)

	close(l.changed)
	l.changed = make(chan struct{})
}

func normalized(s Settings) Settings {
	if len(s.Delivery.RetryIntervals) == 0 {
		s.Delivery.RetryIntervals = nil
	}

	peers := make([]Peer, len(s.Peers))

	for i, p := range s.Peers {
		if len(p.Addresses) == 0 {
			p.Addresses = nil
		}

		if len(p.Routes) == 0 {
			p.Routes = nil
		}

		peers[i] = p
	}

	if len(peers) == 0 {
		peers = nil
	}

	s.Peers = peers

	return s
}

func (l *Live) Changed() <-chan struct{} {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()

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
