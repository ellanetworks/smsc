package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/statusreport"
	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const errorBackoff = time.Second

const (
	tracerName         = "github.com/ellanetworks/smsc/internal/delivery"
	deliverOperation   = "deliver"
	outcomeKey         = attribute.Key("ella.sms.delivery.outcome")
	outcomeRetry       = "retry"
	outcomeInterrupted = "interrupted"
	outcomeSuperseded  = "superseded"
)

const (
	DefaultAttemptTimeout = 30 * time.Second
	DefaultConcurrency    = 256
	ClaimMargin           = 5 * time.Second
)

const mwdStatusFlags = s6c.MWDStatusMNRF | s6c.MWDStatusMCEF | s6c.MWDStatusMNRG | s6c.MWDStatusMNR5G | s6c.MWDStatusMNR5GN3G

type Store interface {
	DueRecipients(ctx context.Context, now time.Time, limit int, skip func(msisdn string) bool) ([]string, time.Time, error)
	NextDue(ctx context.Context, msisdn string, now time.Time) (db.Message, bool, error)
	CompleteMessage(ctx context.Context, id int64, status db.MessageStatus, at time.Time, report *db.NewStatusReport,
		tpStatus uint8, f db.Fence, attempts ...db.DeliveryAttempt) error
	ScheduleRetry(ctx context.Context, id int64, at, now time.Time, hold bool, since time.Time, f db.Fence,
		attempts ...db.DeliveryAttempt) error
	ClaimRecipient(ctx context.Context, msisdn string, term uint64) (db.Claim, error)
	ReleaseRecipient(ctx context.Context, msisdn string, c db.Claim) error
	RecordAttemptAndClaim(ctx context.Context, a db.DeliveryAttempt, msisdn string, term uint64, alertMSISDN *string) (db.Claim,
		error)
	ClaimedBefore(ctx context.Context, term uint64, since time.Time) ([]string, error)
	AlertRecipient(ctx context.Context, msisdn string, now time.Time) ([]string, error)
	SetAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string, now time.Time) error
	NextDueFor(ctx context.Context, msisdn string, excludeID int64, now time.Time) (db.Message, bool, error)
	CreateDeliveryAttempt(ctx context.Context, a db.DeliveryAttempt) (int64, error)
}

type Router interface {
	SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (routing s6c.Routing, hss string, err error)
	ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error)
}

type Sender interface {
	Do(ctx context.Context, peerHost string, req *diameter.Message) (*diameter.Message, error)
	PeerRealm(peerHost string) (string, bool)
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
	Metrics        *Metrics
	Now            func() time.Time
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider

	wakeOnce sync.Once
	wake     chan struct{}

	mu       sync.Mutex
	busy     map[string]bool
	realert  map[string]bool
	running  int
	freed    chan struct{}
	inflight sync.WaitGroup
}

type run struct {
	term  uint64
	held  map[string]time.Time
	ready []string
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
	kindIPSMGW
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
	outcome       outcome
	cause         *s6c.DeliveryCause
	diagnostic    *tgpp.AbsentUserDiagnostic
	rejected      bool
	status        tpdu.Status
	deliverReport []byte
}

type nodeOutcome struct {
	kind       nodeKind
	source     nodeSource
	cause      s6c.DeliveryCause
	diagnostic *tgpp.AbsentUserDiagnostic
}

// TS 23.040 clause 9.2.3.15
type result struct {
	outcome  outcome
	hold     bool
	status   tpdu.Status
	attempts []db.DeliveryAttempt
}

var errInterrupted = errors.New("short message delivery interrupted")

var anyClaim = time.Unix(0, 0)

var (
	statusCongestion      = tpdu.TemporaryCongestion.GaveUp()
	statusNoResponse      = tpdu.TemporaryNoResponse.GaveUp()
	statusSMEBusy         = tpdu.TemporarySMEBusy.GaveUp()
	statusErrorInSME      = tpdu.TemporaryErrorInSME.GaveUp()
	statusServiceRejected = tpdu.TemporaryServiceRejected.GaveUp()
)

