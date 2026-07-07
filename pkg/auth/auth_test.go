package auth_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

func TestParseTicketTimestamp(t *testing.T) {
	t.Parallel()

	tests := buildParseTicketTimestampTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := auth.ParseTicketTimestamp(testCase.ticket)

			if testCase.wantErr {
				assertParseTimestampError(t, err, testCase.errType)

				return
			}

			if err != nil {
				t.Errorf("ParseTicketTimestamp() unexpected error = %v", err)

				return
			}

			if testCase.validateAge {
				validateTimestampAge(t, result)
			}
		})
	}
}

// buildParseTicketTimestampTestCases creates test cases for timestamp parsing validation.
//
//nolint:funlen // Test case data structure
func buildParseTicketTimestampTestCases() []struct {
	name        string
	ticket      string
	wantErr     bool
	errType     error
	validateAge bool
} {
	hexTimestamp := "659F8E78" // Hex representation of 1705314424 (2024-01-15 10:27:04 UTC)

	return []struct {
		name        string
		ticket      string
		wantErr     bool
		errType     error
		validateAge bool
	}{
		{
			name:    "valid ticket format",
			ticket:  "PVE:root@pam:" + hexTimestamp + "::signature-data-here",
			wantErr: false,
		},
		{
			name:    "valid ticket with different data",
			ticket:  "somedata:" + hexTimestamp + "::anothersignature",
			wantErr: false,
		},
		{
			name:    "empty ticket",
			ticket:  "",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
		{
			name:    "invalid format - no colons",
			ticket:  "invalidticketformat",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
		{
			name:    "invalid format - only one colon",
			ticket:  "data:timestamp",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
		{
			name:    "invalid format - single colon instead of double",
			ticket:  "data:" + hexTimestamp + ":signature",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
		{
			name:    "invalid hex - non-hex characters",
			ticket:  "data:ZZZZZZZZ::signature",
			wantErr: true,
		},
		{
			name:    "invalid hex - wrong length (too short)",
			ticket:  "data:659F8E::signature",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
		{
			name:    "invalid hex - wrong length (too long)",
			ticket:  "data:659F8E78FF::signature",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
		{
			name:    "lowercase hex should not match",
			ticket:  "data:659f8e78::signature",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
		{
			name:    "valid hex with spaces should fail",
			ticket:  "data: 659F8E78 ::signature",
			wantErr: true,
			errType: auth.ErrInvalidTicketFormat,
		},
	}
}

// assertParseTimestampError validates expected error conditions for timestamp parsing.
func assertParseTimestampError(t *testing.T, err, expectedType error) {
	t.Helper()

	if err == nil {
		t.Errorf("ParseTicketTimestamp() expected error, got nil")

		return
	}

	if expectedType != nil && !errors.Is(err, expectedType) {
		t.Errorf("ParseTicketTimestamp() error = %v, want %v", err, expectedType)
	}
}

// validateTimestampAge checks that timestamp is reasonable (not too old or in future).
func validateTimestampAge(t *testing.T, timestamp time.Time) {
	t.Helper()

	now := time.Now()
	tenYearsAgo := now.Add(-10 * 365 * 24 * time.Hour)

	if timestamp.Before(tenYearsAgo) {
		t.Errorf("ParseTicketTimestamp() timestamp too old: %v", timestamp)
	}

	if timestamp.After(now.Add(time.Hour)) {
		t.Errorf("ParseTicketTimestamp() timestamp in future: %v", timestamp)
	}
}

func TestParseTicketTimestamp_ActualValues(t *testing.T) {
	t.Parallel()

	// Test with actual known timestamp values
	tests := []struct {
		name     string
		ticket   string
		wantTime time.Time
	}{
		{
			name:     "known timestamp 2024-01-15",
			ticket:   "data:659F8E78::sig",
			wantTime: time.Unix(0x659F8E78, 0), // 2024-01-15 10:27:04 UTC
		},
		{
			name:     "epoch timestamp",
			ticket:   "data:00000000::sig",
			wantTime: time.Unix(0, 0),
		},
		{
			name:     "max 32-bit timestamp",
			ticket:   "data:FFFFFFFF::sig",
			wantTime: time.Unix(0xFFFFFFFF, 0), // 2106-02-07 06:28:15 UTC
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := auth.ParseTicketTimestamp(testCase.ticket)
			if err != nil {
				t.Fatalf("ParseTicketTimestamp() unexpected error = %v", err)
			}

			if !result.Equal(testCase.wantTime) {
				t.Errorf("ParseTicketTimestamp() = %v, want %v", result, testCase.wantTime)
			}
		})
	}
}

func TestTicket_ShouldRenew(t *testing.T) {
	t.Parallel()

	tests := buildShouldRenewTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := testCase.ticket.ShouldRenew(testCase.threshold)

			if result != testCase.wantRenew {
				t.Errorf("ShouldRenew() = %v, want %v. %s", result, testCase.wantRenew, testCase.description)
			}
		})
	}
}

// buildShouldRenewTestCases creates test cases for ticket renewal validation.
//
//nolint:funlen // Test case data structure
func buildShouldRenewTestCases() []struct {
	name        string
	ticket      *auth.Ticket
	threshold   time.Duration
	wantRenew   bool
	description string
} {
	now := time.Now()
	oneHourAgo := now.Add(-1 * time.Hour)
	twoHoursAgo := now.Add(-2 * time.Hour)
	thirtyMinutesAgo := now.Add(-30 * time.Minute)

	// Create valid ticket with timestamp one hour ago (hex: current time - 3600)
	oneHourAgoHex := formatTimestampHex(oneHourAgo.Unix())
	thirtyMinAgoHex := formatTimestampHex(thirtyMinutesAgo.Unix())
	twoHoursAgoHex := formatTimestampHex(twoHoursAgo.Unix())

	return []struct {
		name        string
		ticket      *auth.Ticket
		threshold   time.Duration
		wantRenew   bool
		description string
	}{
		{
			name: "ticket > 1 hour old, should renew",
			ticket: &auth.Ticket{
				Value:      "data:" + oneHourAgoHex + "::signature",
				ValidUntil: now.Add(59 * time.Minute), // Still valid but old
			},
			threshold:   time.Hour,
			wantRenew:   true,
			description: "Ticket is > 1 hour old, approaching expiry",
		},
		{
			name: "ticket < 1 hour old, should not renew",
			ticket: &auth.Ticket{
				Value:      "data:" + thirtyMinAgoHex + "::signature",
				ValidUntil: now.Add(90 * time.Minute),
			},
			threshold:   time.Hour,
			wantRenew:   false,
			description: "Ticket is only 30 minutes old",
		},
		{
			name: "ticket > 2 hours old (expired), should renew",
			ticket: &auth.Ticket{
				Value:      "data:" + twoHoursAgoHex + "::signature",
				ValidUntil: now.Add(-1 * time.Minute), // Already expired
			},
			threshold:   time.Hour,
			wantRenew:   true,
			description: "Ticket is expired",
		},
		{
			name: "empty ticket value, should not renew",
			ticket: &auth.Ticket{
				Value:      "",
				ValidUntil: now.Add(time.Hour),
			},
			threshold:   time.Hour,
			wantRenew:   false,
			description: "No ticket value present",
		},
		{
			name: "unparseable ticket, fallback to ValidUntil",
			ticket: &auth.Ticket{
				Value:      "invalid-ticket-format",
				ValidUntil: now.Add(30 * time.Minute), // < 1 hour until expiry
			},
			threshold:   time.Hour,
			wantRenew:   true,
			description: "Falls back to ValidUntil check, 30min < 1hour threshold",
		},
		{
			name: "unparseable ticket, not near expiry",
			ticket: &auth.Ticket{
				Value:      "invalid-ticket-format",
				ValidUntil: now.Add(90 * time.Minute), // > 1 hour until expiry
			},
			threshold:   time.Hour,
			wantRenew:   false,
			description: "Falls back to ValidUntil check, 90min > 1hour threshold",
		},
		{
			name: "custom threshold 30min, ticket 100min old",
			ticket: &auth.Ticket{
				Value:      "data:" + formatTimestampHex(now.Add(-100*time.Minute).Unix()) + "::sig",
				ValidUntil: now.Add(20 * time.Minute),
			},
			threshold:   30 * time.Minute,
			wantRenew:   true,
			description: "With 30min threshold, 100min old ticket (20min left) should renew",
		},
		{
			name: "custom threshold 30min, ticket 20min old",
			ticket: &auth.Ticket{
				Value:      "data:" + formatTimestampHex(now.Add(-20*time.Minute).Unix()) + "::sig",
				ValidUntil: now.Add(100 * time.Minute),
			},
			threshold:   30 * time.Minute,
			wantRenew:   false,
			description: "With 30min threshold, 20min old ticket should not renew",
		},
	}
}

// formatTimestampHex formats a Unix timestamp as an 8-character uppercase hex string.
func formatTimestampHex(timestamp int64) string {
	return fmt.Sprintf("%08X", timestamp)
}
