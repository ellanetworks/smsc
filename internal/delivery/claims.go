package delivery

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
)

var errSlowClaim = errors.New("the recipient's claim committed too late to send under it")

type lease struct {
	term    uint64
	claim   db.Claim
	at      time.Time
	started time.Time
}

func (l *lease) fence() db.Fence {
	if l == nil || l.term == 0 {
		return db.Fence{}
	}

	return db.Fence{Term: l.term, Claim: l.claim}
}

func (l *lease) enabled() bool {
	return l != nil && l.term != 0
}

// TS 23.040 clause 6.2
func (d *Deliverer) ensureClaim(ctx context.Context, m db.Message, l *lease) error {
	if !l.enabled() || l.claim.Term != 0 && d.Now().Sub(l.at) <= ClaimMargin {
		return nil
	}

	at := d.Now()

	c, err := d.Store.ClaimRecipient(ctx, m.MSISDN, l.term)
	if err != nil {
		return err
	}

	return d.claimed(ctx, m, l, c, at)
}

func (d *Deliverer) recordAndClaim(stop context.Context, log *slog.Logger, m db.Message, a db.DeliveryAttempt, l *lease,
	alertMSISDN *string,
) error {
	ctx := context.WithoutCancel(stop)

	if !l.enabled() {
		d.record(stop, log, m.ID, a)

		if alertMSISDN != nil {
			d.setAlertMSISDN(ctx, m.MSISDN, *alertMSISDN)
		}

		return nil
	}

	a.MessageID, a.CompletedAt = m.ID, d.Now()
	at := d.Now()

	c, err := d.Store.RecordAttemptAndClaim(ctx, a, m.MSISDN, l.term, alertMSISDN)
	if err != nil {
		return err
	}

	log.DebugContext(ctx, "short message delivery attempt", slog.String("step", string(a.Step)), slog.String("node", a.Node),
		slog.String("outcome", a.Outcome))

	return d.claimed(ctx, m, l, c, at)
}

func (d *Deliverer) claimed(ctx context.Context, m db.Message, l *lease, c db.Claim, at time.Time) error {
	l.claim, l.at = c, at

	if d.Now().Sub(at) <= ClaimMargin {
		return nil
	}

	if err := d.Store.ReleaseRecipient(ctx, m.MSISDN, c); err != nil {
		d.Logger.WarnContext(ctx, "failed to release a recipient", slog.String("msisdn", m.MSISDN), slog.Any("error", err))
	}

	l.claim = db.Claim{}

	return errSlowClaim
}

func (d *Deliverer) unclaimed(ctx context.Context, log *slog.Logger, err error) result {
	if errors.Is(err, db.ErrStaleTerm) {
		log.DebugContext(ctx, "a newer leader term owns the recipient", slog.Any("error", err))
		return result{outcome: interrupted}
	}

	log.WarnContext(ctx, "failed to claim the recipient before delivering; trying again later", slog.Any("error", err))

	return result{outcome: interrupted}
}

func (d *Deliverer) releaseLease(ctx context.Context, msisdn string, l *lease) {
	if !l.enabled() || l.claim.Term == 0 {
		return
	}

	if err := d.Store.ReleaseRecipient(ctx, msisdn, l.claim); err != nil {
		d.Logger.WarnContext(ctx, "failed to release a recipient", slog.String("msisdn", msisdn), slog.Any("error", err))
	}

	l.claim = db.Claim{}
}
