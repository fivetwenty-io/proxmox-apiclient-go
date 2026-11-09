// Package swaptransport holds an *http.Transport that can be replaced while
// requests run. net/http reads a transport's exported fields, such as
// MaxIdleConns and MaxIdleConnsPerHost, without a lock the caller can take, so
// writing them on a transport in use is a data race. A Transport here is
// installed as an http.Client's RoundTripper and never writes the
// *http.Transport it holds: Update installs a tuned Clone instead.
package swaptransport

import (
	"net/http"
	"sync"
	"sync/atomic"
)

// maxRetired bounds how many replaced transports a Transport keeps, so a
// caller that retunes in a loop does not grow memory without limit.
const maxRetired = 8

// Transport is an http.RoundTripper that sends each request on the current
// *http.Transport, loaded atomically, so Update can replace that transport
// while requests run.
type Transport struct {
	current atomic.Pointer[http.Transport]

	// mu serializes Update and guards retired.
	mu sync.Mutex

	// retired holds the most recent transports an Update replaced, at most
	// maxRetired. A request in flight during the swap finishes on its old
	// transport and can hand that transport a connection afterwards, so
	// CloseIdleConnections still reaches the retired transports.
	retired []*http.Transport
}

// New returns a Transport that sends on t. The caller must not write t's
// fields after it passes t here.
func New(t *http.Transport) *Transport {
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
	s.mu.Lock()
	defer s.mu.Unlock()

	s.current.Load().CloseIdleConnections()

	for _, t := range s.retired {
		t.CloseIdleConnections()
	}
}

// Load returns the transport that later requests use. The caller must not
// write its fields.
func (s *Transport) Load() *http.Transport {
	return s.current.Load()
}

// Update passes apply a Clone of the current transport. When apply changes
// the clone and returns true, Update installs the clone for later requests
// and closes the idle connections of the transport it replaced. Requests
// already in flight finish on the replaced transport, which closes the
// connections they release rather than pooling them. The rare connection it
// does keep, from a request that was still waiting for a connection during the
// swap, is closed by CloseIdleConnections while that transport is among the
// last maxRetired replaced, and otherwise by the transport's IdleConnTimeout.
// A transport dropped from that list has its idle connections closed once
// more as it is dropped. When apply returns false, the current transport
// stays in place and the clone is discarded.
func (s *Transport) Update(apply func(next *http.Transport) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.current.Load()
	next := old.Clone()

	if !apply(next) {
		return
	}

	s.current.Store(next)
	old.CloseIdleConnections()

	if len(s.retired) == maxRetired {
		s.retired[0].CloseIdleConnections()
		s.retired = append(s.retired[:0], s.retired[1:]...)
	}

	s.retired = append(s.retired, old)
}

// RetiredCount reports how many replaced transports the Transport keeps,
// which is at most maxRetired.
func (s *Transport) RetiredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.retired)
}
