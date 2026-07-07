package auth_test

import (
	"sync"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

// TestAPITokenAuthenticator_ConcurrentAccess exercises SetToken against
// concurrent readers under the race detector to confirm the authenticator's
// token access is synchronized.
func TestAPITokenAuthenticator_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	authenticator := auth.NewAPITokenAuthenticator(&auth.Token{ID: "user@pam!t", Secret: "s0"}, "")

	const goroutines = 50

	var waitGroup sync.WaitGroup

	waitGroup.Add(goroutines)

	for range goroutines {
		go func() {
			defer waitGroup.Done()

			authenticator.SetToken(&auth.Token{ID: "user@pam!t", Secret: "s1"})
			_ = authenticator.GetToken()
			_ = authenticator.GetHeaders()
			_ = authenticator.IsAuthenticated()
			_ = authenticator.Authenticate()
		}()
	}

	waitGroup.Wait()
}
