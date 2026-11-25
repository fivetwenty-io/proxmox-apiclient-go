package websocket

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
)

// Message types that Conn.ReadMessage returns and Conn.WriteMessage accepts.
// They are the WebSocket opcodes for text and binary data frames, which are
// the values callers have always seen from ReadMessage.
const (
	TextMessage   = int(websocket.MessageText)
	BinaryMessage = int(websocket.MessageBinary)
)

var (
	// ErrBadHandshake is returned, wrapped, when the server answers a
	// WebSocket dial with anything other than an upgrade to WebSocket,
	// including a redirect, which every dial refuses.
	ErrBadHandshake = errors.New("websocket: bad handshake")

	// ErrUnsupportedMessageType is returned, wrapped, when Conn.WriteMessage is
	// given a message type other than TextMessage or BinaryMessage.
	ErrUnsupportedMessageType = errors.New("unsupported websocket message type")

	errDuplicateHeader = errors.New("websocket: duplicate header not allowed")
	errNoNetConn       = errors.New("websocket dial returned no network connection")
)

// reservedHeaders are the handshake headers the dial sets itself. A caller
// that sets one of them gets an error instead of a silently replaced value.
//
//nolint:gochecknoglobals // a fixed lookup table
var reservedHeaders = []string{
	"Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions",
}

// netDialFunc opens the network connection a WebSocket dial runs over.
type netDialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// dialer holds what Client.Connect needs to open a connection. NetDialContext,
// when set, replaces the plain TCP dial.
type dialer struct {
	TLSClientConfig  *tls.Config
	HandshakeTimeout time.Duration
	NetDialContext   netDialFunc
}

// capturedConn records the network connection a dial opened, so the caller
// can set read deadlines on it after the handshake.
type capturedConn struct {
	mu   sync.Mutex
	conn net.Conn
}

func (cc *capturedConn) set(conn net.Conn) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	cc.conn = conn
}

func (cc *capturedConn) get() net.Conn {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	return cc.conn
}

// dialSpec describes one WebSocket dial.
type dialSpec struct {
	target    *url.URL
	header    http.Header
	tlsConfig *tls.Config
	netDial   netDialFunc

	// onPong, when set, runs with the network connection each time a pong
	// arrives.
	onPong func(raw net.Conn)
}

// dialWebSocket opens the WebSocket connection spec describes and returns it
// with the network connection under it.
//
// The handshake runs over its own HTTP/1.1 transport, with no proxy and no
// connection reuse, and over the TLS configuration in spec, which net/http
// clones and gives the host name as ServerName when it has none. The client
// refuses every redirect, because the request carries the ticket cookie and
// the CSRF token. No subprotocol or compression is offered unless the caller
// sets Sec-WebSocket-Protocol, no User-Agent or Accept-Encoding header is
// added, and the read limit is lifted, so a message of any size is read whole.
func dialWebSocket(ctx context.Context, spec *dialSpec) (*websocket.Conn, net.Conn, error) {
	header, host, subprotocols, err := handshakeHeaders(spec.header)
	if err != nil {
		return nil, nil, err
	}

	baseDial := spec.netDial
	if baseDial == nil {
		baseDial = (&net.Dialer{}).DialContext
	}

	captured := &capturedConn{}

	transport := newHandshakeTransport(spec.tlsConfig, baseDial, captured)

	opts := &websocket.DialOptions{
		// Refuse every redirect. The guard refuses it before net/http reads
		// the Location header, and the policy backs it up.
		HTTPClient:      &http.Client{Transport: redirect.Guard(transport, nil), CheckRedirect: redirect.Refuse},
		HTTPHeader:      header,
		Host:            host,
		Subprotocols:    subprotocols,
		CompressionMode: websocket.CompressionDisabled,
	}

	if spec.onPong != nil {
		opts.OnPongReceived = func(context.Context, []byte) {
			if raw := captured.get(); raw != nil {
				spec.onPong(raw)
			}
		}
	}

	conn, resp, err := websocket.Dial(ctx, spec.target.String(), opts)
	if err != nil {
		return nil, nil, dialError(err, resp)
	}

	raw := captured.get()
	if raw == nil {
		_ = conn.CloseNow()

		return nil, nil, errNoNetConn
	}

	conn.SetReadLimit(-1)

	return conn, raw, nil
}

