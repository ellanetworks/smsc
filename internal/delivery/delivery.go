package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/sgd"
	"github.com/ellanetworks/smsc/internal/tbcd"
	"github.com/ellanetworks/smsc/internal/tgpp"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

const errorBackoff = time.Second

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
	CreateDeliveryAttempt(ctx context.Context, messageID int64, servingNode string, resultCode uint32, attemptedAt time.Time) (int64, error)
}

type Router interface {
	SendRoutingInfoForSM(ctx context.Context, req s6c.Request) (s6c.Routing, error)
	ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error)
}

type Sender interface {
	Do(ctx context.Context, peerHost string, req *diameter.Message) (*diameter.Message, error)
	NewSessionID() string
}

type Deliverer struct {
	Store                Store
	Router               Router
	Sender               Sender
	Identity             diameter.Identity
	ServiceCentreAddress string
	RetryIntervals       []time.Duration
	AttemptTimeout       time.Duration
	Concurrency          int
	Now                  func() time.Time
	Logger               *slog.Logger

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
	kind        nodeKind
	source      nodeSource
	name        string
	realm       string
	numberCode  uint32
	numberValue string
}

type forwardResult struct {
	outcome    outcome
	code       uint32
	cause      *uint32
	diagnostic *uint32
}

type nodeOutcome struct {
	kind       nodeKind
	source     nodeSource
	cause      uint32
	diagnostic *uint32
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

	if m.SingleShot || len(d.RetryIntervals) == 0 {
		return d.Store.SetMessageStatus(ctx, m.ID, db.StatusFailed, now)
	}

	at := now.Add(d.RetryIntervals[min(m.Retries, len(d.RetryIntervals)-1)])

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
		log.Info("short message expired before delivery")
		return result{outcome: expired}
	}

	sri, cancel := context.WithTimeout(ctx, d.AttemptTimeout)
	routing, err := d.Router.SendRoutingInfoForSM(sri, s6c.Request{MSISDN: m.MSISDN, SingleAttempt: m.SingleShot})

	cancel()

	if err != nil {
		log.Info("routing lookup for short message failed", slog.Any("error", err))

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
		log.Info("no serving node reachable over SGd for the recipient")
		return result{outcome: permanent}
	}

	more, err := d.Store.CountPendingFor(ctx, m.MSISDN, m.ID)
	if err != nil {
		log.Error("failed to count pending messages", slog.Any("error", err))
	}

	a := d.attempt(stop, log, m, routing.IMSI, targets, deliver, more > 0)

	if a.outcome == interrupted {
		log.Info("short message delivery interrupted by shutdown")
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

	log.Info("retrying delivery via the serving nodes the HSS reported")

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
		if s6c.IsExperimental(err, code) {
			return result{outcome: permanent}
		}
	}

	return result{outcome: temporary, hold: s6c.IsExperimental(err, tgpp.ResultErrorAbsentUser)}
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

		r := d.forward(ctx, imsi, t, deliver, more)

		if _, err := d.Store.CreateDeliveryAttempt(ctx, m.ID, t.name, r.code, d.Now()); err != nil {
			log.Error("failed to record delivery attempt", slog.Any("error", err))
		}

		log.Info("short message delivery attempt", slog.String("serving_node", t.name), slog.Uint64("result_code", uint64(r.code)))

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

func mwdFlag(o nodeOutcome) uint32 {
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

func hssDiagnostic(a s6c.AbsentUserDiagnostics, kind nodeKind) *uint32 {
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
		log.Warn("failed to report delivery status to the HSS", slog.Any("error", err))
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
				numberCode: tgpp.AVPMMENumberForMTSMS, numberValue: sn.node.MME.Number,
			})
		}

		if sn.node.SGSN != nil {
			add(target{
				kind: kindSGSN, source: sn.source, name: sn.node.SGSN.Name, realm: sn.node.SGSN.Realm,
				numberCode: tgpp.AVPSGSNNumber, numberValue: sn.node.SGSN.Number,
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

func (d *Deliverer) forward(ctx context.Context, imsi string, t target, deliver func(bool) ([]byte, error), more bool) forwardResult {
	smRPUI, err := deliver(more)
	if err != nil {
		return forwardResult{outcome: permanent}
	}

	scAddress, err := tbcd.Encode(d.ServiceCentreAddress)
	if err != nil {
		return forwardResult{outcome: permanent}
	}

	start := d.Now()

	avps := []diameter.AVP{
		diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, d.Sender.NewSessionID()),
		diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained),
		diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, d.Identity.OriginHost),
		diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, d.Identity.OriginRealm),
		diameter.UTF8String(diameter.AVPDestinationHost, diameter.AVPFlagMandatory, 0, t.name),
		diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, t.realm),
		diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, imsi),
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, scAddress),
		diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, smRPUI),
	}

	if t.numberCode != 0 && t.numberValue != "" {
		number, err := tbcd.Encode(t.numberValue)
		if err != nil {
			return forwardResult{outcome: temporary}
		}

		avps = append(avps, diameter.OctetString(t.numberCode, 0, tgpp.VendorID, number))
	}

	if more {
		avps = append(avps, diameter.Unsigned32(sgd.AVPTFRFlags, diameter.AVPFlagMandatory, tgpp.VendorID, sgd.TFRFlagMoreMessagesToSend))
	}

	avps = append(avps,
		diameter.Unsigned32(sgd.AVPSMDeliveryTimer, diameter.AVPFlagMandatory, tgpp.VendorID, uint32(d.AttemptTimeout/time.Second)),
		diameter.Time(sgd.AVPSMDeliveryStartTime, diameter.AVPFlagMandatory, tgpp.VendorID, start),
	)

	attempt, cancel := context.WithTimeout(ctx, d.AttemptTimeout)
	defer cancel()

	ans, err := d.Sender.Do(attempt, t.name, &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   sgd.CommandMTForwardShortMessage,
		ApplicationID: sgd.ApplicationID,
		AVPs:          avps,
	})
	if err != nil {
		return forwardResult{outcome: temporary}
	}

	return classifyForwardAnswer(ans)
}

