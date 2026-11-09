// Package swaptransport replaces *http.Transport values while requests run.
// net/http reads a transport's exported fields, such as MaxIdleConns and
// MaxIdleConnsPerHost, without a lock the caller can take, so writing them on
// a transport in use is a data race. This package never writes a transport
// after it is in use: a Transport installed as an http.Client's RoundTripper
// switches later requests to a tuned Clone, and Retired cleans up the
// transports that were replaced.
//
// A replaced transport can still gain a pooled connection after it is
// replaced. A request that was waiting for a connection during the swap
// resets the transport's close-idle state, so the connection it releases is
// pooled rather than closed. That connection must not live forever, so every
// transport this package hands requests to has a finite IdleConnTimeout:
// the configured value, or DefaultIdleConnTimeout when it is zero or negative.
// A connection pooled on a replaced transport is therefore closed by
// net/http's idle timer at the latest IdleConnTimeout after its request ends,
// and sooner when CloseIdleConnections reaches that transport.
package swaptransport

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultIdleConnTimeout is the IdleConnTimeout given to a transport
// configured with none, matching net/http's DefaultTransport.
const DefaultIdleConnTimeout = 90 * time.Second

// maxRetired bounds how many replaced transports a Retired keeps, so a caller
// that retunes in a loop does not grow memory without limit.
const maxRetired = 8

// FiniteIdleTimeout returns timeout when it is positive and
// DefaultIdleConnTimeout otherwise.
func FiniteIdleTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}

	return DefaultIdleConnTimeout
}

// Retired holds the most recent replaced transports, at most maxRetired, so
// CloseIdleConnections can still reach connections released to them after the
// swap. A transport dropped from the list has its idle connections closed as
// it is dropped; anything it pools later is closed by its IdleConnTimeout.
// The zero value is ready to use.
type Retired struct {
	mu   sync.Mutex
	list []*http.Transport
}

// Add closes old's idle connections and keeps it, dropping the oldest kept
// transport when the list is full.
func (r *Retired) Add(old *http.Transport) {
	old.CloseIdleConnections()

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.list) == maxRetired {
		r.list[0].CloseIdleConnections()
		r.list = append(r.list[:0], r.list[1:]...)
	}

	r.list = append(r.list, old)
}

// CloseIdleConnections closes the idle connections of every kept transport.
func (r *Retired) CloseIdleConnections() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, t := range r.list {
		t.CloseIdleConnections()
	}
}

// Len reports how many replaced transports are kept, at most maxRetired.
func (r *Retired) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.list)
}

// Transport is an http.RoundTripper that sends each request on the current
// *http.Transport, loaded atomically, so Update can replace that transport
// while requests run.
type Transport struct {
	current atomic.Pointer[http.Transport]

	// mu serializes Update.
	mu sync.Mutex

	retired Retired
}

// New returns a Transport that sends on t. It sets t's IdleConnTimeout to
// DefaultIdleConnTimeout when t has none, so t must not be in use yet, and the
// caller must not write t's fields afterwards.
func New(t *http.Transport) *Transport {
	t.IdleConnTimeout = FiniteIdleTimeout(t.IdleConnTimeout)

	s := &Transport{}
	s.current.Store(t)

	return s
}

// RoundTrip sends req on the current transport.
func (s *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.current.Load().RoundTrip(req) //nolint:wrapcheck // a transport must hand back the inner error untouched
}

// CloseIdleConnections closes the idle connections of the current transport
// and of the retired transports Update keeps.
func (s *Transport) CloseIdleConnections() {
	s.current.Load().CloseIdleConnections()
	s.retired.CloseIdleConnections()
}

// Load returns the transport that later requests use. The caller must not
// write its fields.
func (s *Transport) Load() *http.Transport {
	return s.current.Load()
}

// Update passes apply a Clone of the current transport. When apply changes
// the clone and returns true, Update installs the clone for later requests,
// with a finite IdleConnTimeout as the package doc describes, and retires the
// transport it replaced. Requests already in flight finish on the replaced
// transport. When apply returns false, the current transport stays in place
// and the clone is discarded.
func (s *Transport) Update(apply func(next *http.Transport) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.current.Load()
	next := old.Clone()

	if !apply(next) {
		return
	}

	next.IdleConnTimeout = FiniteIdleTimeout(next.IdleConnTimeout)

	s.current.Store(next)
	s.retired.Add(old)
}

// RetiredCount reports how many replaced transports the Transport keeps,
// which is at most maxRetired.
func (s *Transport) RetiredCount() int {
	return s.retired.Len()
}