// TS 23.040 clause 3.7
// TS 29.002 clause 23.3
type session struct {
	routing s6c.Routing
	target  target
	next    db.Message
}

func (s *session) targets() []target {
	targets := []target{s.target}

	for _, t := range deliveryTargets(s.routing.ServingNodes) {
		if !strings.EqualFold(t.name, s.target.name) {
			targets = append(targets, t)
		}
	}

	return targets
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

	d.Alerted(recipients)

	return nil
}

func (d *Deliverer) Alerted(recipients []string) {
	d.mu.Lock()

	for _, msisdn := range recipients {
		if d.busy[msisdn] {
			d.realert[msisdn] = true
		}
	}

	d.mu.Unlock()

	d.Notify()
}

func (d *Deliverer) initState() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.busy == nil {
		d.busy, d.realert, d.freed = map[string]bool{}, map[string]bool{}, make(chan struct{}, 1)
	}
}

func (d *Deliverer) Run(ctx context.Context, term uint64) {
	d.initState()

	wake := d.wakeChan()
	r := &run{term: term, held: map[string]time.Time{}}

	if !d.holdTakenOver(ctx, r) {
		return
	}

	for ctx.Err() == nil {
		wait := time.Duration(-1)
		holdUntil, holding := d.refreshHolds(ctx, r)

		if free := max(d.Concurrency, 1) - d.active(); free > 0 {
			if len(r.ready) < free {
				due, wakeup, err := d.Store.DueRecipients(ctx, d.Now(), free+max(d.Concurrency, 1), d.unavailable(r))
				if err != nil {
					d.Logger.ErrorContext(ctx, "failed to read the recipients due a delivery", slog.Any("error", err))

					wait = errorBackoff
				}

				if !wakeup.IsZero() {
					wait = max(wakeup.Sub(d.Now()), 0)
				}

				r.ready = due
			}

			for free > 0 && len(r.ready) > 0 {
				msisdn := r.ready[0]
				r.ready = r.ready[1:]

				if d.start(ctx, term, msisdn) {
					free--
				}
			}
		}

		if holding && (wait < 0 || holdUntil.Sub(d.Now()) < wait) {
			wait = max(holdUntil.Sub(d.Now()), 0)
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
		case <-d.freed:
		}

		if timer != nil {
			timer.Stop()
		}
	}
}

func (d *Deliverer) Wait(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		d.inflight.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Deliverer) holdWindow() time.Duration {
	return d.AttemptTimeout + ClaimMargin
}

// TS 23.040 clause 6.2
func (d *Deliverer) holdTakenOver(ctx context.Context, r *run) bool {
	if r.term == 0 {
		return true
	}

	for {
		now := d.Now()

		claimed, err := d.Store.ClaimedBefore(ctx, r.term, anyClaim)
		if err == nil {
			until := now.Add(d.holdWindow())

			for _, msisdn := range claimed {
				r.held[msisdn] = until
			}

			if len(claimed) > 0 {
				d.Logger.InfoContext(ctx, "holding the recipients the previous leader was delivering to",
					slog.Int("recipients", len(claimed)), slog.Time("until", until))
			}

			return true
		}

		d.Logger.ErrorContext(ctx, "failed to read the recipients of the previous leader, retrying", slog.Any("error", err))

		select {
		case <-ctx.Done():
			return false
		case <-time.After(errorBackoff):
		}
	}
}

func (d *Deliverer) refreshHolds(ctx context.Context, r *run) (time.Time, bool) {
	if len(r.held) == 0 {
		return time.Time{}, false
	}

	now := d.Now()

	claimed, err := d.Store.ClaimedBefore(ctx, r.term, anyClaim)

	still := make(map[string]bool, len(claimed))
	for _, msisdn := range claimed {
		still[msisdn] = true
	}

	var earliest time.Time

	for msisdn, until := range r.held {
		if err == nil && !still[msisdn] || !now.Before(until) {
			delete(r.held, msisdn)
			continue
		}

		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}

	return earliest, len(r.held) > 0
}

