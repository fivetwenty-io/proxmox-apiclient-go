package tasks_test

import (
	"strings"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/tasks"
)

// ---- ParseUPID tests ----

// TestParseUPID_Valid: standard PVE UPID parses correctly.
func TestParseUPID_Valid(t *testing.T) {
	t.Parallel()

	const raw = "UPID:pve1:00001234:00000001:AABBCCDD:qmstart:100:root@pam:"

	upid, err := tasks.ParseUPID(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if upid.Node != "pve1" {
		t.Errorf("Node: want pve1, got %q", upid.Node)
	}

	if upid.PID != 0x00001234 {
		t.Errorf("PID: want 0x1234, got 0x%x", upid.PID)
	}

	if upid.PStart != 0x00000001 {
		t.Errorf("PStart: want 1, got %d", upid.PStart)
	}

	if upid.StartTime != 0xAABBCCDD {
		t.Errorf("StartTime: want 0xAABBCCDD, got 0x%x", upid.StartTime)
	}

	if upid.Type != "qmstart" {
		t.Errorf("Type: want qmstart, got %q", upid.Type)
	}

	if upid.ID != "100" {
		t.Errorf("ID: want 100, got %q", upid.ID)
	}

	if upid.User != "root@pam" {
		t.Errorf("User: want root@pam, got %q", upid.User)
	}

	if upid.Raw != raw {
		t.Errorf("Raw: want %q, got %q", raw, upid.Raw)
	}
}

// TestParseUPID_ValidNoVMID: task with empty ID field (no VMID, e.g. node-level task).
func TestParseUPID_ValidNoVMID(t *testing.T) {
	t.Parallel()

	const raw = "UPID:node2:0000ABCD:000000FF:66778899:aptupdate::root@pam:"

	upid, err := tasks.ParseUPID(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if upid.ID != "" {
		t.Errorf("ID: want empty, got %q", upid.ID)
	}

	if upid.Type != "aptupdate" {
		t.Errorf("Type: want aptupdate, got %q", upid.Type)
	}
}

// TestParseUPID_InvalidPrefix: non-UPID prefix returns error.
func TestParseUPID_InvalidPrefix(t *testing.T) {
	t.Parallel()

	_, err := tasks.ParseUPID("NOPE:pve1:00001234:00000001:AABBCCDD:qmstart:100:root@pam:")
	if err == nil {
		t.Fatal("expected error for invalid prefix")
	}

	if !strings.Contains(err.Error(), "UPID") {
		t.Errorf("error should mention expected prefix: %v", err)
	}
}

// TestParseUPID_TooFewFields: missing fields returns error.
func TestParseUPID_TooFewFields(t *testing.T) {
	t.Parallel()

	_, err := tasks.ParseUPID("UPID:pve1:00001234:")
	if err == nil {
		t.Fatal("expected error for too few fields")
	}
}

// TestParseUPID_EmptyString: empty string returns error.
func TestParseUPID_EmptyString(t *testing.T) {
	t.Parallel()

	_, err := tasks.ParseUPID("")
	if err == nil {
		t.Fatal("expected error for empty string")
	}
}

// TestParseUPID_InvalidPIDHex: non-hex PID returns error.
func TestParseUPID_InvalidPIDHex(t *testing.T) {
	t.Parallel()

	_, err := tasks.ParseUPID("UPID:pve1:ZZZZZZZZ:00000001:AABBCCDD:qmstart:100:root@pam:")
	if err == nil {
		t.Fatal("expected error for invalid PID hex")
	}

	if !strings.Contains(err.Error(), "PID") {
		t.Errorf("error should mention PID: %v", err)
	}
}

// TestParseUPID_InvalidPStartHex: non-hex PStart returns error.
func TestParseUPID_InvalidPStartHex(t *testing.T) {
	t.Parallel()

	_, err := tasks.ParseUPID("UPID:pve1:00001234:ZZZZZZZZ:AABBCCDD:qmstart:100:root@pam:")
	if err == nil {
		t.Fatal("expected error for invalid PStart hex")
	}

	if !strings.Contains(err.Error(), "PStart") {
		t.Errorf("error should mention PStart: %v", err)
	}
}

// TestParseUPID_InvalidStartTimeHex: non-hex StartTime returns error.
func TestParseUPID_InvalidStartTimeHex(t *testing.T) {
	t.Parallel()

	_, err := tasks.ParseUPID("UPID:pve1:00001234:00000001:ZZZZZZZZ:qmstart:100:root@pam:")
	if err == nil {
		t.Fatal("expected error for invalid StartTime hex")
	}

	if !strings.Contains(err.Error(), "StartTime") {
		t.Errorf("error should mention StartTime: %v", err)
	}
}

// ---- WaitForUPID tests ----

// TestWaitForUPID_Success: parses node from UPID and waits successfully.
func TestWaitForUPID_Success(t *testing.T) {
	t.Parallel()

	const upid = "UPID:pve10:00001234:00000001:AABBCCDD:qmstart:100:root@pam:"

	apiPath := "/api2/json/nodes/pve10/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 1, "OK")

	srv := newTestHTTPServer(t, handler)

	svc := tasks.New(newTestClient(t, srv))

	status, err := svc.WaitForUPID(t.Context(), upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status == nil || status.ExitStatus != "OK" {
		t.Errorf("unexpected status: %#v", status)
	}
}

// TestWaitForUPID_InvalidUPID: invalid UPID returns parse error, no HTTP call.
func TestWaitForUPID_InvalidUPID(t *testing.T) {
	t.Parallel()

	srv := newTestHTTPServer(t, nil)

	svc := tasks.New(newTestClient(t, srv))

	_, err := svc.WaitForUPID(t.Context(), "not-a-valid-upid", &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err == nil {
		t.Fatal("expected error for invalid UPID")
	}
}

// ---- Warning exitstatus tests (F6) ----

// TestWaitTask_WarningsExitStatus: "WARNINGS: N" is non-failure, sets Warned=true.
func TestWaitTask_WarningsExitStatus(t *testing.T) {
	t.Parallel()

	const node, upid = "pvew1", "UPID:pvew1:00001234:00000001:AABBCCDD:qmstart:100:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "WARNINGS: 3")

	srv := newTestHTTPServer(t, handler)

	svc := tasks.New(newTestClient(t, srv))

	status, err := svc.Wait(t.Context(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("WARNINGS: N should be non-failure, got err: %v", err)
	}

	if status == nil {
		t.Fatal("status is nil")
	}

	if !status.Warned {
		t.Errorf("Warned: want true, got false")
	}

	if status.ExitStatus != "WARNINGS: 3" {
		t.Errorf("ExitStatus: want %q, got %q", "WARNINGS: 3", status.ExitStatus)
	}
}

// TestWaitTask_WarningsSingleCount: "WARNINGS: 1" is also non-failure.
func TestWaitTask_WarningsSingleCount(t *testing.T) {
	t.Parallel()

	const node, upid = "pvew2", "UPID:pvew2:00001235:00000001:AABBCCDE:vzdump:200:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "WARNINGS: 1")

	srv := newTestHTTPServer(t, handler)

	svc := tasks.New(newTestClient(t, srv))

	status, err := svc.Wait(t.Context(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("WARNINGS: 1 should be non-failure, got err: %v", err)
	}

	if status == nil || !status.Warned {
		t.Errorf("expected Warned=true, got status: %#v", status)
	}
}

// TestWaitTask_OKNotWarned: normal "OK" does not set Warned flag.
func TestWaitTask_OKNotWarned(t *testing.T) {
	t.Parallel()

	const node, upid = "pvew3", "UPID:pvew3:00001236:00000001:AABBCCDF:qmstart:300:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "OK")

	srv := newTestHTTPServer(t, handler)

	svc := tasks.New(newTestClient(t, srv))

	status, err := svc.Wait(t.Context(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("OK should be success: %v", err)
	}

	if status == nil || status.Warned {
		t.Errorf("expected Warned=false for OK exit, got status: %#v", status)
	}
}
