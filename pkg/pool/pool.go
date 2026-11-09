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
// use. They copy the shared *http.Client, give the copy a Clone of its
// *http.Transport with only the requested limit changed, and make that pair
// the shared client, which later Get calls return. Everything else a caller
// configured on the client or transport obtained from Get, such as a cookie
// jar, a redirect policy, a timeout, TLS settings, a proxy, or a dial
// function, carries over. A client obtained earlier keeps working on its own
// transport. That transport's idle connections are closed when it is
// replaced. The transport New builds has a finite IdleConnTimeout
// (Config.IdleTimeout, or 90 seconds when that is zero or negative), and its
// clones keep it, so a connection a replaced transport pools afterwards is
// closed at the latest that long after its request ends. A caller that sets
// IdleConnTimeout to zero through Get gives up that bound.
//
// When a caller has replaced the shared client's Transport with something
// other than an *http.Transport, the setters record the new limit in the
// pool's configuration and leave the client and its Transport unchanged,
// because the pool cannot clone or retune a RoundTripper it did not build.
//
// Closing idle connections calls the Close method of each net.Conn, which a
// caller's dial function supplies and which may call back into the pool. The
// pool therefore closes connections only after releasing its own locks.
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
	// config is the pool's own copy of the configuration New was given. The
	// setters record their limits in it under mu.
	config Config

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

	// Copy the caller's Config so pools built from the same one share no
	// state; see Config.
	own := *config

	return &Pool{
		config: own,
		client: newClient(&own),
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
// active/total usage counters. Its Transport is an *http.Transport unless the
// caller replaced it. Get returns the same *http.Client until
// SetMaxConnections or SetMaxConnectionsPerHost changes a limit, and the new
// shared client after that, which carries over what the caller configured on
// the old one; it does not check out a distinct connection. Configure the
// client before other goroutines use it or call the setters, because the
// setters copy it and the pool does not guard writes to it.
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

// Close closes the connection pool and the idle connections of the shared
// client and of the most recent transports the setters replaced. It marks the
// pool closed under its lock and closes the connections after releasing it,
// because closing a connection runs caller code that may call back into the
// pool. It is safe to call Close more than once.
func (p *Pool) Close() error {
	client, retired := p.markClosed()
	if client == nil {
		return nil
	}

	client.CloseIdleConnections()
	retired.CloseIdleConnections()

	return nil
}

// SetMaxConnections updates the maximum number of idle connections
// (MaxIdleConns) for subsequent requests. It is safe to call while requests
// run: when the limit changes, it installs a copy of the shared client with a
// Clone of its transport carrying the new limit, rather than writing the
// transport in use, as the package doc describes. Requests already in flight,
// and clients obtained from Get before the call, keep using the replaced
// transport. When the shared client's Transport is not an *http.Transport,
// it records the limit and changes nothing else.
func (p *Pool) SetMaxConnections(maxConns int) {
	p.retune(maxConns,
		func(config *Config) *int { return &config.MaxConnections },
		func(transport *http.Transport) *int { return &transport.MaxIdleConns })
}

// SetMaxConnectionsPerHost updates the maximum idle connections per host
// (MaxIdleConnsPerHost) for subsequent requests, in the same way as
// SetMaxConnections.
func (p *Pool) SetMaxConnectionsPerHost(maxConnsPerHost int) {
	p.retune(maxConnsPerHost,
		func(config *Config) *int { return &config.MaxConnectionsPerHost },
		func(transport *http.Transport) *int { return &transport.MaxIdleConnsPerHost })
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

// markClosed marks the pool closed and returns the shared client and the
// retired transports for Close to close, or a nil client when the pool was
// already closed.
func (p *Pool) markClosed() (*http.Client, swaptransport.Idle) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, nil
	}

	p.closed = true

	return p.client, p.retired.Snapshot()
}

// retune records value in the configuration field configField selects and,
// on an open pool whose shared client sends through an *http.Transport whose
// transportField differs from value, installs a copy of the client with a
// Clone of that transport carrying value. It closes the replaced transports'
// idle connections after releasing mu.
func (p *Pool) retune(value int, configField func(*Config) *int, transportField func(*http.Transport) *int) {
	p.swapClient(value, configField, transportField).CloseIdleConnections()
}

// swapClient does retune's work under mu and returns the transports whose idle
// connections retune closes once mu is released.
func (p *Pool) swapClient(
	value int,
	configField func(*Config) *int,
	transportField func(*http.Transport) *int,
) swaptransport.Idle {
	p.mu.Lock()
	defer p.mu.Unlock()

	*configField(&p.config) = value

	if p.closed {
		return nil
	}

	// A caller may have replaced the Transport through Get. Only an
	// *http.Transport can be cloned without losing its configuration; any
	// other RoundTripper, including a nil one that means
	// http.DefaultTransport, is left alone.
	current, ok := p.client.Transport.(*http.Transport)
	if !ok || current == nil || *transportField(current) == value {
		return nil
	}

	nextTransport := current.Clone()
	*transportField(nextTransport) = value

	// Copy the client so its Jar, CheckRedirect, Timeout, and any other field
	// a caller set carry over.
	nextClient := *p.client
	nextClient.Transport = nextTransport
	p.client = &nextClient

	return p.retired.Add(current)
}
