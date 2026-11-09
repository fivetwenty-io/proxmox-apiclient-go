package http //nolint:testpackage // white-box test: inspects the client's unexported transport holder

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
)

var errNilWrapDial = errors.New("nil wrap dial stub")

// v3110Transport is what NewClient set on its *http.Transport in v3.11.0,
// which is the transport the http.Client sent through directly. Every field
// the release did not set stays at its zero value.
type v3110Transport struct {
	maxIdleConns        int
	maxIdleConnsPerHost int
	idleConnTimeout     time.Duration
	tlsHandshakeTimeout time.Duration

	// proxy is what the transport's Proxy function must return, and nil means
	// the transport has no Proxy function.
	proxy *url.URL

	// dialErr is the error a caller-supplied DialContext returns, and nil
	// means the transport has none. dialBuilt is true when NewClient built a
	// net.Dialer from the dial timeout knobs.
	dialErr   error
	dialBuilt bool
}

func (want v3110Transport) verify(t *testing.T, got *http.Transport) {
	t.Helper()

	if got.MaxIdleConns != want.maxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d", got.MaxIdleConns, want.maxIdleConns)
	}

	if got.MaxIdleConnsPerHost != want.maxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", got.MaxIdleConnsPerHost, want.maxIdleConnsPerHost)
	}

	if got.IdleConnTimeout != want.idleConnTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v", got.IdleConnTimeout, want.idleConnTimeout)
	}

	if got.TLSHandshakeTimeout != want.tlsHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", got.TLSHandshakeTimeout, want.tlsHandshakeTimeout)
	}

	want.verifyProxy(t, got)
	want.verifyDial(t, got)

	if got.DisableCompression || got.DisableKeepAlives || got.ForceAttemptHTTP2 {
		t.Error("DisableCompression, DisableKeepAlives, or ForceAttemptHTTP2 is set, and v3.11.0 left them false")
	}

	if got.ResponseHeaderTimeout != 0 || got.ExpectContinueTimeout != 0 || got.MaxConnsPerHost != 0 ||
		got.MaxResponseHeaderBytes != 0 || got.TLSNextProto != nil || got.DialTLSContext != nil ||
		got.OnProxyConnectResponse != nil || got.GetProxyConnectHeader != nil {
		t.Error("a transport field that v3.11.0 left at its zero value is set")
	}
}

func (want v3110Transport) verifyProxy(t *testing.T, got *http.Transport) {
	t.Helper()

	if want.proxy == nil {
		if got.Proxy != nil {
			t.Error("Proxy is set, want nil so requests go direct")
		}

		return
	}

	if got.Proxy == nil {
		t.Fatal("Proxy = nil, want Options.Proxy")
	}

	gotURL, err := got.Proxy(&http.Request{URL: &url.URL{Scheme: protoHTTPS, Host: "pve.invalid"}})
	if err != nil || gotURL != want.proxy {
		t.Errorf("Proxy() = %v, %v, want %v", gotURL, err, want.proxy)
	}
}

func (want v3110Transport) verifyDial(t *testing.T, got *http.Transport) {
	t.Helper()

	switch {
	case want.dialErr != nil:
		if got.DialContext == nil {
			t.Fatal("DialContext = nil, want Options.DialContext")
		}

		_, err := got.DialContext(context.Background(), "tcp", "127.0.0.1:1")
		if !errors.Is(err, want.dialErr) {
			t.Errorf("DialContext error = %v, want %v", err, want.dialErr)
		}
	case want.dialBuilt:
		if got.DialContext == nil {
			t.Error("DialContext = nil, want a dialer built from the dial timeout knobs")
		}
	default:
		if got.DialContext != nil {
			t.Error("DialContext is set, want nil when no dial knob is set")
		}
	}
}

// nilWrapTransportCase is one set of options and the v3.11.0 transport they
// produce.
type nilWrapTransportCase struct {
	configure func(*Options)
	want      v3110Transport
}