func classifyForwardAnswer(ans *diameter.Message) forwardResult {
	if rc, ok := ans.Find(diameter.AVPResultCode, 0); ok {
		code, err := rc.Unsigned32()

		switch {
		case err == nil && code == diameter.ResultSuccess:
			return forwardResult{outcome: delivered, code: code, cause: ptr(s6c.DeliveryCauseSuccessfulTransfer)}
		case code >= 5000 && code < 6000:
			return forwardResult{outcome: targetFailed, code: code}
		default:
			return forwardResult{outcome: temporary, code: code}
		}
	}

	vendor, code, ok := experimentalResult(ans)
	if !ok || vendor != tgpp.VendorID {
		return forwardResult{outcome: temporary, code: code}
	}

	switch code {
	case tgpp.ResultErrorUserUnknown:
		return forwardResult{outcome: targetFailed, code: code, cause: ptr(s6c.DeliveryCauseAbsentUser)}
	case tgpp.ResultErrorAbsentUser:
		return forwardResult{outcome: temporary, code: code, cause: ptr(s6c.DeliveryCauseAbsentUser), diagnostic: absentDiagnostic(ans)}
	case tgpp.ResultErrorIllegalUser, tgpp.ResultErrorIllegalEquipment:
		return forwardResult{outcome: permanent, code: code}
	case tgpp.ResultErrorSMDeliveryFailure:
		cause, ok := deliveryFailureCause(ans)

		switch {
		case ok && (cause == sgd.CauseEquipmentProtocolError || cause == sgd.CauseEquipmentNotSMEquipped):
			return forwardResult{outcome: permanent, code: code}
		case ok && cause == sgd.CauseMemoryCapacityExceeded:
			return forwardResult{outcome: temporary, code: code, cause: ptr(s6c.DeliveryCauseMemoryCapacityExceeded)}
		default:
			return forwardResult{outcome: temporary, code: code}
		}
	default:
		return forwardResult{outcome: temporary, code: code}
	}
}

func ptr(v uint32) *uint32 {
	return &v
}

func absentDiagnostic(ans *diameter.Message) *uint32 {
	a, ok := ans.Find(sgd.AVPAbsentUserDiagnosticSM, tgpp.VendorID)
	if !ok {
		return nil
	}

	v, err := a.Unsigned32()
	if err != nil {
		return nil
	}

	return &v
}

func experimentalResult(ans *diameter.Message) (uint32, uint32, bool) {
	er, ok := ans.Find(diameter.AVPExperimentalResult, 0)
	if !ok {
		return 0, 0, false
	}

	inner, err := er.Grouped()
	if err != nil {
		return 0, 0, false
	}

	vendorAVP, okVendor := diameter.Find(inner, diameter.AVPVendorID, 0)
	codeAVP, okCode := diameter.Find(inner, diameter.AVPExperimentalResultCode, 0)

	if !okVendor || !okCode {
		return 0, 0, false
	}

	vendor, errVendor := vendorAVP.Unsigned32()
	code, errCode := codeAVP.Unsigned32()

	return vendor, code, errVendor == nil && errCode == nil
}

func deliveryFailureCause(ans *diameter.Message) (uint32, bool) {
	cause, ok := ans.Find(sgd.AVPSMDeliveryFailureCause, tgpp.VendorID)
	if !ok {
		return 0, false
	}

	inner, err := cause.Grouped()
	if err != nil {
		return 0, false
	}

	enum, ok := diameter.Find(inner, sgd.AVPSMEnumeratedDeliveryFailureCause, tgpp.VendorID)
	if !ok {
		return 0, false
	}

	v, err := enum.Unsigned32()

	return v, err == nil
}
