// Package pool wraps a single shared *http.Client (and its *http.Transport)
// behind a Get/Put/Do API, plus request-level accounting. It does not
// maintain a pool of distinct client instances: every Get returns the shared
// *http.Client in effect, and Put only decrements a counter. Actual TCP connection
// reuse across concurrent requests comes from http.Transport's own internal
// idle-connection pool (configured here via MaxConnections and
// MaxConnectionsPerHost), not from anything this package does itself.
//
// Stats reflects that: ActiveConnections/IdleConnections/TotalConnections
// count Get/Put calls (concurrent callers holding a reference to the shared
// client), not distinct pooled network connections — the number of actual
// open sockets is managed internally by http.Transport and is not exposed
// here. This package is a standalone opt-in utility: it is not wired into
// pkg/client.Client. Construct one directly (pool.New(cfg)) and use its
// Do/DoWithContext methods, or pull the *http.Client via Get, when you want
// this accounting layer around outbound HTTP calls.
//
// net/http reads a transport's idle limits while requests run, so
// SetMaxConnections and SetMaxConnectionsPerHost never write the transport in
// use. They build a new *http.Transport and *http.Client from the updated
// configuration and make that pair the shared client, which later Get calls
// return. A client obtained earlier keeps working on its own transport. That
// transport's idle connections are closed when it is replaced, and every
// transport the pool builds has a finite IdleConnTimeout (Config.IdleTimeout,
// or 90 seconds when that is zero or negative), so a connection it pools
// afterwards is closed at the latest that long after its request ends.
package pool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/swaptransport"
)

var (
	ErrPoolClosed = errors.New("pool is closed")
)

// Config represents the connection pool configuration.
type Config struct {
	// MaxConnections is the maximum number of idle connections.
	MaxConnections int

	// MaxConnectionsPerHost is the maximum number of idle connections per host.
	MaxConnectionsPerHost int

	// IdleTimeout is the maximum amount of time a connection may be idle.
	// Zero or negative means 90 seconds, net/http's DefaultTransport value.
	IdleTimeout time.Duration

	// ConnectionTimeout is the maximum amount of time to wait for a connection.
	ConnectionTimeout time.Duration

	// MaxIdleTime is the maximum amount of time an idle connection is kept.
	MaxIdleTime time.Duration

	// EnableHTTP2 enables HTTP/2 support.
	EnableHTTP2 bool
}

// DefaultConfig returns the default pool configuration.
func DefaultConfig() *Config {
	return &Config{
		MaxConnections:        constants.MaxConnections,
		MaxConnectionsPerHost: constants.MaxConnectionsPerHost,
		IdleTimeout:           constants.LongTimeout(),
		ConnectionTimeout:     constants.DefaultClientTimeout(),
		MaxIdleTime:           constants.MaxIdleTimeout(),
		EnableHTTP2:           true,
	}
}

// Pool wraps one shared *http.Client/*http.Transport pair with
// concurrency-usage accounting. See the package doc: it does not hold a set
// of distinct pooled connections itself — connection reuse is delegated to
// http.Transport's internal idle-connection pool.
type Pool struct {
	config *Config

	// client is the shared client in effect. The setters replace it, and its
	// transport, under mu rather than writing either.
	client *http.Client

	// retired holds the most recent transports the setters replaced, so Close
	// still reaches connections released to them afterwards.
	retired swaptransport.Retired

	mu     sync.RWMutex
	stats  *poolStats
	closed bool
}

// Stats reports request-level usage of the shared client, not distinct
// pooled network connections (there is only one *http.Client/*http.Transport
// pair; see the package doc).
type Stats struct {
	// ActiveConnections counts Get calls not yet matched by a Put — i.e.
	// callers currently holding a reference to the shared client.
	ActiveConnections int64
	// IdleConnections counts completed Put calls; it only ever increases and
	// is not decremented by a subsequent Get. It is a lifetime counter of
	// "checked back in" events, not a live count of idle sockets.
	IdleConnections int64
	// TotalConnections counts every Get call made against this Pool.
	TotalConnections int64
	// FailedConnections counts requests where client.Do returned an error.
	FailedConnections int64
	// RequestsServed counts every DoWithContext call, regardless of outcome.
	RequestsServed int64
	// BytesSent sums req.ContentLength across served requests that report a
	// positive length; requests with unknown or chunked length are not counted.
	BytesSent int64
	// BytesReceived sums resp.ContentLength across served requests that
	// report a positive length; responses with unknown or chunked length are
	// not counted.
	BytesReceived int64
	// AverageResponseTime is a running average of Do/DoWithContext latency
	// across served requests.
	AverageResponseTime time.Duration
}

// poolStats is the internal, mutex-guarded counter state. Stats (the embedded
// value) is the pure-data snapshot handed back by Pool.Stats; keeping the lock
// off the exported type means callers can copy a snapshot without copying a
// mutex.
type poolStats struct {
	Stats

	mu sync.RWMutex
}

// New creates a new connection pool with the given configuration.
func New(config *Config) *Pool {
	if config == nil {
		config = DefaultConfig()
	}

	return &Pool{
		config: config,
		client: newClient(config),
		mu:     sync.RWMutex{},
		stats:  &poolStats{},
		closed: false,
	}
}

