package pool_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

// redirectPair starts a target listener and a front listener that answers every
// request with a 307 to the target, which sits on another port of the same
// host. The counters hold what each read, and the target also records whether
// a request carried the Authorization header.
func redirectPair(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32, *atomic.Bool) {
	t.Helper()

	frontHits, targetHits, targetAuth := new(atomic.Int32), new(atomic.Int32), new(atomic.Bool)

	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)

		if r.Header.Get("Authorization") != "" {
			targetAuth.Store(true)
		}
	}))
	t.Cleanup(target.Close)

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		frontHits.Add(1)
		secretTarget := strings.Replace(target.URL, "http://", "http://user:HUNTER2@", 1) + "/x?ticket=QSECRET"
		w.Header().Set("Location", secretTarget)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(front.Close)

	return front, frontHits, targetHits, targetAuth
}

func getWithToken(t *testing.T, ctx context.Context, p *pool.Pool, rawURL string) error {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	req.Header.Set("Authorization", "PVEAPIToken=root@pam!t=secret")

	resp, err := p.DoWithContext(ctx, req)
	if err != nil {
		return fmt.Errorf("pool request: %w", err)
	}

	_ = resp.Body.Close()

	return nil
}

func TestPoolRefusesRedirectByDefault(t *testing.T) {
	t.Parallel()

	front, frontHits, targetHits, targetAuth := redirectPair(t)
	poolInstance := pool.New(nil)

	t.Cleanup(func() { _ = poolInstance.Close() })

	err := getWithToken(t, context.Background(), poolInstance, front.URL)
	if !errors.Is(err, pve.ErrRedirectRefused) {
		t.Fatalf("error = %v, want pve.ErrRedirectRefused", err)
	}

	if strings.Contains(err.Error(), "HUNTER2") || strings.Contains(err.Error(), "QSECRET") {
		t.Errorf("error %q holds credentials from the Location header", err.Error())
	}

	if targetHits.Load() != 0 || targetAuth.Load() {
		t.Errorf("redirect target read %d requests, token seen %v, want none", targetHits.Load(), targetAuth.Load())
	}

	if got := frontHits.Load(); got != 1 {
		t.Errorf("front listener read %d requests, want 1", got)
	}
}

func TestPoolCheckRedirectOptsIn(t *testing.T) {
	t.Parallel()

	front, _, targetHits, _ := redirectPair(t)

	config := pool.DefaultConfig()
	config.CheckRedirect = pve.FollowRedirects(1)

	poolInstance := pool.New(config)

	t.Cleanup(func() { _ = poolInstance.Close() })

	err := getWithToken(t, context.Background(), poolInstance, front.URL)
	if err != nil {
		t.Fatalf("request with the opt-in failed: %v", err)
	}

	if got := targetHits.Load(); got != 1 {
		t.Errorf("redirect target read %d requests, want 1", got)
	}
}
