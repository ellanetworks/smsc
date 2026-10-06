package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

const errorBackoff = time.Second

const (
	DefaultAttemptTimeout = 30 * time.Second
	DefaultConcurrency    = 20
)

const mwdStatusFlags = s6c.MWDStatusMNRF | s6c.MWDStatusMCEF | s6c.MWDStatusMNRG | s6c.MWDStatusMNR5G | s6c.MWDStatusMNR5GN3G

type Store interface {
	NextDue(ctx context.Context, now time.Time, busy []string) (db.Message, bool, error)
	NextWakeup(ctx context.Context, busy []string) (time.Time, bool, error)
	SetMessageStatus(ctx context.Context, id int64, status db.MessageStatus, at time.Time) error
	ScheduleRetry(ctx context.Context, id int64, at, now time.Time) error
	HoldRecipient(ctx context.Context, msisdn string, until, now time.Time) error
	AlertRecipient(ctx context.Context, msisdn string, now time.Time) ([]string, error)
	SetAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string, now time.Time) error
	CountPendingFor(ctx context.Context, msisdn string, excludeID int64) (int, error)
	CreateDeliveryAttempt(ctx context.Context, a db.DeliveryAttempt) (int64, error)
}

type Router interface {
	SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (routing s6c.Routing, hss string, err error)
	ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error)
}

type Sender interface {
	Do(ctx context.Context, peerHost string, req *diameter.Message) (*diameter.Message, error)
	NewSessionID() string
}

type Deliverer struct {
	Store          Store
	Router         Router
	Sender         Sender
	Identity       func() diameter.Identity
	Settings       func() settings.Settings
	AttemptTimeout time.Duration
	Concurrency    int
	Now            func() time.Time
	Logger         *slog.Logger

	wakeOnce sync.Once
	wake     chan struct{}

	mu      sync.Mutex
	busy    map[string]bool
	realert map[string]bool
}

type outcome int

const (
	delivered outcome = iota
	permanent
	temporary
	expired
	targetFailed
	interrupted
)

type nodeKind int

const (
	kindMME nodeKind = iota
	kindSGSN
	kindSMSF3GPP
	kindSMSFNon3GPP
)

type nodeSource int

const (
	sourceServing nodeSource = iota
	sourceAdditional
	sourceSMSF3GPP
	sourceSMSFNon3GPP
)

type target struct {
	kind       nodeKind
	source     nodeSource
	name       string
	realm      string
	mmeNumber  string
	sgsnNumber string
}

type forwardResult struct {
	outcome    outcome
	cause      *s6c.DeliveryCause
	diagnostic *tgpp.AbsentUserDiagnostic
}

type nodeOutcome struct {
	kind       nodeKind
	source     nodeSource
	cause      s6c.DeliveryCause
	diagnostic *tgpp.AbsentUserDiagnostic
}

type result struct {
	outcome outcome
	hold    bool
}

func (d *Deliverer) Notify() {
	select {
	case d.wakeChan() <- struct{}{}:
	default:
	}
}

func (d *Deliverer) wakeChan() chan struct{} {
	d.wakeOnce.Do(func() { d.wake = make(chan struct{}, 1) })

	return d.wake
}

func (d *Deliverer) Alert(ctx context.Context, msisdn string) error {
	recipients, err := d.Store.AlertRecipient(ctx, msisdn, d.Now())
	if err != nil {
		return err
	}

	d.mu.Lock()

	for _, r := range recipients {
		if d.busy[r] {
			d.realert[r] = true
		}
	}

	d.mu.Unlock()

	d.Notify()

	return nil
}

func (d *Deliverer) Run(ctx context.Context) {
	wake := d.wakeChan()
	slots := max(d.Concurrency, 1)
	done := make(chan string)
	running := 0

	d.mu.Lock()
	d.busy = make(map[string]bool)
	d.realert = make(map[string]bool)
	d.mu.Unlock()

	for ctx.Err() == nil {
		wait := time.Duration(-1)

		if running < slots {
			m, ok, err := d.Store.NextDue(ctx, d.Now(), d.busyRecipients())

			switch {
			case err != nil:
				d.Logger.Error("delivery worker failed", slog.Any("error", err))

				wait = errorBackoff
			case ok:
				d.setBusy(m.MSISDN)

				running++

				go func() {
					if err := d.process(ctx, m); err != nil {
						d.Logger.Error("failed to record delivery outcome", slog.Int64("message_id", m.ID), slog.Any("error", err))
					}

					done <- m.MSISDN
				}()

				continue
			default:
				at, ok, err := d.Store.NextWakeup(ctx, d.busyRecipients())
				if err != nil {
					d.Logger.Error("delivery worker failed to read the next wakeup", slog.Any("error", err))

					wait = errorBackoff
				} else if ok {
					wait = max(at.Sub(d.Now()), 0)
				}
			}
		}

		var (
			timer *time.Timer
			fire  <-chan time.Time
		)

		if wait >= 0 {
			timer = time.NewTimer(wait)
			fire = timer.C
		}

		select {
		case <-ctx.Done():
		case <-wake:
		case <-fire:
		case msisdn := <-done:
			running--

			d.finish(context.WithoutCancel(ctx), msisdn)
		}

		if timer != nil {
			timer.Stop()
		}
	}

	for ; running > 0; running-- {
		d.finish(context.WithoutCancel(ctx), <-done)
	}
}

