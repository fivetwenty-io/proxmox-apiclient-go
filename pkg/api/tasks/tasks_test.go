package tasks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/tasks"
	pveclient "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

func optsFromServerURL(u string) pveclient.Options {
	parsed, _ := url.Parse(u)
	host := strings.Split(parsed.Host, ":")[0]
	port := 0

	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		p, _ := strconv.Atoi(parts[1])
		port = p
	}

	return pveclient.Options{Host: host, Port: port, Protocol: "http", APIToken: "user@pam!tok=sec"}
}

// newTestClient builds a pveclient.Client pointed at srv.
//
//nolint:ireturn // Returns interface intentionally for test helper flexibility.
func newTestClient(t *testing.T, srv *httptest.Server) pveclient.Client {
	t.Helper()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return cli
}

// newTestHTTPServer starts an httptest.Server with handler and registers cleanup.
// A nil handler uses http.DefaultServeMux (responds 404 to everything useful).
func newTestHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no handler", http.StatusNotFound)
		})
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return srv
}

const (
	taskStatusKey  = "status"
	taskStoppedVal = "stopped"
	taskRunningVal = "running"
	taskExitKey    = "exitstatus"
)

// taskStatusHandler returns an http.HandlerFunc that yields runningCount "running" responses
// then a "stopped" response with the given exitStatus.
func taskStatusHandler(path string, runningCount int, exitStatus string) http.HandlerFunc {
	calls := 0

	return func(writer http.ResponseWriter, req *http.Request) {
		if req.URL.Path != path {
			http.Error(writer, "bad path", http.StatusNotFound)

			return
		}

		calls++

		var data map[string]any
		if calls <= runningCount {
			data = map[string]any{taskStatusKey: taskRunningVal}
		} else {
			data = map[string]any{taskStatusKey: taskStoppedVal, taskExitKey: exitStatus}
		}

		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": data, "success": 1})
	}
}

// ---- original tests (preserved) ----

func TestWaitTaskSuccess(t *testing.T) {
	t.Parallel()

	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api2/json/nodes/node1/tasks/UPID123/status" {
			http.Error(writer, "bad path", http.StatusNotFound)

			return
		}

		calls++
		// first call running, second stopped OK
		var data map[string]any
		if calls < 2 {
			data = map[string]any{taskStatusKey: taskRunningVal}
		} else {
			data = map[string]any{taskStatusKey: taskStoppedVal, taskExitKey: "OK"}
		}

		_ = json.NewEncoder(writer).Encode(map[string]any{"data": data, "success": 1})
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := tasks.New(cli)

	status, err := svc.Wait(context.Background(), "node1", "UPID123", &tasks.WaitOptions{TimeoutSeconds: 5, IntervalMillis: 10})
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}

	if status == nil || status.Status != taskStoppedVal || status.ExitStatus != "OK" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestWaitTaskTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow timeout test in short mode")
	}

	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		// Always running
		_, _ = writer.Write([]byte(`{"data": {"status": "running"}, "success": 1}`))
	}))
	defer srv.Close()

	parsed, _ := url.Parse(srv.URL)
	host := strings.Split(parsed.Host, ":")[0]

	portNum := 0
	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		portNum, _ = strconv.Atoi(parts[1])
	}

	cli, err := pveclient.NewClient(pveclient.Options{Host: host, Port: portNum, Protocol: "http", APIToken: "u@pam!tok=sec"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := tasks.New(cli)

	_, err = svc.Wait(context.Background(), "n1", "UPID123", &tasks.WaitOptions{TimeoutSeconds: 1, IntervalMillis: 5})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
}

// ---- new tests ----

// TestWaitTask_FullLifecycle: running->running->stopped(OK). Verifies Status fields.
func TestWaitTask_FullLifecycle(t *testing.T) {
	t.Parallel()

	const node, upid = "pve1", "UPID:pve1:00001234:00000001:AABBCCDD:qmstart:100:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 2, "OK")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	taskStatus, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 10,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if taskStatus == nil {
		t.Fatal("status is nil")
	}

	if taskStatus.Status != taskStoppedVal {
		t.Errorf("Status: want stopped, got %q", taskStatus.Status)
	}

	if taskStatus.ExitStatus != "OK" {
		t.Errorf("ExitStatus: want OK, got %q", taskStatus.ExitStatus)
	}

	if taskStatus.UpID != upid {
		t.Errorf("UpID: want %q, got %q", upid, taskStatus.UpID)
	}
}