func (d *Deliverer) active() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.running
}

func (d *Deliverer) unavailable(r *run) func(string) bool {
	return func(msisdn string) bool {
		if _, held := r.held[msisdn]; held {
			return true
		}

		d.mu.Lock()
		defer d.mu.Unlock()

		return d.busy[msisdn]
	}
}

func (d *Deliverer) start(ctx context.Context, term uint64, msisdn string) bool {
	d.mu.Lock()

	if d.busy[msisdn] {
		d.mu.Unlock()
		return false
	}

	d.busy[msisdn] = true
	d.running++
	d.mu.Unlock()

	d.inflight.Go(func() {
		defer d.finish(context.WithoutCancel(ctx), msisdn)

		if err := d.drain(ctx, term, msisdn); err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(errorBackoff):
			}
		}
	})

	return true
}

func (d *Deliverer) drain(ctx context.Context, term uint64, msisdn string) error {
	m, ok, err := d.Store.NextDue(ctx, msisdn, d.Now())
	if err != nil {
		if ctx.Err() == nil {
			d.Logger.ErrorContext(ctx, "failed to read the next message to a recipient", slog.String("msisdn", msisdn),
				slog.Any("error", err))
		}

		return err
	}

	if !ok {
		return nil
	}

	return d.process(ctx, term, m)
}

func (d *Deliverer) finish(ctx context.Context, msisdn string) {
	d.mu.Lock()
	delete(d.busy, msisdn)
	d.running--
	realert := d.realert[msisdn]
	delete(d.realert, msisdn)
	d.mu.Unlock()

	select {
	case d.freed <- struct{}{}:
	default:
	}

	if !realert {
		return
	}

	if _, err := d.Store.AlertRecipient(ctx, msisdn, d.Now()); err != nil {
		d.Logger.ErrorContext(ctx, "failed to apply a service centre alert", slog.String("msisdn", msisdn), slog.Any("error", err))
	}
}

// TS 23.040 clause 6.2
func (d *Deliverer) process(stop context.Context, term uint64, m db.Message) error {
	ctx := context.WithoutCancel(stop)
	l := &lease{term: term}

	defer d.releaseLease(ctx, m.MSISDN, l)

	var s *session

	for {
		next, err := d.deliverAndSettle(stop, m, s, l)
		if err != nil {
			return err
		}

		if next == nil || stop.Err() != nil {
			return nil
		}

		m, s = next.next, next
	}
}

func (d *Deliverer) deliverAndSettle(stop context.Context, m db.Message, s *session, l *lease) (_ *session, err error) {
	destination := d.Identity().OriginHost

	stop, span := d.tracer().Start(stop, deliverOperation+" "+destination,
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithLinks(tracing.Links(m.TraceParent)...),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String(tracing.MessagingSystem),
			semconv.MessagingDestinationName(destination),
			semconv.MessagingOperationName(deliverOperation),
			semconv.MessagingOperationTypeProcess,
			semconv.MessagingMessageID(strconv.FormatInt(m.ID, 10)),
		),
	)

	defer func() {
		if err != nil && !errors.Is(err, errInterrupted) {
			d.Logger.ErrorContext(stop, "failed to record delivery outcome", slog.Int64("message_id", m.ID), slog.Any("error", err))
			span.SetAttributes(semconv.ErrorTypeOther)
			span.SetStatus(codes.Error, err.Error())
		}

		span.End()
	}()

	if l != nil {
		l.started = d.Now()
	}

	r, next := d.deliver(stop, m, d.Now(), s, l)

	if err := d.settle(context.WithoutCancel(stop), m, r, l, next != nil); err != nil {
		return nil, err
	}

	return next, nil
}

