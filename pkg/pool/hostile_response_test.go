package pool_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

// TestHostileResponsesLeaveNoSecretInAPoolError sends every hostile Location
// value through the pool's Do and through the client Get returns, with the
// default refusal and with a policy that follows. The error text and its %+v
// form must hold none of the secrets in the header. The client from Get is
// not wrapped by the pool, so it relies on the guard in its transport.
func TestHostileResponsesLeaveNoSecretInAPoolError(t *testing.T) {
	t.Parallel()

	modes := map[string]func(*pool.Config){
		"default": func(*pool.Config) {},
		"follow":  func(c *pool.Config) { c.CheckRedirect = pve.FollowRedirects(3) },
	}

	for _, hostile := range redirecttest.Cases(redirecttest.DeadAddr(t)) {
		for modeName, mode := range modes {
			for _, direct := range []bool{false, true} {
				// A client from Get that follows redirects shows net/http's own
				// error text, which the pool has no hook on and its
				// documentation says so.
				if direct && modeName == "follow" {
					continue
				}

				name := fmt.Sprintf("%s/%s/direct=%v", hostile.Name, modeName, direct)

				t.Run(name, func(t *testing.T) {
					t.Parallel()

					server := redirecttest.Serve(t, http.StatusTemporaryRedirect, hostile.Location)
					sendErr := sendThroughPool(t, server, mode, direct)

					redirecttest.AssertClean(t, sendErr)

					if modeName == "default" {
						if got := server.Hits(); got != 1 {
							t.Errorf("server read %d requests, want 1", got)
						}
					}
				})
			}
		}
	}
}

// sendThroughPool sends a GET to server through a pool built with mode, using
// either the client Get returns or the pool's own Do, and returns the error.
func sendThroughPool(t *testing.T, server *redirecttest.Server, mode func(*pool.Config), direct bool) error {
	t.Helper()

	config := pool.DefaultConfig()
	mode(config)

	poolInstance := pool.New(config)

	t.Cleanup(func() { _ = poolInstance.Close() })

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL()+"/api", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	var (
		resp   *http.Response
		doErr  error
		client *http.Client
	)

	if direct {
		client, err = poolInstance.Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}

		resp, doErr = client.Do(req)
	} else {
		resp, doErr = poolInstance.DoWithContext(context.Background(), req)
	}

	if resp != nil {
		_ = resp.Body.Close()
	}

	return doErr //nolint:wrapcheck // the pool's own error is what the test checks
}

// TestPoolTransportErrorsLeaveNoRequestSecrets sends a request whose URL holds
// user information, a query secret, and a fragment secret to a host that is
// down. The error from Do must show the URL as scheme, host, and path only.
func TestPoolTransportErrorsLeaveNoRequestSecrets(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(nil)

	t.Cleanup(func() { _ = poolInstance.Close() })

	target := "http://user:" + redirecttest.SecretUser + "@" + redirecttest.DeadAddr(t) +
		"/x?ticket=" + redirecttest.SecretQuery + "#" + redirecttest.SecretFragment

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := poolInstance.DoWithContext(context.Background(), req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	redirecttest.AssertClean(t, err)

	if !strings.Contains(err.Error(), "dial tcp") {
		t.Errorf("error = %v, want the dial failure kept", err)
	}
}

// TestPoolRefusesARedirectWithoutALocation checks the pool's client turns a 3xx
// answer with no Location header into the refusal, which net/http alone hands
// back as a response, for Do and for the client Get returns.
func TestPoolRefusesARedirectWithoutALocation(t *testing.T) {
	t.Parallel()

	server := redirecttest.Serve(t, http.StatusFound, nil)
	poolInstance := pool.New(nil)

	t.Cleanup(func() { _ = poolInstance.Close() })

	client, err := poolInstance.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL()+"/x", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	if !errors.Is(err, pve.ErrRedirectRefused) {
		t.Errorf("direct request error = %v, want pve.ErrRedirectRefused", err)
	}

	resp, err = poolInstance.DoWithContext(context.Background(), req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	if !errors.Is(err, pve.ErrRedirectRefused) {
		t.Errorf("Do error = %v, want pve.ErrRedirectRefused", err)
	}
}
