package stream_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/stream"
)

// formatLines is the Config.Format of a JSON Lines stream.
const formatLines = "jsonlines"

// TestNewFromResponseReadErrorLeavesNoServerText reads a reply whose trailer
// line the transport cannot read through a stream built from the response.
// The error must hold none of the trailer's bytes.
func TestNewFromResponseReadErrorLeavesNoServerText(t *testing.T) {
	t.Parallel()

	for _, format := range []string{formatLines, "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.ServeRaw(t, redirecttest.BadTrailer("{\"a\":1}\n{\"b\":"))

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL()+"/x", nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}

			defer func() { _ = resp.Body.Close() }()

			config := stream.DefaultConfig()
			config.Format = format

			body := stream.NewFromResponse(resp, config)

			defer func() { _ = body.Close() }()

			_, err = body.ReadAll()
			if err == nil {
				t.Fatal("got no error, want the body read to fail")
			}

			redirecttest.AssertNoTrailerText(t, err.Error())
		})
	}
}
