package main

import "time"

const tunnelFailureThreshold = 3

// A failed rebuild has closed the old service. Edge reachability cannot clear
// this state; only a successful rebuild can restore the service.
type tunnelRecoveryState struct {
	failures     int
	retryPending bool
	retryDelay   time.Duration
}

func (s *tunnelRecoveryState) observeProbe(edgeReachable bool) bool {
	if edgeReachable {
		s.failures = 0
	} else if s.failures < tunnelFailureThreshold {
		s.failures++
	}
	return s.retryPending || s.failures >= tunnelFailureThreshold
}

func (s *tunnelRecoveryState) reconnectFailed() time.Duration {
	const (
		baseDelay = 2 * time.Minute
		maxDelay  = 15 * time.Minute
	)
	s.retryPending = true
	delay := s.retryDelay
	if delay == 0 {
		delay = baseDelay
	}
	if delay >= maxDelay/2 {
		s.retryDelay = maxDelay
	} else {
		s.retryDelay = delay * 2
	}
	return delay
}

func (s *tunnelRecoveryState) reconnected() {
	*s = tunnelRecoveryState{}
}
