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
	tracerName       = "github.com/ellanetworks/smsc/internal/delivery"
	deliverOperation = "deliver"
	// outcomeKey is the outcome of a delivery attempt: the status it gave the message, replaced when a later message
	// replaced it meanwhile, or outcomeRetry or outcomeInterrupted when it stays pending.
	outcomeKey         = attribute.Key("ella.sms.delivery.outcome")
	outcomeRetry       = "retry"
	outcomeInterrupted = "interrupted"
)

const (
	DefaultAttemptTimeout = 30 * time.Second
	DefaultConcurrency    = 20
)

const mwdStatusFlags = s6c.MWDStatusMNRF | s6c.MWDStatusMCEF | s6c.MWDStatusMNRG | s6c.MWDStatusMNR5G | s6c.MWDStatusMNR5GN3G

type Store interface {
	NextDue(ctx context.Context, now time.Time, busy []string) (db.Message, bool, error)
	NextWakeup(ctx context.Context, busy []string) (time.Time, bool, error)
	CompleteMessage(ctx context.Context, id int64, status db.MessageStatus, at time.Time, report *db.NewStatusReport,
		tpStatus uint8) error
	ScheduleRetry(ctx context.Context, id int64, at, now time.Time, hold bool) error
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
	// PeerRealm returns the Origin-Realm of the connected peer with the given host.
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
	// TracerProvider traces the delivery attempts; none when nil.
	TracerProvider trace.TracerProvider
	// Completed, when set, is called after each message gets its final status, which may have queued SMPP
	// notifications on it.
	Completed func()

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
	outcome    outcome
	cause      *s6c.DeliveryCause
	diagnostic *tgpp.AbsentUserDiagnostic
	// rejected is set when the MS refused this message, rather than being unable to take any message for now.
	rejected bool
	// status is the TP-Status of a status report on an attempt that did not deliver the message.
	status tpdu.Status
	// deliverReport is the SMS-DELIVER-REPORT for RP-ACK that the serving node passed on, if any.
	deliverReport []byte
}

type nodeOutcome struct {
	kind       nodeKind
	source     nodeSource
	cause      s6c.DeliveryCause
	diagnostic *tgpp.AbsentUserDiagnostic
}

// result is the outcome of a delivery attempt, and the TP-Status of a status report on it (TS 23.040 clause
// 9.2.3.15). The status of a temporary error is the one for once the SC makes no more transfer attempts.
type result struct {
	outcome outcome
	hold    bool
	status  tpdu.Status
}

// The statuses of the outcomes of delivery attempts, which the SC reports to the originator of a message.
var (
	statusCongestion      = tpdu.TemporaryCongestion.GaveUp()
	statusNoResponse      = tpdu.TemporaryNoResponse.GaveUp()
	statusSMEBusy         = tpdu.TemporarySMEBusy.GaveUp()
	statusErrorInSME      = tpdu.TemporaryErrorInSME.GaveUp()
	statusServiceRejected = tpdu.TemporaryServiceRejected.GaveUp()
)

// session is what the delivery of a message to a recipient leaves for the next one: the serving node that took it, and
// what the HSS said about the recipient. When the SC announces more messages to send, the serving node keeps the MS
// connected for them (TS 23.040 clause 3.7), so the SC sends them there in turn, without another routing lookup, as
// in the multiple short message transfer of TS 29.002 clause 23.3.
type session struct {
	routing s6c.Routing
	target  target
}

// targets returns the serving nodes to try for the next message: the one that took the previous message, then the
// others the HSS gave, as a routing lookup would.
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

// process delivers the message, then the recipient's next messages for as long as each delivery announced the next.
// The recipient stays busy throughout, so it never has more than one SMS-DELIVER outstanding (TS 23.040 clause 6.2).
func (d *Deliverer) process(stop context.Context, m db.Message) error {
	ctx := context.WithoutCancel(stop)

	var s *session

	for {
		next, err := d.deliverAndSettle(stop, m, s)
		if err != nil {
			return err
		}

		if next == nil || stop.Err() != nil {
			return nil
		}

		n, ok, err := d.Store.NextDueFor(ctx, m.MSISDN, m.ID, d.Now())
		if err != nil || !ok {
			return err
		}

		m, s = n, next
	}
}

