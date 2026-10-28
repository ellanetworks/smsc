package server

import "time"

// PurgeBatch is the most messages that one transaction of the retention deletes.
const PurgeBatch = purgeBatch

func (s *Server) SetAttemptTimeout(d time.Duration) { s.attemptTimeout = d }

func (s *Server) SetBcryptCost(cost int) { s.bcryptCost = cost }

func (s *Server) DiameterHost() string { return s.nodes.Identity().OriginHost }

// SetPurgeClock makes the message retention run every interval, on the clock now.
func (s *Server) SetPurgeClock(now func() time.Time, interval time.Duration) {
	s.purgeNow, s.purgeInterval = now, interval
}
