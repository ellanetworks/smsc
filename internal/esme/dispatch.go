package esme

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	maxWait         = time.Minute
	dispatchBackoff = time.Second
	window          = smpp.DefaultWindow
)

func (s *Service) NotificationsQueued() {
	s.init()
	s.wakeDispatcher()
}

func (s *Service) wakeDispatcher() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	s.init()

	inflight := map[int64]bool{}
	done := make(chan int64)

	for ctx.Err() == nil {
		wait := s.dispatch(ctx, inflight, done)

		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-timer.C:
		case id := <-done:
			delete(inflight, id)
		}

		timer.Stop()
	}

	for len(inflight) > 0 {
		delete(inflight, <-done)
	}
}

func (s *Service) dispatch(ctx context.Context, inflight map[int64]bool, done chan<- int64) time.Duration {
	now := s.Now()

	expired, err := s.Store.ExpireNotifications(ctx, now)
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to expire SMPP notifications", slog.Any("error", err))
		return dispatchBackoff
	}

	for t, n := range expired {
		s.Metrics.notified(t, db.NotificationExpired, n)
	}

	exclude := make([]int64, 0, len(inflight))
	for id := range inflight {
		exclude = append(exclude, id)
	}

	wait := maxWait

	for esmeID, sessions := range s.receivers() {
		free := 0
		for _, sess := range sessions {
			free += window - sess.inflight
		}

		if free > 0 {
			due, err := s.Store.DueNotifications(ctx, esmeID, now, exclude, free)
			if err != nil {
				s.Logger.ErrorContext(ctx, "failed to read the due SMPP notifications", slog.Any("error", err))
				return dispatchBackoff
			}

			for _, n := range due {
				sess := s.reserve(sessions)
				if sess == nil {
					break
				}

				inflight[n.ID] = true

				go func() {
					s.send(context.WithoutCancel(ctx), sess, n)
					s.release(sess)

					done <- n.ID
				}()
			}
		}

		next, ok, err := s.Store.NextNotificationAt(ctx, esmeID, now)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to read the next SMPP notification", slog.Any("error", err))
			return dispatchBackoff
		}

		if ok && next.After(now) {
			wait = min(wait, next.Sub(now))
		}
	}

	return wait
}

type receiverSlot struct {
	sess     *Session
	inflight int
}

func (s *Service) receivers() map[int64][]receiverSlot {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[int64][]receiverSlot{}

	for _, sess := range s.sessions {
		if sess.conn != nil && !sess.closed && sess.BindType.receives() {
			out[sess.ESME.ID] = append(out[sess.ESME.ID], receiverSlot{sess: sess, inflight: sess.inflight})
		}
	}

	return out
}

func (s *Service) reserve(slots []receiverSlot) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	var best *Session

	for _, slot := range slots {
		sess := slot.sess
		if sess.closed || sess.inflight >= window {
			continue
		}

		if best == nil || sess.inflight < best.inflight {
			best = sess
		}
	}

	if best != nil {
		best.inflight++
	}

	return best
}

func (s *Service) release(sess *Session) {
	s.mu.Lock()
	sess.inflight--
	s.mu.Unlock()
}

// SMPP 3.4 section 4.6.2
// SMPP 3.4 section 7.1
func (s *Service) send(ctx context.Context, sess *Session, n db.Notification) {
	log := s.Logger.With(slog.String("system_id", sess.ESME.SystemID), slog.Int64("message_id", n.Message.ID),
		slog.String("type", string(n.Type)))

	d, err := deliverSM(n)
	if errors.Is(err, errNoUserData) {
		s.close(ctx, log, n, db.NotificationNoUserData)
		return
	}

	if err != nil {
		log.ErrorContext(ctx, "failed to make an SMPP notification", slog.Any("error", err))
		s.retry(ctx, log, n)

		return
	}

	s.mu.Lock()
	conn := sess.conn
	s.mu.Unlock()

	ctx, span := s.startSpan(ctx, trace.SpanKindClient, smpp.CmdDeliverSM, sess, tracing.Links(n.Message.TraceParent)...)
	status, err := conn.Deliver(ctx, d)

	endDeliverSpan(span, status, err)

	var stateErr *smpp.StateError

	switch {
	case errors.Is(err, smpp.ErrClosed) || errors.As(err, &stateErr):
		log.DebugContext(ctx, "deliver_sm on a session that ended", slog.Any("error", err))

		s.mu.Lock()
		sess.closed = true
		s.mu.Unlock()
	case err != nil:
		log.DebugContext(ctx, "deliver_sm failed", slog.Any("error", err))
		s.retry(ctx, log, n)
	case status == smpp.StatusOK:
		s.close(ctx, log, n, db.NotificationAcknowledged)
	case status == smpp.StatusXPAppn || status == smpp.StatusXRAppn:
		log.InfoContext(ctx, "deliver_sm rejected", slog.String("status", status.String()))
		s.close(ctx, log, n, db.NotificationRejected)
	default:
		log.DebugContext(ctx, "deliver_sm refused for now", slog.String("status", status.String()))
		s.retry(ctx, log, n)
	}
}

func (s *Service) close(ctx context.Context, log *slog.Logger, n db.Notification, result db.NotificationResult) {
	err := s.Store.CloseNotification(ctx, n.ID, result)
	if errors.Is(err, db.ErrNotFound) {
		return
	}

	if err != nil {
		log.ErrorContext(ctx, "failed to store the outcome of an SMPP notification", slog.Any("error", err))
		return
	}

	s.Metrics.notified(n.Type, result, 1)
}

func (s *Service) retry(ctx context.Context, log *slog.Logger, n db.Notification) {
	if err := s.Store.RetryNotification(ctx, n.ID, s.Now().Add(retryDelay(n.Attempts))); err != nil {
		log.ErrorContext(ctx, "failed to schedule an SMPP notification again", slog.Any("error", err))
	}
}

func endDeliverSpan(span trace.Span, status smpp.CommandStatus, err error) {
	var stateErr *smpp.StateError

	switch {
	case errors.Is(err, smpp.ErrClosed) || errors.As(err, &stateErr):
		endSpan(span, nil, "session_closed", err)
	case errors.Is(err, smpp.ErrResponseTimeout) || errors.Is(err, context.DeadlineExceeded):
		endSpan(span, nil, "timeout", err)
	case errors.Is(err, context.Canceled):
		endSpan(span, nil, "canceled", err)
	case err != nil:
		endSpan(span, nil, semconv.ErrorTypeOther.Value.AsString(), err)
	case status != smpp.StatusOK:
		endSpan(span, &status, statusErrorType(status), nil)
	default:
		endSpan(span, &status, "", nil)
	}
}

func statusErrorType(status smpp.CommandStatus) string {
	if name := status.String(); strings.HasPrefix(name, "ESME_") {
		return name
	}

	return semconv.ErrorTypeOther.Value.AsString()
}