// deliverAndSettle makes one delivery attempt of the message and stores its outcome, in a span of the attempt: a
// messaging process span, which starts a trace of its own and links to the request that created the message, since
// the attempts of a message can span days.
func (d *Deliverer) deliverAndSettle(stop context.Context, m db.Message, s *session) (_ *session, err error) {
	stop, span := d.tracer().Start(stop, deliverOperation,
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithLinks(tracing.Links(m.TraceParent)...),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String(tracing.MessagingSystem),
			// The SMSC has no destination but the recipient, which spans must not carry: the name identifies the SMSC.
			semconv.MessagingDestinationName(d.Identity().OriginHost),
			semconv.MessagingOperationName(deliverOperation),
			semconv.MessagingOperationTypeProcess,
			semconv.MessagingMessageID(strconv.FormatInt(m.ID, 10)),
		),
	)

	defer func() {
		if err != nil {
			span.SetAttributes(semconv.ErrorTypeOther)
			span.SetStatus(codes.Error, err.Error())
		}

		span.End()
	}()

	r, next := d.deliver(stop, m, d.Now(), s)

	if err := d.settle(context.WithoutCancel(stop), m, r); err != nil {
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

// settled sets the outcome of the delivery attempt on its span: an error when the message failed or expired.
func settled(ctx context.Context, outcome string) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(outcomeKey.String(outcome))

	if outcome == string(db.StatusFailed) || outcome == string(db.StatusExpired) {
		span.SetAttributes(semconv.ErrorTypeKey.String(outcome))
		span.SetStatus(codes.Error, "")
	}
}

// settle stores the outcome of a delivery. A message that is to be retried holds its recipient's other messages until
// the retry, so that they keep their order, unless the MS refused that message itself.
func (d *Deliverer) settle(ctx context.Context, m db.Message, r result) error {
	switch r.outcome {
	case delivered:
		return d.complete(ctx, m, db.StatusDelivered, r.status, d.Now())
	case expired:
		return d.complete(ctx, m, db.StatusExpired, r.status, d.Now())
	case permanent:
		return d.complete(ctx, m, db.StatusFailed, r.status, d.Now())
	case interrupted:
		settled(ctx, outcomeInterrupted)
		return nil
	}

	now := d.Now()

	intervals := d.Settings().Delivery.RetryIntervals
	if m.SingleShot || len(intervals) == 0 {
		return d.complete(ctx, m, db.StatusFailed, r.status, now)
	}

	at := now.Add(intervals[min(m.Retries, len(intervals)-1)])

	if err := d.Store.ScheduleRetry(ctx, m.ID, at, now, r.hold); err != nil {
		return err
	}

	settled(ctx, outcomeRetry)

	return nil
}

// complete sets the final status of a message, with the status report on it that its originator asked for, if any. It
// times a delivered message once its status is stored, so that a message whose status failed to be stored, which stays
// pending, is not timed twice. A message that a later one replaced during its delivery keeps its status.
func (d *Deliverer) complete(ctx context.Context, m db.Message, status db.MessageStatus, reported tpdu.Status, at time.Time) error {
	report, err := statusreport.For(m, reported, at, d.Settings())
	if err != nil {
		d.Logger.Error("failed to make the status report on a short message", slog.Int64("message_id", m.ID),
			slog.Any("error", err))
	}

	err = d.Store.CompleteMessage(ctx, m.ID, status, at, report, uint8(reported))
	if errors.Is(err, db.ErrNotPending) {
		d.Logger.Debug("short message replaced during its delivery", slog.Int64("message_id", m.ID),
			slog.String("status", string(status)))
		settled(ctx, string(db.StatusReplaced))

		return nil
	}

	if err != nil {
		return err
	}

	settled(ctx, string(status))

	if status == db.StatusDelivered {
		d.Metrics.delivered(m, at)
	}

	if d.Completed != nil {
		d.Completed()
	}

	return nil
}