// newClient builds a new *http.Client and *http.Transport from config. The
// transport's IdleConnTimeout is always finite; see the package doc.
func newClient(config *Config) *http.Client {
	transport := &http.Transport{
		Proxy:                  nil,
		OnProxyConnectResponse: nil,
		DialContext:            nil,
		DialTLSContext:         nil,
		TLSClientConfig:        nil,
		TLSHandshakeTimeout:    time.Duration(0),
		DisableKeepAlives:      false,
		DisableCompression:     false,
		MaxIdleConns:           config.MaxConnections,
		MaxIdleConnsPerHost:    config.MaxConnectionsPerHost,
		MaxConnsPerHost:        0,
		IdleConnTimeout:        swaptransport.FiniteIdleTimeout(config.IdleTimeout),
		ResponseHeaderTimeout:  config.ConnectionTimeout,
		ExpectContinueTimeout:  time.Duration(0),
		TLSNextProto:           nil,
		ProxyConnectHeader:     nil,
		GetProxyConnectHeader:  nil,
		MaxResponseHeaderBytes: 0,
		WriteBufferSize:        0,
		ReadBufferSize:         0,
		ForceAttemptHTTP2:      config.EnableHTTP2,
		HTTP2:                  nil,
		Protocols:              nil,
	}

	return &http.Client{
		Transport:     transport,
		CheckRedirect: nil,
		Jar:           nil,
		Timeout:       config.ConnectionTimeout,
	}
}

// Get returns the shared *http.Client in effect and increments the
// active/total usage counters. Its Transport is an *http.Transport. Get
// returns the same *http.Client until SetMaxConnections or
// SetMaxConnectionsPerHost changes a limit, and the new shared client after
// that; it does not check out a distinct connection.
func (p *Pool) Get() (*http.Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return nil, ErrPoolClosed
	}

	p.stats.mu.Lock()
	p.stats.ActiveConnections++
	p.stats.TotalConnections++
	p.stats.mu.Unlock()

	return p.client, nil
}

// Put records that a caller is done with the client obtained from Get,
// decrementing ActiveConnections and incrementing IdleConnections. The
// client parameter is accepted for symmetry with Get but is not used to
// select or release an actual connection — there is only the one shared
// *http.Client.
func (p *Pool) Put(client *http.Client) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return
	}

	p.stats.mu.Lock()
	// Guard against a Put without a matching Get (or a double Put) driving the
	// active counter negative; these counters are observational, not a semaphore.
	if p.stats.ActiveConnections > 0 {
		p.stats.ActiveConnections--
	}

	p.stats.IdleConnections++
	p.stats.mu.Unlock()
}

// Do executes an HTTP request using a pooled connection.
func (p *Pool) Do(req *http.Request) (*http.Response, error) {
	return p.DoWithContext(context.Background(), req)
}

// DoWithContext executes an HTTP request with context using a pooled connection.
func (p *Pool) DoWithContext(ctx context.Context, req *http.Request) (*http.Response, error) {
	client, err := p.Get()
	if err != nil {
		return nil, err
	}
	defer p.Put(client)

	start := time.Now()
	req = req.WithContext(ctx)

	// Track request
	p.stats.mu.Lock()

	p.stats.RequestsServed++
	if req.ContentLength > 0 {
		p.stats.BytesSent += req.ContentLength
	}

	p.stats.mu.Unlock()

	resp, err := client.Do(req)
	if err != nil {
		p.stats.mu.Lock()
		p.stats.FailedConnections++
		p.stats.mu.Unlock()

		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	// Track response
	elapsed := time.Since(start)

	p.stats.mu.Lock()

	if resp.ContentLength > 0 {
		p.stats.BytesReceived += resp.ContentLength
	}
	// Update average response time
	if p.stats.AverageResponseTime == 0 {
		p.stats.AverageResponseTime = elapsed
	} else {
		p.stats.AverageResponseTime = (p.stats.AverageResponseTime + elapsed) / constants.AverageResponseTimeDivisor
	}

	p.stats.mu.Unlock()

	return resp, nil
}

// Stats returns the current pool statistics.
func (p *Pool) Stats() Stats {
	p.stats.mu.RLock()
	defer p.stats.mu.RUnlock()

	return p.stats.Stats
}

// Close closes the connection pool.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true
	p.client.CloseIdleConnections()
	p.retired.CloseIdleConnections()

	return nil
}

// SetMaxConnections updates the maximum number of idle connections
// (MaxIdleConns) for subsequent requests. It is safe to call while requests
// run: when the limit changes, it builds a new shared client and transport
// rather than writing the transport in use, as the package doc describes.
// Requests already in flight, and clients obtained from Get before the call,
// keep using the replaced transport.
func (p *Pool) SetMaxConnections(maxConns int) {
	p.retune(func(config *Config) bool {
		if config.MaxConnections == maxConns {
			return false
		}

		config.MaxConnections = maxConns

		return true
	})
}

// SetMaxConnectionsPerHost updates the maximum idle connections per host
// (MaxIdleConnsPerHost) for subsequent requests, in the same way as
// SetMaxConnections.
func (p *Pool) SetMaxConnectionsPerHost(maxConnsPerHost int) {
	p.retune(func(config *Config) bool {
		if config.MaxConnectionsPerHost == maxConnsPerHost {
			return false
		}

		config.MaxConnectionsPerHost = maxConnsPerHost

		return true
	})
}

// IsHealthy checks if the pool is healthy.
func (p *Pool) IsHealthy() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return false
	}

	stats := p.Stats()
	// Consider unhealthy if too many failures
	if stats.TotalConnections > 0 {
		failureRate := float64(stats.FailedConnections) / float64(stats.TotalConnections)
		if failureRate > constants.FailureRateThreshold {
			return false
		}
	}

	return true
}

// retune applies change to the configuration and, when it reports a change on
// an open pool, installs a new shared client built from it and retires the
// transport it replaced. A closed pool records the change only.
func (p *Pool) retune(change func(config *Config) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !change(p.config) || p.closed {
		return
	}

	old := p.client
	p.client = newClient(p.config)

	if oldTransport, ok := old.Transport.(*http.Transport); ok {
		p.retired.Add(oldTransport)
	}
}
