// Package pool wraps a single shared *http.Client (and its *http.Transport)
// behind a Get/Put/Do API, plus request-level accounting. It does not
// maintain a pool of distinct client instances: every Get returns the same
// *http.Client, and Put only decrements a counter. Actual TCP connection
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
// SetMaxConnections and SetMaxConnectionsPerHost change the idle limits of
// the *http.Transport the pool created, in place. net/http reads those limits
// while requests run and offers no way to change them on a transport in use
// without a data race, so call the setters before the pool's transport sends
// its first request. A request that has returned, or was canceled, doesn't
// make the transport idle, because net/http may still be dialing for it and
// reads the limits when that connection is pooled. The setters are safe to
// call concurrently with each other and with Get, Put, Stats, IsHealthy, and
// Close.
//
// Close closes the idle connections of the transport the pool created, even
// when a caller has replaced or wrapped the shared client's Transport through
// Get. Closing a connection calls its net.Conn's Close method, which a
// caller's dial function supplies and which may call back into the pool, so
// Close closes connections only after releasing the pool's lock.
package pool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
)

var (
	ErrPoolClosed = errors.New("pool is closed")
)

// Config represents the connection pool configuration. New copies it, so a
// Config may be shared between pools and changed after New without affecting
// any pool built from it. Every field is a plain value, so the copy is deep; a
// field that holds a slice, map, or pointer must be deep-copied in New.
type Config struct {
	// MaxConnections is the maximum number of idle connections.
	MaxConnections int

	// MaxConnectionsPerHost is the maximum number of idle connections per host.
	MaxConnectionsPerHost int

	// IdleTimeout is the maximum amount of time a connection may be idle.
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
	// config is the pool's own copy of the configuration New was given. The
	// setters record their limits in it under mu.
	config Config

	// transport is the transport New created. The pool keeps its own
	// reference, so the setters and Close reach it even after a caller
	// replaces or wraps client.Transport. The setters write its idle limits
	// under mu.
	transport *http.Transport

	// client is the shared client Get returns for the life of the pool. The
	// pool never replaces it.
	client *http.Client

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

	// Copy the caller's Config so pools built from the same one share no
	// state; see Config.
	own := *config
	transport := newTransport(&own)

	return &Pool{
		config:    own,
		transport: transport,
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: nil,
			Jar:           nil,
			Timeout:       own.ConnectionTimeout,
		},
		mu:     sync.RWMutex{},
		stats:  &poolStats{},
		closed: false,
	}
}

// newTransport builds the pool's *http.Transport from config.
func newTransport(config *Config) *http.Transport {
	return &http.Transport{
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
		IdleConnTimeout:        config.IdleTimeout,
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
}

// Get returns the shared *http.Client and increments the active/total usage
// counters. It always returns the same *http.Client instance for the life
// of the Pool; it does not check out a distinct connection. The pool does not
// guard writes to the client or its Transport, so configure them before other
// goroutines use the client.
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

// Close closes the connection pool and the idle connections of the transport
// New created, reached through the pool's own reference so a caller that
// replaced or wrapped the shared client's Transport cannot hide it. When that
// Transport is a different RoundTripper with a CloseIdleConnections method,
// Close calls it too. Close marks the pool closed under its lock and closes
// the connections after releasing it, because closing a connection runs
// caller code that may call back into the pool. It is safe to call Close more
// than once.
func (p *Pool) Close() error {
	for _, closer := range p.markClosed() {
		closer.CloseIdleConnections()
	}

	return nil
}

// SetMaxConnections updates the maximum number of idle connections
// (MaxIdleConns). It records the limit in the pool's configuration and
// changes the shared transport in place, so the client Get returns stays the
// same and every caller sees the new limit, including through a wrapper a
// caller installed around the pool's transport. net/http reads the limit
// while requests run and may still be dialing after a request returns or is
// canceled, so call SetMaxConnections before the transport sends its first
// request; it is safe against concurrent setter, Get, Put, Stats, IsHealthy,
// and Close calls.
func (p *Pool) SetMaxConnections(maxConns int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.config.MaxConnections = maxConns
	p.transport.MaxIdleConns = maxConns
}

// SetMaxConnectionsPerHost updates the maximum idle connections per host
// (MaxIdleConnsPerHost). Like SetMaxConnections, it changes the shared
// transport in place and must be called before the transport sends its
// first request.
func (p *Pool) SetMaxConnectionsPerHost(maxConnsPerHost int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.config.MaxConnectionsPerHost = maxConnsPerHost
	p.transport.MaxIdleConnsPerHost = maxConnsPerHost
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

// idleCloser is a RoundTripper that can close its idle connections, as
// *http.Transport and well-behaved wrappers can.
type idleCloser interface {
	CloseIdleConnections()
}

// markClosed marks the pool closed and returns what Close closes: the
// transport New created, and the shared client's Transport when a caller
// replaced it with a different RoundTripper that has a CloseIdleConnections
// method. It returns nothing when the pool was already closed. A nil
// Transport means http.DefaultTransport, which the pool does not own and so
// never closes.
func (p *Pool) markClosed() []idleCloser {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true

	closers := []idleCloser{p.transport}

	current := p.client.Transport
	if current == nil || current == http.RoundTripper(p.transport) {
		return closers
	}

	if closer, ok := current.(idleCloser); ok {
		closers = append(closers, closer)
	}

	return closers
}