// TestWaitTask_FailedTask: stopped with non-OK exit returns error.
// poll() returns (nil, err) for non-retryable errors; only the error is checked.
func TestWaitTask_FailedTask(t *testing.T) {
	t.Parallel()

	const node, upid = "pve2", "UPID:pve2:00001235:00000002:AABBCCDE:qmstop:101:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "FAILED")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	_, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err == nil {
		t.Fatal("expected error for failed task")
	}

	if !strings.Contains(err.Error(), "task failed") && !strings.Contains(err.Error(), "FAILED") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestWaitTask_ExitStatusOkLowercase: "ok" (lowercase) is also a success exit.
func TestWaitTask_ExitStatusOkLowercase(t *testing.T) {
	t.Parallel()

	const node, upid = "pve3", "UPID:pve3:00001236:00000003:AABBCCDF:vzdump:102:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "ok")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	taskStatus, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("lowercase 'ok' should be success: %v", err)
	}

	if taskStatus.ExitStatus != "ok" {
		t.Errorf("ExitStatus: want ok, got %q", taskStatus.ExitStatus)
	}
}

// TestWaitTask_ExitStatusEmpty: empty exitstatus is also success.
func TestWaitTask_ExitStatusEmpty(t *testing.T) {
	t.Parallel()

	const node, upid = "pve4", "UPID:pve4:00001237:00000004:AABBCCE0:qmclone:103:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	taskStatus, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("empty exitstatus should be success: %v", err)
	}

	if taskStatus.ExitStatus != "" {
		t.Errorf("ExitStatus: want empty, got %q", taskStatus.ExitStatus)
	}
}

// TestWaitTask_ContextCancellation: cancelled ctx returns quickly with an error.
// Uses WithTimeout rather than a goroutine-based cancel to avoid scheduling
// non-determinism under -race -count=N parallel load.
func TestWaitTask_ContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data": {"status": "running"}, "success": 1}`))
	}))
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	// Deadline-based cancellation is runtime-enforced; no goroutine scheduling dependency.
	// Use a 2s deadline so the context fires reliably even under -race -count=20 load.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()

	_, err := svc.Wait(ctx, "node1", "UPID_CTX", &tasks.WaitOptions{
		TimeoutSeconds: 30,
		IntervalMillis: 5,
	})

	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on ctx cancellation")
	}

	// Must return within 10s even under heavy parallel load (-race -count=20).
	if elapsed > 10*time.Second {
		t.Errorf("cancellation took too long: %v", elapsed)
	}
}