func (d *Deliverer) tracer() trace.Tracer {
	tp := d.TracerProvider
	if tp == nil {
		tp = noop.NewTracerProvider()
	}

	return tp.Tracer(tracerName, trace.WithSchemaURL(semconv.SchemaURL))
}

func settled(ctx context.Context, outcome string) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(outcomeKey.String(outcome))

	if outcome == string(db.StatusFailed) || outcome == string(db.StatusExpired) {
		span.SetAttributes(semconv.ErrorTypeKey.String(outcome))
		span.SetStatus(codes.Error, "")
	}
}

func (d *Deliverer) settle(ctx context.Context, m db.Message, r result, l *lease, keep bool) error {
	f := l.fence()
	f.Keep = keep

	settledClaim := func() {
		if l != nil && !keep {
			l.claim = db.Claim{}
		}
	}

	switch r.outcome {
	case delivered:
		return d.complete(ctx, m, db.StatusDelivered, r, d.Now(), f, settledClaim)
	case expired:
		return d.complete(ctx, m, db.StatusExpired, r, d.Now(), f, settledClaim)
	case permanent:
		return d.complete(ctx, m, db.StatusFailed, r, d.Now(), f, settledClaim)
	case interrupted:
		d.flush(ctx, r.attempts)
		settled(ctx, outcomeInterrupted)

		if ctx.Err() == nil {
			return errInterrupted
		}

		return nil
	}

	now := d.Now()

	intervals := d.Settings().Delivery.RetryIntervals
	if m.SingleShot || len(intervals) == 0 {
		return d.complete(ctx, m, db.StatusFailed, r, now, f, settledClaim)
	}

	at := now.Add(intervals[min(m.Retries, len(intervals)-1)])

	var since time.Time
	if l != nil {
		since = l.started
	}

	err := d.Store.ScheduleRetry(ctx, m.ID, at, now, r.hold, since, f, r.attempts...)
	if errors.Is(err, db.ErrStaleTerm) || errors.Is(err, db.ErrNotFound) {
		d.flush(ctx, r.attempts)
		settled(ctx, outcomeSuperseded)

		return nil
	}

	if err != nil {
		return err
	}

	settledClaim()
	settled(ctx, outcomeRetry)

	return nil
}

func (d *Deliverer) complete(ctx context.Context, m db.Message, status db.MessageStatus, r result, at time.Time,
	f db.Fence, settledClaim func(),
) error {
	reported := r.status

	report, err := statusreport.For(m, reported, at, d.Settings())
	if err != nil {
		d.Logger.ErrorContext(ctx, "failed to make the status report on a short message", slog.Int64("message_id", m.ID),
			slog.Any("error", err))
	}

	err = d.Store.CompleteMessage(ctx, m.ID, status, at, report, uint8(reported), f, r.attempts...)
	if errors.Is(err, db.ErrStaleTerm) || errors.Is(err, db.ErrNotPending) || errors.Is(err, db.ErrNotFound) {
		d.flush(ctx, r.attempts)
	}

	if errors.Is(err, db.ErrStaleTerm) {
		d.Logger.DebugContext(ctx, "a newer leader term took the recipient over", slog.Int64("message_id", m.ID))
		settled(ctx, outcomeSuperseded)

		return nil
	}

	if errors.Is(err, db.ErrNotPending) {
		d.Logger.DebugContext(ctx, "short message replaced during its delivery", slog.Int64("message_id", m.ID),
			slog.String("status", string(status)))
		settled(ctx, string(db.StatusReplaced))

		return nil
	}

	if err != nil {
		return err
	}

	settledClaim()
	settled(ctx, string(status))

	if status == db.StatusDelivered {
		d.Metrics.delivered(m, at)
	}

	return nil
}