// deliver makes one delivery attempt of the message. Within a session it goes to the serving node that took the
// previous message; otherwise it starts with a routing lookup. It returns the session for the recipient's next message
// when the delivery announced one.
func (d *Deliverer) deliver(stop context.Context, m db.Message, now time.Time, s *session) (result, *session) {
	ctx := context.WithoutCancel(stop)
	log := d.Logger.With(slog.Int64("message_id", m.ID))

	if !now.Before(m.ExpiresAt) {
		log.Debug("short message expired before delivery")
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

		routing, hss, err = d.Router.SendRoutingInfoForSM(sri, d.routingRequest(log, m))

		cancel()

		if err != nil && stop.Err() != nil {
			log.Debug("short message routing lookup interrupted by shutdown")
			return result{outcome: interrupted}, nil
		}

		routingAttempt := routingAttemptOf(routing, err)
		routingAttempt.Step, routingAttempt.Node, routingAttempt.StartedAt = db.StepRouting, hss, started

		d.record(ctx, log, m.ID, routingAttempt)

		if err != nil {
			log.Debug("routing lookup for short message failed", slog.Any("error", err))

			return d.routingFailed(ctx, m, err), nil
		}

		d.setAlertMSISDN(ctx, m.MSISDN, routing.AlertMSISDN)

		targets = deliveryTargets(routing.ServingNodes)
	}

	deliver, err := buildDeliver(m)
	if err != nil {
		log.Error("stored short message cannot be turned into a TPDU to deliver", slog.Any("error", err))
		return result{outcome: permanent, status: tpdu.StatusRemoteProcedureError}, nil
	}

	if len(targets) == 0 {
		log.Debug("no serving node reachable over SGd for the recipient")
		return result{outcome: permanent, status: tpdu.StatusNoInterworking}, nil
	}

	// More messages are to send when another one is due now, which the SC sends right after this one.
	_, more, err := d.Store.NextDueFor(ctx, m.MSISDN, m.ID, d.Now())
	if err != nil {
		log.Error("failed to look for more messages to the recipient", slog.Any("error", err))
	}

	a := d.attempt(stop, log, m, routing.IMSI, targets, deliver, more)

	if a.outcome == interrupted {
		log.Debug("short message delivery interrupted by shutdown")
		return result{outcome: interrupted}, nil
	}

	if a.outcome == delivered {
		reported := true

		if success := a.successReport(); routing.MWDStatus&mwdStatusFlags != 0 || len(success) > 1 {
			_, reported = d.report(stop, log, m, success, nil)
		}

		return result{outcome: delivered, status: tpdu.StatusReceived}, follow(routing, a.via, more && reported)
	}

	if a.outcome == permanent {
		return result{outcome: permanent, status: a.status}, nil
	}

	if len(a.failures()) == 0 {
		return result{outcome: temporary, hold: !a.rejected, status: a.status}, nil
	}

	if !needsReport(a.failures(), routing) {
		return result{outcome: temporary, hold: true, status: a.status}, nil
	}

	alert, ok := d.report(stop, log, m, a.outcomes, &routing.ServingNodes)
	if !ok {
		return result{outcome: temporary, hold: true, status: a.status}, nil
	}

	retryTargets := deliveryTargets(alert.ServingNodes)
	if len(retryTargets) == 0 || stop.Err() != nil {
		return result{outcome: temporary, hold: true, status: a.status}, nil
	}

	log.Debug("retrying delivery via the serving nodes the HSS reported")

	retry := d.attempt(stop, log, m, routing.IMSI, retryTargets, deliver, more)

	switch retry.outcome {
	case delivered:
		_, reported := d.report(stop, log, m, retry.successReport(), nil)
		routing.ServingNodes = alert.ServingNodes

		return result{outcome: delivered, status: tpdu.StatusReceived}, follow(routing, retry.via, more && reported)
	case permanent:
		return result{outcome: permanent, status: retry.status}, nil
	default:
		return result{outcome: temporary, hold: true, status: retry.status}, nil
	}
}

// follow returns the session for the recipient's next message after a delivery via the given serving node, or nil
// when the delivery announced no more messages. It also returns nil when the HSS may still hold message waiting data
// that the delivery should have cleared, since the HSS then needs to hear of the next delivery too: the next message
// starts over with a routing lookup, whose answer tells.
func follow(routing s6c.Routing, via target, announced bool) *session {
	if !announced {
		return nil
	}

	// The HSS holds no message waiting flags once a delivery succeeds: they were either clear, or the success report
	// cleared them.
	routing.MWDStatus &^= mwdStatusFlags
	routing.Absent = s6c.AbsentUserDiagnostics{}

	return &session{routing: routing, target: via}
}

