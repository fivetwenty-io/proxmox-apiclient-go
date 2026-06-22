package batch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/batch"
)

// TestMalformedBodyCountedAsFailure is a regression test: a 2xx response whose
// body is not valid JSON must be counted as FailureCount, not SuccessCount.
// executeRequest returns Error!="" for undecodable bodies, so processRequest
// must increment FailureCount rather than SuccessCount.
func TestMalformedBodyCountedAsFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not json {{{"))
	}))
	defer srv.Close()

	cfg := &batch.Config{
		MaxBatchSize:        10,
		MaxConcurrency:      2,
		Timeout:             5 * time.Second,
		RetryFailedRequests: false,
		MaxRetries:          0,
	}

	b := batch.New(cfg)

	addErr := b.Add(&batch.Request{
		ID:      "malformed-1",
		Method:  methodGET,
		Path:    srv.URL,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})
	if addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}

	executor := batch.NewExecutor(&http.Client{}, cfg)

	result, err := executor.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1 (malformed JSON body must be a failure)", result.FailureCount)
	}

	if result.SuccessCount != 0 {
		t.Errorf("SuccessCount = %d, want 0", result.SuccessCount)
	}

	resp, ok := result.Responses["malformed-1"]
	if !ok {
		t.Fatal("response for malformed-1 not found in result")
	}

	if resp.Error == "" {
		t.Error("Response.Error must be non-empty for malformed body")
	}
}

// TestEmptyBody204CountedAsSuccess verifies that a 204 No Content response
// (empty body / EOF) is counted as a success, not a failure.
func TestEmptyBody204CountedAsSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &batch.Config{
		MaxBatchSize:        10,
		MaxConcurrency:      2,
		Timeout:             5 * time.Second,
		RetryFailedRequests: false,
		MaxRetries:          0,
	}

	b := batch.New(cfg)

	addErr := b.Add(&batch.Request{
		ID:      "empty-204",
		Method:  methodGET,
		Path:    srv.URL,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})
	if addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}

	executor := batch.NewExecutor(&http.Client{}, cfg)

	result, err := executor.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1 (empty 204 must be a success)", result.SuccessCount)
	}

	if result.FailureCount != 0 {
		t.Errorf("FailureCount = %d, want 0", result.FailureCount)
	}
}