func (d *Deliverer) deliver(stop context.Context, m db.Message, now time.Time, s *session, l *lease) (result, *session) {
	ctx := context.WithoutCancel(stop)
	log := d.Logger.With(slog.Int64("message_id", m.ID))

	if !now.Before(m.ExpiresAt) {
		log.DebugContext(ctx, "short message expired before delivery")
		return result{outcome: expired, status: tpdu.StatusValidityPeriodExpired}, nil
	}

	var (
		routing s6c.Routing
		targets []target
	)

	if s != nil {
		routing, targets = s.routing, s.targets()
	} else {
		started := d.Now()
		sri, cancel := context.WithTimeout(stop, d.AttemptTimeout)

		var (
			hss string
			err error
		)

		routing, hss, err = d.Router.SendRoutingInfoForSM(sri, d.routingRequest(ctx, log, m))

		cancel()

		if err != nil && stop.Err() != nil {
			log.DebugContext(ctx, "short message routing lookup interrupted by shutdown")
			return result{outcome: interrupted}, nil
		}

		routingAttempt := routingAttemptOf(routing, err)
		routingAttempt.Step, routingAttempt.Node, routingAttempt.StartedAt = db.StepRouting, hss, started

		if err == nil && len(deliveryTargets(routing.ServingNodes)) > 0 {
			if err := d.recordAndClaim(ctx, log, m, routingAttempt, l, &routing.AlertMSISDN); err != nil {
				return d.unclaimed(ctx, log, err), nil
			}
		} else {
			d.record(ctx, log, m.ID, routingAttempt)
		}

		if err != nil {
			log.DebugContext(ctx, "routing lookup for short message failed", slog.Any("error", err))

			return d.routingFailed(ctx, m, err), nil
		}

		if len(deliveryTargets(routing.ServingNodes)) == 0 {
			d.setAlertMSISDN(ctx, m.MSISDN, routing.AlertMSISDN)
		}

		targets = deliveryTargets(routing.ServingNodes)
	}

	deliver, err := buildDeliver(m)
	if err != nil {
		log.ErrorContext(ctx, "stored short message cannot be turned into a TPDU to deliver", slog.Any("error", err))
		return result{outcome: permanent, status: tpdu.StatusRemoteProcedureError}, nil
	}

	if len(targets) == 0 {
		log.DebugContext(ctx, "no serving node reachable over SGd for the recipient")
		return result{outcome: permanent, status: tpdu.StatusNoInterworking}, nil
	}

	following, more, err := d.Store.NextDueFor(ctx, m.MSISDN, m.ID, d.Now())
	if err != nil {
		log.ErrorContext(ctx, "failed to look for more messages to the recipient", slog.Any("error", err))
	}

	a := d.attempt(stop, log, m, routing.IMSI, targets, deliver, more, l)

	if a.outcome == interrupted {
		log.DebugContext(ctx, "short message delivery interrupted by shutdown")
		return result{outcome: interrupted}, nil
	}

	if a.outcome == delivered {
		reported := true

		if success := a.successReport(); routing.MWDStatus&mwdStatusFlags != 0 || len(success) > 1 {
			_, reported = d.report(stop, log, m, success, nil)
		}

		return result{outcome: delivered, status: tpdu.StatusReceived, attempts: a.pending}, follow(routing, a.via, following,
			more && reported)
	}

	if a.outcome == permanent {
		return result{outcome: permanent, status: a.status, attempts: a.pending}, nil
	}

	if len(a.failures()) == 0 {
		return result{outcome: temporary, hold: !a.rejected, status: a.status, attempts: a.pending}, nil
	}

	if !needsReport(a.failures(), routing) {
		return result{outcome: temporary, hold: true, status: a.status, attempts: a.pending}, nil
	}

	alert, ok := d.report(stop, log, m, a.outcomes, &routing.ServingNodes)
	if !ok {
		return result{outcome: temporary, hold: true, status: a.status, attempts: a.pending}, nil
	}

	retryTargets := deliveryTargets(alert.ServingNodes)
	if len(retryTargets) == 0 || stop.Err() != nil {
		return result{outcome: temporary, hold: true, status: a.status, attempts: a.pending}, nil
	}

	d.flush(ctx, a.pending)

	log.DebugContext(ctx, "retrying delivery via the serving nodes the HSS reported")

	retry := d.attempt(stop, log, m, routing.IMSI, retryTargets, deliver, more, l)

	switch retry.outcome {
	case delivered:
		_, reported := d.report(stop, log, m, retry.successReport(), nil)
		routing.ServingNodes = alert.ServingNodes

		return result{outcome: delivered, status: tpdu.StatusReceived, attempts: retry.pending}, follow(routing, retry.via,
			following, more && reported)
	case permanent:
		return result{outcome: permanent, status: retry.status, attempts: retry.pending}, nil
	default:
		return result{outcome: temporary, hold: true, status: retry.status, attempts: retry.pending}, nil
	}
}