// TestWaitTask_ContextAlreadyCancelled: pre-cancelled ctx returns promptly.
func TestWaitTask_ContextAlreadyCancelled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"data": {"status": "running"}, "success": 1}`))
	}))
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()

	_, err := svc.Wait(ctx, "node1", "UPID_PRECANCEL", &tasks.WaitOptions{
		TimeoutSeconds: 30,
		IntervalMillis: 5,
	})

	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on pre-cancelled ctx")
	}

	if elapsed > 500*time.Millisecond {
		t.Errorf("pre-cancelled ctx took too long: %v", elapsed)
	}
}

// TestWaitTask_NilOptions: nil opts uses defaults without panic.
func TestWaitTask_NilOptions(t *testing.T) {
	t.Parallel()

	const node, upid = "pve5", "UPID:pve5:00001238:00000005:AABBCCE1:qmstart:104:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "OK")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	// nil opts uses DefaultTaskTimeoutSeconds=300; outer ctx caps it to 5s.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	taskStatus, err := svc.Wait(ctx, node, upid, nil)
	if err != nil {
		t.Fatalf("nil opts: unexpected error: %v", err)
	}

	if taskStatus == nil || taskStatus.Status != taskStoppedVal {
		t.Errorf("nil opts: unexpected status: %#v", taskStatus)
	}
}

// TestWaitTask_BackoffIntegration: backoff enabled; task finishes after several polls.
func TestWaitTask_BackoffIntegration(t *testing.T) {
	t.Parallel()

	const node, upid = "pve6", "UPID:pve6:00001239:00000006:AABBCCE2:qmstart:105:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 3, "OK")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	taskStatus, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds:    10,
		IntervalMillis:    5,
		Backoff:           true,
		MaxIntervalMillis: 50,
	})
	if err != nil {
		t.Fatalf("backoff integration: unexpected error: %v", err)
	}

	if taskStatus == nil || taskStatus.ExitStatus != "OK" {
		t.Errorf("backoff integration: unexpected status: %#v", taskStatus)
	}
}

// TestWaitTask_JitterIntegration: jitter enabled; task still completes correctly.
func TestWaitTask_JitterIntegration(t *testing.T) {
	t.Parallel()

	const node, upid = "pve7", "UPID:pve7:0000123A:00000007:AABBCCE3:qmstart:106:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 2, "OK")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	taskStatus, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 10,
		IntervalMillis: 10,
		JitterPct:      20,
	})
	if err != nil {
		t.Fatalf("jitter integration: unexpected error: %v", err)
	}

	if taskStatus == nil || taskStatus.ExitStatus != "OK" {
		t.Errorf("jitter integration: unexpected status: %#v", taskStatus)
	}
}

// TestWaitTask_BadResponseFormat: server returns non-map data triggers error.
func TestWaitTask_BadResponseFormat(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data": "not-a-map", "success": 1}`))
	}))
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := svc.Wait(ctx, "node1", "UPID_BAD", &tasks.WaitOptions{
		TimeoutSeconds: 2,
		IntervalMillis: 5,
	})
	if err == nil {
		t.Fatal("expected error for bad response format")
	}
}

// TestWaitTask_ServerError: server returns 500; error propagates.
func TestWaitTask_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := svc.Wait(ctx, "node1", "UPID_ERR", &tasks.WaitOptions{
		TimeoutSeconds: 2,
		IntervalMillis: 5,
	})
	if err == nil {
		t.Fatal("expected error for server 500")
	}
}

// TestWaitTask_ImmediateStopped: first poll returns stopped(OK) immediately.
func TestWaitTask_ImmediateStopped(t *testing.T) {
	t.Parallel()

	const node, upid = "pve8", "UPID:pve8:0000123B:00000008:AABBCCE4:qmstart:107:root@pam:"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "OK")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	taskStatus, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("immediate stopped: unexpected error: %v", err)
	}

	if taskStatus == nil || taskStatus.Status != taskStoppedVal {
		t.Errorf("immediate stopped: unexpected status: %#v", taskStatus)
	}
}

// TestWaitTask_UPIDPreservedInStatus: UpID field in Status matches the passed value.
func TestWaitTask_UPIDPreservedInStatus(t *testing.T) {
	t.Parallel()

	const (
		node = "pve9"
		upid = "UPID:pve9:0000123C:00000009:AABBCCE5:qmstart:108:root@pam:"
	)

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"
	handler := taskStatusHandler(apiPath, 0, "OK")

	srv := httptest.NewServer(handler)
	defer srv.Close()

	svc := tasks.New(newTestClient(t, srv))

	taskStatus, err := svc.Wait(context.Background(), node, upid, &tasks.WaitOptions{
		TimeoutSeconds: 5,
		IntervalMillis: 5,
	})
	if err != nil {
		t.Fatalf("UPID preserved: unexpected error: %v", err)
	}

	if taskStatus.UpID != upid {
		t.Errorf("UpID: want %q, got %q", upid, taskStatus.UpID)
	}
}

// ---------------------------------------------------------------------------
// GetStatus — single-shot task status read (§7.28 adaptive-poll surface)
// ---------------------------------------------------------------------------