// nilWrapTransportCases lists the option sets the default path is checked
// against. The expectations are written out from the v3.11.0 construction
// rather than read back from createHTTPTransport, so a change to that
// function shows up in the test.
func nilWrapTransportCases() map[string]nilWrapTransportCase {
	proxyURL := &url.URL{Scheme: testProtoHTTP, Host: "proxy.invalid:3128"}

	return map[string]nilWrapTransportCase{
		"knobs unset": {
			configure: func(*Options) {},
			want: v3110Transport{
				maxIdleConns:        5,
				maxIdleConnsPerHost: 5,
				idleConnTimeout:     constants.LongTimeout(),
			},
		},
		"every knob set": {
			configure: func(opts *Options) {
				opts.KeepAlive = 7
				opts.MaxIdleConnsPerHost = 3
				opts.IdleConnTimeoutSec = 11
				opts.TLSHandshakeTimeoutSec = 4
				opts.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
				opts.DialContext = func(context.Context, string, string) (net.Conn, error) {
					return nil, errNilWrapDial
				}
			},
			want: v3110Transport{
				maxIdleConns:        7,
				maxIdleConnsPerHost: 3,
				idleConnTimeout:     11 * time.Second,
				tlsHandshakeTimeout: 4 * time.Second,
				proxy:               proxyURL,
				dialErr:             errNilWrapDial,
			},
		},
		"dial timeouts only": {
			configure: func(opts *Options) {
				opts.DialTimeoutSec = 2
				opts.TCPKeepAliveSec = 9
			},
			want: v3110Transport{
				maxIdleConns:        5,
				maxIdleConnsPerHost: 5,
				idleConnTimeout:     constants.LongTimeout(),
				dialBuilt:           true,
			},
		},
	}
}

// TestNewClient_NilWrapTransportKeepsTheV3110Transport pins the default path.
// With no WrapTransport the http.Client sends through an internal holder, and
// the *http.Transport inside it must carry what NewClient set on the
// transport in v3.11.0, which is the idle limits, the timeouts, the proxy, and
// the dial function, with every other field left at its zero value.
func TestNewClient_NilWrapTransportKeepsTheV3110Transport(t *testing.T) {
	t.Parallel()

	for name, tc := range nilWrapTransportCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := minimalHTTPOptions()
			tc.configure(opts)

			client, err := NewClient(opts)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			tc.want.verify(t, liveTransport(t, client))
		})
	}
}

// TestNewClient_NilWrapTransportKeepsTheV3110TLSConfig covers the HTTPS half
// of the default path. The transport inside the holder carries the TLS
// configuration v3.11.0 built, and an http client carries none.
func TestNewClient_NilWrapTransportKeepsTheV3110TLSConfig(t *testing.T) {
	t.Parallel()

	withSSL := func(protocol string, ssl *SSLOptions) *Options {
		opts := minimalHTTPOptions()
		opts.Protocol = protocol
		opts.SSLOptions = ssl

		return opts
	}

	cases := map[string]struct {
		options      *Options
		wantTLS      bool
		wantInsecure bool
	}{
		"http":                 {options: withSSL(testProtoHTTP, nil)},
		"https without verify": {options: withSSL(protoHTTPS, &SSLOptions{VerifyMode: SSLVerifyNone}), wantTLS: true, wantInsecure: true},
		"https with verify":    {options: withSSL(protoHTTPS, &SSLOptions{VerifyMode: SSLVerifyPeer}), wantTLS: true},
		"https without ssl":    {options: withSSL(protoHTTPS, nil), wantTLS: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, err := NewClient(tc.options)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			got := liveTransport(t, client).TLSClientConfig

			if !tc.wantTLS {
				if got != nil {
					t.Fatalf("TLSClientConfig = %+v, want nil for an http client", got)
				}

				return
			}

			if got == nil {
				t.Fatal("TLSClientConfig = nil, want the configuration NewClient built")
			}

			if got.InsecureSkipVerify != tc.wantInsecure {
				t.Errorf("InsecureSkipVerify = %v, want %v", got.InsecureSkipVerify, tc.wantInsecure)
			}

			if got.MinVersion != tls.VersionTLS12 {
				t.Errorf("MinVersion = %#x, want TLS 1.2", got.MinVersion)
			}

			if got.SessionTicketsDisabled || got.VerifyPeerCertificate != nil || len(got.Certificates) != 0 || got.RootCAs != nil {
				t.Errorf("TLSClientConfig carries state it was not asked for: %+v", got)
			}
		})
	}
}