// newHandshakeTransport returns the HTTP/1.1 transport a handshake runs over,
// which records the network connection it dials in captured.
func newHandshakeTransport(tlsConfig *tls.Config, baseDial netDialFunc, captured *capturedConn) *http.Transport {
	var protocols http.Protocols
	protocols.SetHTTP1(true)

	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, dialErr := baseDial(ctx, network, addr)
			if dialErr != nil {
				return nil, dialErr
			}

			captured.set(conn)

			return conn, nil
		},
		TLSClientConfig:    tlsConfig,
		DisableKeepAlives:  true,
		DisableCompression: true, // net/http would otherwise add Accept-Encoding: gzip
		Protocols:          &protocols,
	}
}

// handshakeHeaders splits the caller's headers into the headers sent as
// they are, the Host to send, and the subprotocols to offer. It refuses a
// header the dial sets itself.
func handshakeHeaders(callerHeader http.Header) (http.Header, string, []string, error) {
	out := make(http.Header, len(callerHeader))

	var (
		host         string
		subprotocols []string
	)

	for name, values := range callerHeader {
		canonical := http.CanonicalHeaderKey(name)

		switch {
		case canonical == "Host":
			if len(values) > 0 {
				host = values[0]
			}
		case canonical == "Sec-Websocket-Protocol":
			for _, value := range values {
				for _, protocol := range strings.Split(value, ",") {
					if protocol = strings.TrimSpace(protocol); protocol != "" {
						subprotocols = append(subprotocols, protocol)
					}
				}
			}
		case isReservedHeader(canonical):
			return nil, "", nil, fmt.Errorf("%w: %s", errDuplicateHeader, canonical)
		default:
			out[name] = append([]string(nil), values...)
		}
	}

	// net/http sends its own User-Agent unless the header is present, and an
	// empty value keeps it off the wire, as the handshake has always been.
	if _, ok := out["User-Agent"]; !ok {
		out["User-Agent"] = []string{""}
	}

	return out, host, subprotocols, nil
}

func isReservedHeader(canonical string) bool {
	for _, reserved := range reservedHeaders {
		if canonical == reserved {
			return true
		}
	}

	return false
}

// dialError cleans an error from websocket.Dial.
//
// When the server answered but did not upgrade, the error names only the
// status code. The library's own text quotes response headers the server
// chose. A refused redirect also counts as a bad handshake. Every other error,
// such as a refused connection or a TLS failure, keeps its chain, with the
// ticket in the URL and any text the server chose cleaned by redirect.Scrub.
func dialError(err error, resp *http.Response) error {
	if resp != nil {
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}

		return fmt.Errorf("%w: status %d", ErrBadHandshake, resp.StatusCode)
	}

	scrubbed := redirect.Scrub(err)

	if errors.Is(err, redirect.ErrRefused) {
		return fmt.Errorf("%w: %w", ErrBadHandshake, scrubbed)
	}

	return scrubbed //nolint:wrapcheck // Scrub returns a cleaned copy of the dial error
}

// readError gives an error from reading a message the close status a peer
// that vanished implies. A connection that ends without a close frame reads
// as io.EOF or io.ErrUnexpectedEOF, and it gets StatusAbnormalClosure, as the
// WebSocket protocol reserves that code for it. Every other error is returned
// as it is.
func readError(err error) error {
	if websocket.CloseStatus(err) != -1 {
		return err
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %w", websocket.CloseError{Code: websocket.StatusAbnormalClosure, Reason: "unexpected EOF"}, err)
	}

	return err
}
