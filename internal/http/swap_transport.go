package http

import (
	"net/http"
	"sync"
	"sync/atomic"
)

// swappableTransport is the RoundTripper NewClient installs when no
// WrapTransport option is set. Each request loads the current *http.Transport
// atomically, so SetKeepAlive retunes the client by installing a tuned clone
// rather than by writing fields that net/http reads while requests run.
// A transport is never written after it is installed.
type swappableTransport struct {
	current atomic.Pointer[http.Transport]

	// mu serializes retune and guards retired.
	mu sync.Mutex

	// retired holds every transport a retune replaced. A request in flight
	// during the swap finishes on its old transport and can hand that
	// transport a connection afterwards, so CloseIdleConnections still
	// reaches the retired transports.
	retired []*http.Transport
}

func newSwappableTransport(t *http.Transport) *swappableTransport {
	s := &swappableTransport{}
	s.current.Store(t)

	return s
}

// RoundTrip sends req on the current transport.
func (s *swappableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.current.Load().RoundTrip(req) //nolint:wrapcheck // a transport must hand back the inner error untouched
}

// CloseIdleConnections closes the idle connections of the current transport
// and of every transport a retune replaced.
func (s *swappableTransport) CloseIdleConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.current.Load().CloseIdleConnections()

	for _, t := range s.retired {
		t.CloseIdleConnections()
	}
}

// load returns the transport that later requests use.
func (s *swappableTransport) load() *http.Transport {
	return s.current.Load()
}

// retune sets MaxIdleConns, and MaxIdleConnsPerHost when perHost is true, to
// connections. It installs a Clone of the current transport carrying the new
// limits, so later requests and logins use the clone, and it closes the idle
// connections of the transport it replaced. Requests already in flight finish
// on the replaced transport, which closes the connections they release rather
// than pooling them, and CloseIdleConnections still reaches any it does keep.
// A call that changes neither limit leaves the current transport in place.
func (s *swappableTransport) retune(connections int, perHost bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.current.Load()

	if old.MaxIdleConns == connections && (!perHost || old.MaxIdleConnsPerHost == connections) {
		return
	}

	next := old.Clone()
	next.MaxIdleConns = connections

	if perHost {
		next.MaxIdleConnsPerHost = connections
	}

	s.current.Store(next)
	s.retired = append(s.retired, old)

	old.CloseIdleConnections()
}