func follow(routing s6c.Routing, via target, next db.Message, announced bool) *session {
	if !announced {
		return nil
	}

	routing.MWDStatus &^= mwdStatusFlags
	routing.Absent = s6c.AbsentUserDiagnostics{}

	return &session{routing: routing, target: via, next: next}
}

// TS 23.040 clause 3.3 table 1
func (d *Deliverer) routingFailed(ctx context.Context, m db.Message, err error) result {
	if re := (*s6c.ResultError)(nil); errors.As(err, &re) && re.AlertMSISDN != "" {
		d.setAlertMSISDN(ctx, m.MSISDN, re.AlertMSISDN)
	}

	for code, status := range map[uint32]tpdu.Status{
		tgpp.ResultErrorUserUnknown:          tpdu.StatusNotObtainable,
		tgpp.ResultErrorServiceNotSubscribed: tpdu.StatusConnectionRejected,
	} {
		if tgpp.IsExperimental(err, code) {
			return result{outcome: permanent, status: status}
		}
	}

	switch {
	case tgpp.IsExperimental(err, tgpp.ResultErrorAbsentUser):
		return result{outcome: temporary, hold: true, status: statusNoResponse}
	case tgpp.IsExperimental(err, tgpp.ResultErrorServiceBarred), tgpp.IsExperimental(err, tgpp.ResultErrorFacilityNotSupported):
		return result{outcome: temporary, hold: true, status: statusServiceRejected}
	}

	return result{outcome: temporary, hold: true, status: statusCongestion}
}

// TS 29.338 clause 5.3.3.2
func (d *Deliverer) routingRequest(ctx context.Context, log *slog.Logger, m db.Message) s6c.RoutingRequest {
	req := s6c.RoutingRequest{
		MSISDN:        m.MSISDN,
		MTI:           s6c.SMRPMTIDeliver,
		SMEA:          d.originatingSME(ctx, log, m),
		SingleAttempt: m.SingleShot,
	}

	if m.Kind == db.KindStatusReport {
		req.MTI = s6c.SMRPMTIStatusReport
	}

	return req
}

// TS 23.040 clause 9.3.2.2
// TS 29.338 clause 5.2.1.1
func (d *Deliverer) originatingSME(ctx context.Context, log *slog.Logger, m db.Message) []byte {
	smea, err := tpdu.Address{
		TypeOfNumber:  m.Originator.TypeOfNumber,
		NumberingPlan: m.Originator.NumberingPlan,
		Digits:        m.Originator.Digits,
	}.Encode()
	if err != nil {
		log.ErrorContext(ctx, "cannot encode the originator for the routing lookup", slog.Any("error", err))
		return nil
	}

	return smea
}

func (d *Deliverer) setAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string) {
	if err := d.Store.SetAlertMSISDN(ctx, msisdn, alertMSISDN, d.Now()); err != nil {
		d.Logger.ErrorContext(ctx, "failed to record the Alert MSISDN", slog.String("msisdn", msisdn), slog.Any("error", err))
	}
}