func (d *Deliverer) routingFailed(ctx context.Context, m db.Message, err error) result {
	if re := (*s6c.ResultError)(nil); errors.As(err, &re) && re.AlertMSISDN != "" {
		d.setAlertMSISDN(ctx, m.MSISDN, re.AlertMSISDN)
	}

	// An unknown subscriber, and one without SMS, are permanent errors; barring, and SMS not provided in the visited
	// network, are temporary (TS 23.040 clause 3.3, table 1).
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

// routingRequest returns the routing lookup for the delivery of a message or a status report, which the HSS tells
// apart by SM-RP-MTI (TS 29.338 clause 5.3.3.2).
func (d *Deliverer) routingRequest(log *slog.Logger, m db.Message) s6c.RoutingRequest {
	req := s6c.RoutingRequest{
		MSISDN:        m.MSISDN,
		MTI:           s6c.SMRPMTIDeliver,
		SMEA:          d.originatingSME(log, m),
		SingleAttempt: m.SingleShot,
	}

	if m.Kind == db.KindStatusReport {
		req.MTI = s6c.SMRPMTIStatusReport
	}

	return req
}

// originatingSME returns the SM-RP-SMEA of the routing lookup: the TP-OA of the SMS-DELIVER (TS 23.040 clause 9.3.2.2).
// The SC asks the HSS for both an MME and an SGSN, so it must send it (TS 29.338 clause 5.2.1.1). An SMS-STATUS-REPORT
// has no TP-OA: its originator is the SC, whose address the report's message holds.
func (d *Deliverer) originatingSME(log *slog.Logger, m db.Message) []byte {
	smea, err := tpdu.Address{
		TypeOfNumber:  m.Originator.TypeOfNumber,
		NumberingPlan: m.Originator.NumberingPlan,
		Digits:        m.Originator.Digits,
	}.Encode()
	if err != nil {
		log.Error("cannot encode the originator for the routing lookup", slog.Any("error", err))
		return nil
	}

	return smea
}

func (d *Deliverer) setAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string) {
	if err := d.Store.SetAlertMSISDN(ctx, msisdn, alertMSISDN, d.Now()); err != nil {
		d.Logger.Error("failed to record the Alert MSISDN", slog.String("msisdn", msisdn), slog.Any("error", err))
	}
}

type attemptResult struct {
	outcome  outcome
	outcomes []nodeOutcome
	// via is the serving node that took the message, when it was delivered.
	via target
	// rejected is set when an MS refused the message itself.
	rejected bool
	// status is the TP-Status of the first serving node that did not take the message, or of the one that refused it
	// for good.
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
		if len(r.deliverReport) > 0 {
			deliveryAttempt.DeliverReport = r.deliverReport
		}

		deliveryAttempt.Step, deliveryAttempt.Node, deliveryAttempt.StartedAt = db.StepDelivery, t.name, started

		d.record(ctx, log, m.ID, deliveryAttempt)

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
		// MWD-Status has no flag for the UNRI (TS 29.338 clause 5.3.3.8).
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
		log.Debug("failed to report delivery status to the HSS", slog.Any("error", err))
		return s6c.ReportResult{}, false
	}

	if res.AlertMSISDN != "" {
		d.setAlertMSISDN(ctx, m.MSISDN, res.AlertMSISDN)
	}

	return res, true
}

// buildDeliver returns the TPDU to deliver for a message, given whether more messages wait for the recipient: the
// SMS-DELIVER of a short message, or the SMS-STATUS-REPORT that the SC stored.
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
		// The SC returns status reports on the short messages that an MS submits, when asked (TS 23.040 clause 3.2.9).
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

		// The IP-SM-GW takes the message as an MME or SGSN would, and selects the domain to deliver it in (TS 23.204
		// clause 6.4).
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

	// The HSS gives an IP-SM-GW without its realm (TS 29.338 clause 5.3.3.6). That is the realm of the IP-SM-GW
	// when it is connected, and else the SMSC's own: the IP-SM-GW is an application server of the home IMS (TS
	// 23.204), reached through the route to the home realm.
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

		// The (U)SIM processed a (U)SIM data download message and answered with an error (TS 23.040 clause 9.2.3.22).
		// Sending the same message again would get the same answer.
		if fcs, ok := tpdu.DeliverReportFailureCause(re.DiagnosticInfo); ok && fcs == tpdu.FailureUSIMDataDownloadError {
			return forwardResult{outcome: permanent, status: tpdu.StatusConnectionRejected}
		}

		switch {
		case cause != nil && *cause == sgd.CauseEquipmentNotSMEquipped:
			// SMS lower layers capabilities not provisioned is a temporary error (TS 23.040 clause 3.3, table 1).
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

// rejectsMessage tells whether an MS that answered with a protocol error refused the message itself, rather than being
// unable to take any message for now. The TP-Failure-Cause of its SMS-DELIVER-REPORT says which (TS 23.040 clause
// 9.2.3.22); without one, the error is about the message.
func rejectsMessage(diagnostic []byte) bool {
	fcs, ok := tpdu.DeliverReportFailureCause(diagnostic)
	if !ok {
		return true
	}

	switch fcs {
	case tpdu.FailureUSIMStorageFull, tpdu.FailureMemoryCapacityExceeded, tpdu.FailureUSIMToolkitBusy:
		return false
	default:
		return true
	}
}

func ptr[T any](v T) *T {
	return &v
}