func (d *Deliverer) busyRecipients() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	busy := make([]string, 0, len(d.busy))
	for msisdn := range d.busy {
		busy = append(busy, msisdn)
	}

	return busy
}

func (d *Deliverer) setBusy(msisdn string) {
	d.mu.Lock()
	d.busy[msisdn] = true
	d.mu.Unlock()
}

func (d *Deliverer) finish(ctx context.Context, msisdn string) {
	d.mu.Lock()
	delete(d.busy, msisdn)
	realert := d.realert[msisdn]
	delete(d.realert, msisdn)
	d.mu.Unlock()

	if !realert {
		return
	}

	if _, err := d.Store.AlertRecipient(ctx, msisdn, d.Now()); err != nil {
		d.Logger.Error("failed to apply a service centre alert", slog.String("msisdn", msisdn), slog.Any("error", err))
	}
}

func (d *Deliverer) process(stop context.Context, m db.Message) error {
	r := d.deliver(stop, m, d.Now())
	ctx := context.WithoutCancel(stop)

	switch r.outcome {
	case delivered:
		return d.Store.SetMessageStatus(ctx, m.ID, db.StatusDelivered, d.Now())
	case expired:
		return d.Store.SetMessageStatus(ctx, m.ID, db.StatusExpired, d.Now())
	case permanent:
		return d.Store.SetMessageStatus(ctx, m.ID, db.StatusFailed, d.Now())
	case interrupted:
		return nil
	}

	now := d.Now()

	intervals := d.Settings().Delivery.RetryIntervals
	if m.SingleShot || len(intervals) == 0 {
		return d.Store.SetMessageStatus(ctx, m.ID, db.StatusFailed, now)
	}

	at := now.Add(intervals[min(m.Retries, len(intervals)-1)])

	if err := d.Store.ScheduleRetry(ctx, m.ID, at, now); err != nil {
		return err
	}

	if !r.hold {
		return nil
	}

	return d.Store.HoldRecipient(ctx, m.MSISDN, at, now)
}

