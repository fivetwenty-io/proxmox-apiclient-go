package http //nolint:testpackage // white-box test: builds clients with the shared hostile helpers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/cache"
)

// TestBodyReadErrorsLeaveNoServerText sends a reply whose trailer line the
// transport cannot read, through every path that reads a body after Do. The
// error must hold none of the trailer's bytes and must name the requested URL.
func TestBodyReadErrorsLeaveNoServerText(t *testing.T) {
	t.Parallel()

	paths := map[string]struct {
		mode func(*Options)
		call func(*Client) error
	}{
		"do": {
			func(*Options) {},
			func(c *Client) error {
				_, err := c.DoWithContext(context.Background(), http.MethodGet, "/version", nil)

				return err
			},
		},
		"do bytes": {
			func(*Options) {},
			func(c *Client) error {
				_, err := c.DoBytesWithContext(context.Background(), http.MethodGet, "/version", nil)

				return err
			},
		},
		"cached do": {
			func(o *Options) {
				o.CacheConfig = &cache.Config{
					MaxSize: 1 << 20, DefaultTTL: time.Minute, CleanupInterval: time.Minute, Enabled: true,
				}
			},
			func(c *Client) error {
				_, err := c.DoWithContext(context.Background(), http.MethodGet, "/version", nil)

				return err
			},
		},
	}

	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.ServeRaw(t, redirecttest.BadTrailer(`{"data":{}}`))
			client, captured := newHostileClient(t, server, path.mode, false)

			err := path.call(client)
			if err == nil {
				t.Fatal("got no error, want the body read to fail")
			}

			redirecttest.AssertNoTrailerText(t, err.Error())

			for _, line := range captured.all() {
				redirecttest.AssertNoTrailerText(t, line)
			}

			var header *redirect.HeaderError
			if !errors.As(err, &header) || header.From == "" {
				t.Errorf("error = %v, want a header error that names the request", err)
			}
		})
	}
}

// TestResponseParserBodyReadErrorLeavesNoServerText reads the same reply with
// ResponseParser.
func TestResponseParserBodyReadErrorLeavesNoServerText(t *testing.T) {
	t.Parallel()

	server := redirecttest.ServeRaw(t, redirecttest.BadTrailer(`{"data":{}}`))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL()+"/version", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := (&http.Client{Transport: redirect.Guard(nil, nil)}).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	var target map[string]interface{}

	err = NewResponseParser().Parse(resp, &target)
	if err == nil {
		t.Fatal("got no error, want the body read to fail")
	}

	redirecttest.AssertNoTrailerText(t, err.Error())
}
