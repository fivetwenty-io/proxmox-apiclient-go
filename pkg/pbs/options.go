package pbs

import (
	"fmt"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// Proxmox Backup Server wire-protocol defaults. PBS speaks the same
// /api2/json REST dialect as PVE but listens on its own port and names
// its credentials differently.
const (
	// DefaultPort is the PBS API port (PVE uses 8006).
	DefaultPort = 8007

	// APITokenName is the Authorization header prefix for PBS API
	// tokens (PVE uses "PVEAPIToken").
	APITokenName = "PBSAPIToken"

	// CookieName is the ticket cookie name for PBS session auth
	// (PVE uses "PVEAuthCookie").
	CookieName = "PBSAuthCookie"
)

// DefaultOptions fills the PBS-specific fields of base that are still
// zero-valued: Port, APITokenName, and CookieName. Every other field is
// passed through untouched, so callers configure host, credentials,
// TLS, logging, etc. exactly as they would for a PVE client.
func DefaultOptions(base client.Options) client.Options {
	if base.Port == 0 {
		base.Port = DefaultPort
	}

	if base.APITokenName == "" {
		base.APITokenName = APITokenName
	}

	if base.CookieName == "" {
		base.CookieName = CookieName
	}

	return base
}

// NewClient builds a client.Client for a Proxmox Backup Server from
// base, applying the PBS defaults via DefaultOptions first. The
// returned client is what the generated pkg/pbs/* service constructors
// expect.
func NewClient(base client.Options) (client.Client, error) { //nolint:ireturn // mirrors client.NewClient's factory pattern
	cli, err := client.NewClient(DefaultOptions(base))
	if err != nil {
		return nil, fmt.Errorf("pbs: new client: %w", err)
	}

	return cli, nil
}
