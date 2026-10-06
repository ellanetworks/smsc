package server

import "time"

func (s *Server) SetAttemptTimeout(d time.Duration) { s.attemptTimeout = d }

func (s *Server) DiameterHost() string { return s.nodes.Identity().OriginHost }