// TestGetStatus_Running verifies that GetStatus returns a non-nil Status with
// Status=="running" and a nil error when the task is still in progress.
func TestGetStatus_Running(t *testing.T) {
	t.Parallel()

	const node = "pve1"
	const upid = "UPID:pve1:0:0:0:0:0:0:root@pam"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"

	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPath {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":    map[string]any{"status": "running", "progress": 0.42},
			"success": 1,
		})
	}))

	svc := tasks.New(newTestClient(t, srv))

	st, err := svc.GetStatus(context.Background(), node, upid)
	if err != nil {
		t.Fatalf("GetStatus running: unexpected error: %v", err)
	}

	if st.Status != "running" {
		t.Errorf("Status = %q, want %q", st.Status, "running")
	}

	if st.Progress < 0.41 || st.Progress > 0.43 {
		t.Errorf("Progress = %v, want ~0.42", st.Progress)
	}

	if st.UpID != upid {
		t.Errorf("UpID = %q, want %q", st.UpID, upid)
	}
}

// TestGetStatus_Stopped verifies that GetStatus returns a Status with
// Status=="stopped" and ExitStatus=="OK" without error when task is done.
func TestGetStatus_Stopped(t *testing.T) {
	t.Parallel()

	const node = "pve1"
	const upid = "UPID:pve1:0:0:0:0:0:0:root@pam"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"

	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPath {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":    map[string]any{"status": "stopped", "exitstatus": "OK"},
			"success": 1,
		})
	}))

	svc := tasks.New(newTestClient(t, srv))

	st, err := svc.GetStatus(context.Background(), node, upid)
	if err != nil {
		t.Fatalf("GetStatus stopped: unexpected error: %v", err)
	}

	if st.Status != "stopped" {
		t.Errorf("Status = %q, want %q", st.Status, "stopped")
	}

	if st.ExitStatus != "OK" {
		t.Errorf("ExitStatus = %q, want %q", st.ExitStatus, "OK")
	}
}

// TestGetStatus_FailedTask verifies that GetStatus returns a Status with
// ExitStatus=="FAILED" and a nil error — GetStatus never synthesises errTaskFailed.
func TestGetStatus_FailedTask(t *testing.T) {
	t.Parallel()

	const node = "pve1"
	const upid = "UPID:pve1:0:0:0:0:0:0:root@pam"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"

	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPath {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":    map[string]any{"status": "stopped", "exitstatus": "FAILED"},
			"success": 1,
		})
	}))

	svc := tasks.New(newTestClient(t, srv))

	st, err := svc.GetStatus(context.Background(), node, upid)
	if err != nil {
		t.Fatalf("GetStatus failed-task: expected nil error from GetStatus, got: %v", err)
	}

	if st.ExitStatus != "FAILED" {
		t.Errorf("ExitStatus = %q, want %q", st.ExitStatus, "FAILED")
	}
}

// TestGetStatus_ZeroProgressWhenAbsent verifies that absent progress field
// in the API response yields Progress==0 (parseProgress default).
func TestGetStatus_ZeroProgressWhenAbsent(t *testing.T) {
	t.Parallel()

	const node = "pve1"
	const upid = "UPID:pve1:0:0:0:0:0:0:root@pam"

	apiPath := "/api2/json/nodes/" + node + "/tasks/" + upid + "/status"

	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPath {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// No "progress" key — PVE omits it when not applicable.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":    map[string]any{"status": "running"},
			"success": 1,
		})
	}))

	svc := tasks.New(newTestClient(t, srv))

	st, err := svc.GetStatus(context.Background(), node, upid)
	if err != nil {
		t.Fatalf("GetStatus no-progress: unexpected error: %v", err)
	}

	if st.Progress != 0 {
		t.Errorf("Progress = %v, want 0 when field absent", st.Progress)
	}
}

// TestGetStatus_ContextCancellation verifies that a cancelled context propagates
// as an error from GetStatus.
func TestGetStatus_ContextCancellation(t *testing.T) {
	t.Parallel()

	const node = "pve1"
	const upid = "UPID:pve1:0:0:0:0:0:0:root@pam"

	// Server that blocks until the test ends — ensures cancellation wins.
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		http.Error(w, "canceled", http.StatusGatewayTimeout)
	}))

	svc := tasks.New(newTestClient(t, srv))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := svc.GetStatus(ctx, node, upid)
	if err == nil {
		t.Error("GetStatus context-cancel: expected error, got nil")
	}
}