func (d *Deliverer) deliver(stop context.Context, m db.Message, now time.Time) result {
	ctx := context.WithoutCancel(stop)
	log := d.Logger.With(slog.Int64("message_id", m.ID))

	if !now.Before(m.ExpiresAt) {
		log.Debug("short message expired before delivery")
		return result{outcome: expired}
	}

	started := d.Now()
	sri, cancel := context.WithTimeout(stop, d.AttemptTimeout)
	routing, hss, err := d.Router.SendRoutingInfoForSM(sri, s6c.RoutingRequest{MSISDN: m.MSISDN, SingleAttempt: m.SingleShot})

	cancel()

	if err != nil && stop.Err() != nil {
		log.Debug("short message routing lookup interrupted by shutdown")
		return result{outcome: interrupted}
	}

	routingAttempt := routingAttemptOf(routing, err)
	routingAttempt.Step, routingAttempt.Node, routingAttempt.StartedAt = db.StepRouting, hss, started

	d.record(ctx, log, m.ID, routingAttempt)

	if err != nil {
		log.Debug("routing lookup for short message failed", slog.Any("error", err))

		return d.routingFailed(ctx, m, err)
	}

	d.setAlertMSISDN(ctx, m.MSISDN, routing.AlertMSISDN)

	deliver, err := buildDeliver(m)
	if err != nil {
		log.Error("stored short message cannot be turned into an SMS-DELIVER", slog.Any("error", err))
		return result{outcome: permanent}
	}

	targets := deliveryTargets(routing.ServingNodes)
	if len(targets) == 0 {
		log.Debug("no serving node reachable over SGd for the recipient")
		return result{outcome: permanent}
	}

	more, err := d.Store.CountPendingFor(ctx, m.MSISDN, m.ID)
	if err != nil {
		log.Error("failed to count pending messages", slog.Any("error", err))
	}

	a := d.attempt(stop, log, m, routing.IMSI, targets, deliver, more > 0)

	if a.outcome == interrupted {
		log.Debug("short message delivery interrupted by shutdown")
		return result{outcome: interrupted}
	}

	if a.outcome == delivered {
		if success := a.successReport(); routing.MWDStatus&mwdStatusFlags != 0 || len(success) > 1 {
			d.report(stop, log, m, success, nil)
		}

		return result{outcome: delivered}
	}

	if a.outcome == permanent || len(a.failures()) == 0 {
		return result{outcome: a.outcome}
	}

	if !needsReport(a.failures(), routing) {
		return result{outcome: temporary, hold: true}
	}

	alert, ok := d.report(stop, log, m, a.outcomes, &routing.ServingNodes)
	if !ok {
		return result{outcome: temporary, hold: true}
	}

	retryTargets := deliveryTargets(alert.ServingNodes)
	if len(retryTargets) == 0 || stop.Err() != nil {
		return result{outcome: temporary, hold: true}
	}

	log.Debug("retrying delivery via the serving nodes the HSS reported")

	retry := d.attempt(stop, log, m, routing.IMSI, retryTargets, deliver, more > 0)

	switch retry.outcome {
	case delivered:
		d.report(stop, log, m, retry.successReport(), nil)

		return result{outcome: delivered}
	case permanent:
		return result{outcome: permanent}
	default:
		return result{outcome: temporary, hold: true}
	}
}

func (d *Deliverer) routingFailed(ctx context.Context, m db.Message, err error) result {
	if re := (*s6c.ResultError)(nil); errors.As(err, &re) && re.AlertMSISDN != "" {
		d.setAlertMSISDN(ctx, m.MSISDN, re.AlertMSISDN)
	}

	for _, code := range []uint32{
		tgpp.ResultErrorUserUnknown,
		tgpp.ResultErrorServiceNotSubscribed,
		tgpp.ResultErrorServiceBarred,
		tgpp.ResultErrorFacilityNotSupported,
	} {
		if tgpp.IsExperimental(err, code) {
			return result{outcome: permanent}
		}
	}

	return result{outcome: temporary, hold: tgpp.IsExperimental(err, tgpp.ResultErrorAbsentUser)}
}

func (d *Deliverer) setAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string) {
	if err := d.Store.SetAlertMSISDN(ctx, msisdn, alertMSISDN, d.Now()); err != nil {
		d.Logger.Error("failed to record the Alert MSISDN", slog.String("msisdn", msisdn), slog.Any("error", err))
	}
}

type attemptResult struct {
	outcome  outcome
	outcomes []nodeOutcome
}

func (a attemptResult) failures() []nodeOutcome {
	var failures []nodeOutcome

	for _, o := range a.outcomes {
		if o.cause != s6c.DeliveryCauseSuccessfulTransfer {
			failures = append(failures, o)
		}
	}

	return failures
}

func (a attemptResult) successReport() []nodeOutcome {
	var outcomes []nodeOutcome

	for _, o := range a.outcomes {
		if o.cause != s6c.DeliveryCauseMemoryCapacityExceeded {
			outcomes = append(outcomes, o)
		}
	}

	return outcomes
}

func (d *Deliverer) attempt(stop context.Context, log *slog.Logger, m db.Message, imsi string, targets []target,
	deliver func(bool) ([]byte, error), more bool,
) attemptResult {
	var a attemptResult

	ctx := context.WithoutCancel(stop)
	retry := false

	for i, t := range targets {
		if stop.Err() != nil {
			if i == 0 {
				a.outcome = interrupted
				return a
			}

			retry = true

			break
		}

		started := d.Now()
		r, err := d.forward(ctx, imsi, t, deliver, more)

		deliveryAttempt := deliveryAttemptOf(t.kind, err)
		deliveryAttempt.Step, deliveryAttempt.Node, deliveryAttempt.StartedAt = db.StepDelivery, t.name, started

		d.record(ctx, log, m.ID, deliveryAttempt)

		if r.cause != nil {
			a.outcomes = append(a.outcomes, nodeOutcome{kind: t.kind, source: t.source, cause: *r.cause, diagnostic: r.diagnostic})
			retry = true
		}

		switch r.outcome {
		case delivered, permanent:
			a.outcome = r.outcome
			return a
		case temporary:
			retry = true
		}
	}

	a.outcome = permanent
	if retry {
		a.outcome = temporary
	}

	return a
}

