package control

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sagernet/sing-cloudflared/internal/protocol"
)

func TestRegistrationErrorRecovery(t *testing.T) {
	tests := []struct {
		name        string
		cause       string
		shouldRetry bool
		retryAfter  time.Duration
		wantRetry   bool
	}{
		{"duplicate marked permanent", "EDUPCONN", false, 0, true},
		{"duplicate with server delay", "EDUPCONN", false, 3 * time.Second, true},
		{"duplicate marked retryable", "EDUPCONN", true, time.Second, true},
		{"retryable server response", "temporary edge failure", true, 250 * time.Millisecond, true},
		{"authentication rejected", "authentication rejected", false, time.Second, false},
		{"configuration rejected", "invalid tunnel configuration", false, 0, false},
		{"only exact duplicate code", "EDUPCONN unexpected suffix", false, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := registrationErrorFromResponse(test.cause, test.shouldRetry, test.retryAfter)
			if err.Error() != test.cause {
				t.Fatalf("registration cause changed: %q", err)
			}
			// QUIC and HTTP/2 add context before the supervisor classifies this.
			wrapped := fmt.Errorf("register connection: %w", err)
			var retryable *protocol.RetryableError
			if got := errors.As(wrapped, &retryable); got != test.wantRetry {
				t.Fatalf("retryable=%t, want %t", got, test.wantRetry)
			}
			if retryable != nil && retryable.Delay != test.retryAfter {
				t.Fatalf("server retry delay changed: %v", retryable.Delay)
			}
			if permanent := IsPermanentRegistrationError(wrapped); permanent == test.wantRetry {
				t.Fatalf("permanent=%t, want %t", permanent, !test.wantRetry)
			}
		})
	}
}
