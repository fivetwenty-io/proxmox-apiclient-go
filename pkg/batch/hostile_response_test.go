package batch_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/batch"
)

// TestHostileResponsesLeaveNoSecretInABatchResponse runs every hostile Location
// value through an executor whose client is a plain *http.Client, which follows
// redirects and quotes the header in its errors. The executor puts the error
// text into the response, so it must hold none of the secrets in the header.
func TestHostileResponsesLeaveNoSecretInABatchResponse(t *testing.T) {
	t.Parallel()

	for _, hostile := range redirecttest.Cases(redirecttest.DeadAddr(t)) {
		t.Run(hostile.Name, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.Serve(t, http.StatusFound, hostile.Location)

			cfg := &batch.Config{
				MaxBatchSize: 1, MaxConcurrency: 1, Timeout: 10 * time.Second, RetryFailedRequests: false, MaxRetries: 0,
			}
			batchInstance := batch.New(cfg)

			err := batchInstance.Add(&batch.Request{ID: "hostile", Method: methodGET, Path: server.URL() + pathTest})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}

			result, err := batch.NewExecutor(&http.Client{}, cfg).Execute(
				context.Background(), batchInstance,
			)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}

			resp := result.Responses["hostile"]
			if resp == nil || resp.Error == "" {
				t.Fatalf("response = %+v, want an error", resp)
			}

			redirecttest.AssertCleanText(t, resp.Error)
		})
	}
}

// TestTransportErrorsLeaveNoRequestSecretsInABatchResponse sends a request whose
// URL holds user information, a query secret, and a fragment secret to a host
// that is down. The executor's response text must show the URL as scheme, host,
// and path only.
func TestTransportErrorsLeaveNoRequestSecretsInABatchResponse(t *testing.T) {
	t.Parallel()

	target := "http://user:" + redirecttest.SecretUser + "@" + redirecttest.DeadAddr(t) +
		"/x?ticket=" + redirecttest.SecretQuery + "#" + redirecttest.SecretFragment

	cfg := &batch.Config{MaxBatchSize: 1, MaxConcurrency: 1, Timeout: 10 * time.Second}
	batchInstance := batch.New(cfg)

	err := batchInstance.Add(&batch.Request{ID: "down", Method: methodGET, Path: target})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	result, err := batch.NewExecutor(&http.Client{}, cfg).Execute(context.Background(), batchInstance)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	resp := result.Responses["down"]
	if resp == nil || resp.Error == "" {
		t.Fatalf("response = %+v, want an error", resp)
	}

	redirecttest.AssertCleanText(t, resp.Error)
}
