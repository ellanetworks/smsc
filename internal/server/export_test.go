package server

import (
	"time"

	"go.opentelemetry.io/otel/trace"
)

const PurgeBatch = purgeBatch

func (s *Server) SetAttemptTimeout(d time.Duration) { s.attemptTimeout = d }

func (s *Server) SetBcryptCost(cost int) { s.bcryptCost = cost }

func (s *Server) SetTracerProvider(tp trace.TracerProvider) { s.tracerProvider = tp }

func (s *Server) DiameterHost() string { return s.nodes.Identity().OriginHost }

func (s *Server) DiameterLabel() string { return s.nodes.label }

func (s *Server) SetPurgeClock(now func() time.Time, interval time.Duration) {
	s.purgeNow, s.purgeInterval = now, interval
}