type attemptResult struct {
	pending   []db.DeliveryAttempt
	outcome   outcome
	outcomes  []nodeOutcome
	via       target
	rejected  bool
	status    tpdu.Status
	hasStatus bool
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
	deliver func(bool) ([]byte, error), more bool, l *lease,
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

		if err := d.ensureClaim(ctx, m, l); err != nil {
			unclaimed := d.unclaimed(ctx, log, err)
			a.outcome, a.status, a.hasStatus = unclaimed.outcome, unclaimed.status, true

			return a
		}

		started := d.Now()
		r, err := d.forward(ctx, imsi, t, deliver, more)

		deliveryAttempt := deliveryAttemptOf(t.kind, err)
		if len(r.deliverReport) > 0 {
			deliveryAttempt.DeliverReport = r.deliverReport
		}

		deliveryAttempt.Step, deliveryAttempt.Node, deliveryAttempt.StartedAt = db.StepDelivery, t.name, started

		if continues := r.outcome != delivered && r.outcome != permanent && i+1 < len(targets) && stop.Err() == nil; continues {
			if err := d.recordAndClaim(ctx, log, m, deliveryAttempt, l, nil); err != nil {
				unclaimed := d.unclaimed(ctx, log, err)
				a.outcome, a.status, a.hasStatus = unclaimed.outcome, unclaimed.status, true

				return a
			}
		} else {
			a.pending = append(a.pending, d.pending(ctx, log, m.ID, deliveryAttempt))
		}

		if r.cause != nil {
			a.outcomes = append(a.outcomes, nodeOutcome{kind: t.kind, source: t.source, cause: *r.cause, diagnostic: r.diagnostic})
			retry = true
		}

		a.rejected = a.rejected || r.rejected

		if r.outcome != delivered && (!a.hasStatus || r.outcome == permanent) {
			a.status, a.hasStatus = r.status, true
		}

		switch r.outcome {
		case delivered:
			a.outcome, a.via = delivered, t
			return a
		case permanent:
			a.outcome = permanent
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
	case kindIPSMGW:
		return 0
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
	case kindIPSMGW:
		return nil
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
		case kindIPSMGW:
			slot = &rep.IPSMGW
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
		log.DebugContext(ctx, "failed to report delivery status to the HSS", slog.Any("error", err))
		return s6c.ReportResult{}, false
	}

	if res.AlertMSISDN != "" {
		d.setAlertMSISDN(ctx, m.MSISDN, res.AlertMSISDN)
	}

	return res, true
}

