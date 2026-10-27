package server

import "time"

func (s *Server) SetAttemptTimeout(d time.Duration) { s.attemptTimeout = d }

func (s *Server) SetBcryptCost(cost int) { s.bcryptCost = cost }

func (s *Server) DiameterHost() string { return s.nodes.Identity().OriginHost }
