package main

import (
	"testing"
	"time"
)

func TestTunnelRecoveryContinuesWhenEdgeReturns(t *testing.T) {
	var recovery tunnelRecoveryState
	for attempt := 1; attempt <= 3; attempt++ {
		if got := recovery.observeProbe(false); got != (attempt == 3) {
			t.Fatalf("probe %d: rebuild=%t", attempt, got)
		}
	}
	if delay := recovery.reconnectFailed(); delay != 2*time.Minute {
		t.Fatalf("first retry delay=%v", delay)
	}
	for attempt := 0; attempt < 4; attempt++ {
		if !recovery.observeProbe(true) {
			t.Fatal("TCP recovery abandoned a failed tunnel rebuild")
		}
		recovery.reconnectFailed()
	}
	recovery.reconnected()
	if recovery.observeProbe(true) {
		t.Fatal("successful rebuild did not clear recovery state")
	}
}

func TestTunnelRecoveryThresholdResetsOnlyBeforeRebuild(t *testing.T) {
	var recovery tunnelRecoveryState
	for _, reachable := range []bool{false, false, true, false, false} {
		if recovery.observeProbe(reachable) {
			t.Fatal("short edge outage triggered a rebuild")
		}
	}
	if !recovery.observeProbe(false) {
		t.Fatal("three consecutive failures did not trigger a rebuild")
	}
}

func TestTunnelRecoveryBackoffStaysBoundedAndResets(t *testing.T) {
	var recovery tunnelRecoveryState
	for index, want := range []time.Duration{2, 4, 8, 15} {
		if got := recovery.reconnectFailed(); got != want*time.Minute {
			t.Fatalf("retry %d: got %v, want %v", index, got, want*time.Minute)
		}
	}
	for attempt := 0; attempt < 1000; attempt++ {
		if got := recovery.reconnectFailed(); got != 15*time.Minute {
			t.Fatalf("long-running retry overflowed: %v", got)
		}
	}
	recovery.reconnected()
	if got := recovery.reconnectFailed(); got != 2*time.Minute {
		t.Fatalf("successful rebuild did not reset backoff: %v", got)
	}
}