func needsReport(failures []nodeOutcome, routing s6c.Routing) bool {
	for _, f := range failures {
		if routing.MWDStatus&s6c.MWDStatusSCAddressNotIncluded != 0 {
			return true
		}

		if routing.MWDStatus&mwdFlag(f) == 0 {
			return true
		}

		if f.diagnostic != nil {
			if hss := hssDiagnostic(routing.Absent, f.kind); hss == nil || *hss != *f.diagnostic {
				return true
			}
		}
	}

	return false
}

func mwdFlag(o nodeOutcome) s6c.MWDStatus {
	if o.cause == s6c.DeliveryCauseMemoryCapacityExceeded {
		return s6c.MWDStatusMCEF
	}

	switch o.kind {
	case kindSGSN:
		return s6c.MWDStatusMNRG
	case kindSMSF3GPP:
		return s6c.MWDStatusMNR5G
	case kindSMSFNon3GPP:
		return s6c.MWDStatusMNR5GN3G
	default:
		return s6c.MWDStatusMNRF
	}
}

func hssDiagnostic(a s6c.AbsentUserDiagnostics, kind nodeKind) *tgpp.AbsentUserDiagnostic {
	switch kind {
	case kindSGSN:
		return a.SGSN
	case kindSMSF3GPP:
		return a.SMSF3GPP
	case kindSMSFNon3GPP:
		return a.SMSFNon3GPP
	default:
		return a.MME
	}
}

func (d *Deliverer) report(stop context.Context, log *slog.Logger, m db.Message, outcomes []nodeOutcome,
	attempted *s6c.ServingNodes,
) (s6c.ReportResult, bool) {
	if stop.Err() != nil {
		return s6c.ReportResult{}, false
	}

	ctx := context.WithoutCancel(stop)
	rep := s6c.DeliveryReport{MSISDN: m.MSISDN, SingleAttempt: m.SingleShot}

	for _, o := range outcomes {
		if attempted != nil && o.cause == s6c.DeliveryCauseAbsentUser {
			switch o.source {
			case sourceServing:
				rep.Failed.Serving = attempted.Serving
			case sourceAdditional:
				rep.Failed.Additional = attempted.Additional
			case sourceSMSF3GPP:
				rep.Failed.SMSF3GPP = attempted.SMSF3GPP
			case sourceSMSFNon3GPP:
				rep.Failed.SMSFNon3GPP = attempted.SMSFNon3GPP
			}
		}

		slot := &rep.MME

		switch o.kind {
		case kindSGSN:
			slot = &rep.SGSN
		case kindSMSF3GPP:
			slot = &rep.SMSF3GPP
		case kindSMSFNon3GPP:
			slot = &rep.SMSFNon3GPP
		}

		if *slot == nil || o.cause == s6c.DeliveryCauseSuccessfulTransfer {
			*slot = &s6c.DeliveryOutcome{Cause: o.cause, AbsentDiagnostic: o.diagnostic}
		}
	}

	rdr, cancel := context.WithTimeout(ctx, d.AttemptTimeout)
	defer cancel()

	res, err := d.Router.ReportSMDeliveryStatus(rdr, rep)
	if err != nil {
		log.Debug("failed to report delivery status to the HSS", slog.Any("error", err))
		return s6c.ReportResult{}, false
	}

	if res.AlertMSISDN != "" {
		d.setAlertMSISDN(ctx, m.MSISDN, res.AlertMSISDN)
	}

	return res, true
}

func buildDeliver(m db.Message) (func(more bool) ([]byte, error), error) {
	submit, err := tpdu.DecodeSubmit(m.TPDU)
	if err != nil {
		return nil, err
	}

	originator := tpdu.Address{
		TypeOfNumber:  m.Originator.TypeOfNumber,
		NumberingPlan: m.Originator.NumberingPlan,
		Digits:        m.Originator.Digits,
	}

	return func(more bool) ([]byte, error) {
		deliver := tpdu.DeliverFromSubmit(submit, originator, m.SubmittedAt)
		deliver.MoreMessagesToSend = more

		return deliver.Encode()
	}, nil
}