func buildDeliver(m db.Message) (func(more bool) ([]byte, error), error) {
	if m.Kind == db.KindStatusReport {
		return func(more bool) ([]byte, error) {
			return tpdu.SetMoreMessagesToSend(m.TPDU, more), nil
		}, nil
	}

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
		deliver := tpdu.DeliverFromSubmit(submit, originator, m.TimeStamp)
		deliver.MoreMessagesToSend = more
		deliver.StatusReportIndication = m.Origin == db.OriginMobile && submit.StatusReportRequest

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

		if sn.node.IPSMGW != nil {
			add(target{kind: kindIPSMGW, source: sn.source, name: sn.node.IPSMGW.Name, realm: sn.node.IPSMGW.Realm})
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
		return forwardResult{outcome: permanent, status: tpdu.StatusRemoteProcedureError}, err
	}

	realm := t.realm
	if realm == "" {
		var ok bool
		if realm, ok = d.Sender.PeerRealm(t.name); !ok {
			realm = d.Identity().OriginRealm
		}
	}

	tfr, err := sgd.NewMTForwardShortMessageRequest(tgpp.Envelope{
		SessionID:        d.Sender.NewSessionID(),
		Origin:           d.Identity(),
		DestinationHost:  t.name,
		DestinationRealm: realm,
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
		return forwardResult{outcome: permanent, status: tpdu.StatusRemoteProcedureError}, err
	}

	attempt, cancel := context.WithTimeout(ctx, d.AttemptTimeout)
	defer cancel()

	ans, err := d.Sender.Do(attempt, t.name, tfr)
	if err != nil {
		return forwardResult{outcome: temporary, status: statusCongestion}, err
	}

	answer, err := sgd.ParseMTForwardShortMessageAnswer(ans)

	r := classifyForwardAnswer(err)
	if err == nil {
		r.deliverReport = answer.SMRPUI
	}

	return r, err
}

func classifyForwardAnswer(err error) forwardResult {
	if err == nil {
		return forwardResult{outcome: delivered, cause: ptr(s6c.DeliveryCauseSuccessfulTransfer)}
	}

	var re *sgd.ResultError
	if !errors.As(err, &re) {
		return forwardResult{outcome: temporary, status: statusCongestion}
	}

	code := re.Code

	if !re.Experimental {
		if code >= 5000 && code < 6000 {
			return forwardResult{outcome: targetFailed, status: statusCongestion}
		}

		return forwardResult{outcome: temporary, status: statusCongestion}
	}

	if re.VendorID != tgpp.VendorID {
		return forwardResult{outcome: temporary, status: statusCongestion}
	}

	switch code {
	case tgpp.ResultErrorUserUnknown:
		return forwardResult{outcome: targetFailed, cause: ptr(s6c.DeliveryCauseAbsentUser), status: statusNoResponse}
	case tgpp.ResultErrorAbsentUser:
		return forwardResult{
			outcome: temporary, cause: ptr(s6c.DeliveryCauseAbsentUser), diagnostic: re.AbsentUserDiagnostic,
			status: statusNoResponse,
		}
	case tgpp.ResultErrorUserBusyForMTSMS:
		return forwardResult{outcome: temporary, status: statusSMEBusy}
	case tgpp.ResultErrorIllegalUser, tgpp.ResultErrorIllegalEquipment:
		return forwardResult{outcome: permanent, status: tpdu.StatusConnectionRejected}
	case tgpp.ResultErrorSMDeliveryFailure:
		cause := re.DeliveryFailureCause

		if fcs, ok := tpdu.DeliverReportFailureCause(re.DiagnosticInfo); ok && fcs == tpdu.FailureUSIMDataDownloadError {
			return forwardResult{outcome: permanent, status: tpdu.StatusConnectionRejected}
		}

		switch {
		case cause != nil && *cause == sgd.CauseEquipmentNotSMEquipped:
			return forwardResult{outcome: temporary, status: statusServiceRejected}
		case cause != nil && *cause == sgd.CauseMemoryCapacityExceeded:
			return forwardResult{outcome: temporary, cause: ptr(s6c.DeliveryCauseMemoryCapacityExceeded), status: statusErrorInSME}
		case cause != nil && *cause == sgd.CauseEquipmentProtocolError:
			if rejectsMessage(re.DiagnosticInfo) {
				return forwardResult{outcome: temporary, rejected: true, status: statusServiceRejected}
			}

			return forwardResult{outcome: temporary, status: statusErrorInSME}
		default:
			return forwardResult{outcome: temporary, status: statusErrorInSME}
		}
	case tgpp.ResultErrorFacilityNotSupported:
		return forwardResult{outcome: temporary, status: statusServiceRejected}
	default:
		return forwardResult{outcome: temporary, status: statusCongestion}
	}
}

// TS 23.040 clause 3.3.2
// TS 23.040 clause 9.2.3.22
// TS 23.040 clause 11.1
func rejectsMessage(diagnostic []byte) bool {
	fcs, ok := tpdu.DeliverReportFailureCause(diagnostic)
	if !ok {
		return false
	}

	switch fcs {
	case tpdu.FailureUSIMStorageFull, tpdu.FailureErrorInMS, tpdu.FailureMemoryCapacityExceeded, tpdu.FailureUSIMToolkitBusy:
		return false
	default:
		return true
	}
}

func ptr[T any](v T) *T {
	return &v
}