func deliveryTargets(n s6c.ServingNodes) []target {
	var targets []target

	seen := make(map[string]bool)

	add := func(t target) {
		key := strings.ToLower(t.name)
		if t.name == "" || seen[key] {
			return
		}

		seen[key] = true

		targets = append(targets, t)
	}

	for _, sn := range []struct {
		source nodeSource
		node   *s6c.ServingNode
	}{
		{sourceServing, n.Serving},
		{sourceAdditional, n.Additional},
	} {
		if sn.node == nil {
			continue
		}

		if sn.node.MME != nil {
			add(target{
				kind: kindMME, source: sn.source, name: sn.node.MME.Name, realm: sn.node.MME.Realm,
				mmeNumber: sn.node.MME.Number,
			})
		}

		if sn.node.SGSN != nil {
			add(target{
				kind: kindSGSN, source: sn.source, name: sn.node.SGSN.Name, realm: sn.node.SGSN.Realm,
				sgsnNumber: sn.node.SGSN.Number,
			})
		}
	}

	if n.SMSF3GPP != nil {
		add(target{kind: kindSMSF3GPP, source: sourceSMSF3GPP, name: n.SMSF3GPP.Name, realm: n.SMSF3GPP.Realm})
	}

	if n.SMSFNon3GPP != nil {
		add(target{kind: kindSMSFNon3GPP, source: sourceSMSFNon3GPP, name: n.SMSFNon3GPP.Name, realm: n.SMSFNon3GPP.Realm})
	}

	return targets
}

func (d *Deliverer) forward(ctx context.Context, imsi string, t target, deliver func(bool) ([]byte, error), more bool) (forwardResult, error) {
	smRPUI, err := deliver(more)
	if err != nil {
		return forwardResult{outcome: permanent}, err
	}

	tfr, err := sgd.NewMTForwardShortMessageRequest(tgpp.Envelope{
		SessionID:        d.Sender.NewSessionID(),
		Origin:           d.Identity(),
		DestinationHost:  t.name,
		DestinationRealm: t.realm,
	}, sgd.MTForwardShortMessage{
		IMSI:                 imsi,
		ServiceCentreAddress: d.Settings().Operator.ServiceCentreAddress,
		SMRPUI:               smRPUI,
		MMENumberForMTSMS:    t.mmeNumber,
		SGSNNumber:           t.sgsnNumber,
		MoreMessagesToSend:   more,
		DeliveryTimer:        d.AttemptTimeout,
		DeliveryStartTime:    d.Now(),
	})
	if err != nil {
		return forwardResult{outcome: permanent}, err
	}

	attempt, cancel := context.WithTimeout(ctx, d.AttemptTimeout)
	defer cancel()

	ans, err := d.Sender.Do(attempt, t.name, tfr)
	if err != nil {
		return forwardResult{outcome: temporary}, err
	}

	_, err = sgd.ParseMTForwardShortMessageAnswer(ans)

	return classifyForwardAnswer(err), err
}

func classifyForwardAnswer(err error) forwardResult {
	if err == nil {
		return forwardResult{outcome: delivered, cause: ptr(s6c.DeliveryCauseSuccessfulTransfer)}
	}

	var re *sgd.ResultError
	if !errors.As(err, &re) {
		return forwardResult{outcome: temporary}
	}

	code := re.Code

	if !re.Experimental {
		if code >= 5000 && code < 6000 {
			return forwardResult{outcome: targetFailed}
		}

		return forwardResult{outcome: temporary}
	}

	if re.VendorID != tgpp.VendorID {
		return forwardResult{outcome: temporary}
	}

	switch code {
	case tgpp.ResultErrorUserUnknown:
		return forwardResult{outcome: targetFailed, cause: ptr(s6c.DeliveryCauseAbsentUser)}
	case tgpp.ResultErrorAbsentUser:
		return forwardResult{outcome: temporary, cause: ptr(s6c.DeliveryCauseAbsentUser), diagnostic: re.AbsentUserDiagnostic}
	case tgpp.ResultErrorIllegalUser, tgpp.ResultErrorIllegalEquipment:
		return forwardResult{outcome: permanent}
	case tgpp.ResultErrorSMDeliveryFailure:
		cause := re.DeliveryFailureCause

		switch {
		case cause != nil && *cause == sgd.CauseEquipmentNotSMEquipped:
			return forwardResult{outcome: permanent}
		case cause != nil && *cause == sgd.CauseMemoryCapacityExceeded:
			return forwardResult{outcome: temporary, cause: ptr(s6c.DeliveryCauseMemoryCapacityExceeded)}
		default:
			return forwardResult{outcome: temporary}
		}
	default:
		return forwardResult{outcome: temporary}
	}
}

func ptr[T any](v T) *T {
	return &v
}
