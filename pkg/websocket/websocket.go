// Package websocket provides WebSocket support for real-time PVE events.
package websocket

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/safetext"
)

var (
	errHostRequired             = errors.New("host is required")
	errAlreadyConnected         = errors.New("already connected")
	errFailedToConnect          = errors.New("failed to connect after %d attempts")
	errNotConnected             = errors.New("not connected")
	errPanicInReadLoop          = errors.New("panic in read loop")
	errDisconnectedReconnecting = errors.New("disconnected, attempting to reconnect")
	errReconnected              = errors.New("reconnected after %d attempts")
	errFailedToReconnect        = errors.New("failed to reconnect after %d attempts")
	errTooManyDeadlineFailures  = errors.New("too many consecutive read-deadline failures")
)

const (
	// defaultMaxConcurrentHandlers bounds how many event handler goroutines
	// may run at once across all event types when Config.MaxConcurrentHandlers
	// is left at its zero value.
	defaultMaxConcurrentHandlers = 100

	// maxConsecutiveDeadlineFailures is how many SetReadDeadline failures in a
	// row readLoop tolerates before treating the connection as dead.
	maxConsecutiveDeadlineFailures = 3

	// maxCloseErrorBytes is the most bytes of a read error's text that an
	// error returned to the caller keeps. The text of a close error holds the
	// reason the server chose, so it is escaped and cut at this length.
	maxCloseErrorBytes = 256

	// readDeadlineFailureBackoff is the pause between retries after a
	// SetReadDeadline failure, so a broken conn does not busy-spin the CPU.
	readDeadlineFailureBackoff = 50 * time.Millisecond
)

// Client represents a WebSocket client for PVE events.
type Client struct {
	conn          *websocket.Conn
	rawConn       net.Conn // the network connection under conn, which carries the read deadlines
	config        *Config
	url           *url.URL
	headers       http.Header
	dialer        *dialer
	handlers      map[string][]handlerEntry
	nextHandlerID uint64
	handlerSem    chan struct{} // bounds concurrently-running handler goroutines
	mu            sync.RWMutex
	closed        bool
	closeChan     chan struct{}
	errorChan     chan error
	reconnect     bool
	pingTicker    *time.Ticker
}

// handlerEntry pairs a registered EventHandler with the ID used to
// deregister it (see removeHandler / Subscription.Cancel).
type handlerEntry struct {
	id      uint64
	handler EventHandler
}

// Config represents WebSocket client configuration.
type Config struct {
	// Host is the PVE host.
	Host string

	// Port is the WebSocket port (default: 8006).
	Port int

	// Path is the WebSocket endpoint path.
	Path string

	// Secure indicates whether to use WSS (default: true).
	Secure bool

	// TLSConfig is the TLS configuration.
	TLSConfig *tls.Config

	// HandshakeTimeout is the handshake timeout.
	HandshakeTimeout time.Duration

	// ReadTimeout is the read timeout.
	ReadTimeout time.Duration

	// WriteTimeout is the write timeout.
	WriteTimeout time.Duration

	// PingInterval is the interval for ping messages.
	PingInterval time.Duration

	// ReconnectInterval is the interval between reconnection attempts.
	ReconnectInterval time.Duration

	// MaxReconnectAttempts is the maximum number of reconnection attempts.
	MaxReconnectAttempts int

	// BufferSize has no effect. The WebSocket library sizes its own buffers,
	// and the field is kept so existing configurations still compile.
	BufferSize int

	// MaxConcurrentHandlers bounds how many event handler goroutines may run
	// concurrently across all event types (default: 100 if <= 0). Handler
	// dispatch never blocks the read loop: once the limit is reached,
	// additional handler invocations queue behind it rather than running
	// unbounded in parallel.
	MaxConcurrentHandlers int
}

// DefaultConfig returns the default WebSocket configuration.
func DefaultConfig() *Config {
	return &Config{
		Host:                  "",
		Port:                  constants.ProxmoxDefaultPort,
		Path:                  "/api2/json/nodes/localhost/console",
		Secure:                true,
		TLSConfig:             nil,
		HandshakeTimeout:      constants.WebSocketHandshakeTimeout(),
		ReadTimeout:           constants.DefaultClientTimeout(),
		WriteTimeout:          constants.ShortTimeout(),
		PingInterval:          constants.DefaultClientTimeout(),
		ReconnectInterval:     constants.WebSocketReconnectInterval(),
		MaxReconnectAttempts:  constants.WebSocketMaxReconnectAttempts,
		BufferSize:            constants.LargeBufferSize,
		MaxConcurrentHandlers: defaultMaxConcurrentHandlers,
	}
}

// Event represents a PVE event.
type Event struct {
	Type      string                 `json:"type"`
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Node      string                 `json:"node,omitempty"`
	Resource  string                 `json:"resource,omitempty"`
	Action    string                 `json:"action,omitempty"`
	User      string                 `json:"user,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// EventHandler handles WebSocket events.
type EventHandler func(event *Event)

// New creates a new WebSocket client.
func New(config *Config) (*Client, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if config.Host == "" {
		return nil, errHostRequired
	}

	maxConcurrentHandlers := config.MaxConcurrentHandlers
	if maxConcurrentHandlers <= 0 {
		maxConcurrentHandlers = defaultMaxConcurrentHandlers
	}

	return &Client{
		conn:          nil,
		rawConn:       nil,
		config:        config,
		url:           buildWSURL(config),
		headers:       make(http.Header),
		dialer:        buildDialer(config),
		handlers:      make(map[string][]handlerEntry),
		nextHandlerID: 0,
		handlerSem:    make(chan struct{}, maxConcurrentHandlers),
		mu:            sync.RWMutex{},
		closed:        false,
		closeChan:     make(chan struct{}),
		errorChan:     make(chan error, constants.ErrorChannelSize),
		reconnect:     true,
		pingTicker:    nil,
	}, nil
}

// buildWSURL constructs the target WebSocket URL from config.
func buildWSURL(config *Config) *url.URL {
	scheme := "wss"
	if !config.Secure {
		scheme = "ws"
	}

	return &url.URL{
		Scheme:      scheme,
		Opaque:      "",
		User:        nil,
		Host:        fmt.Sprintf("%s:%d", config.Host, config.Port),
		Path:        config.Path,
		RawPath:     "",
		OmitHost:    false,
		ForceQuery:  false,
		RawQuery:    "",
		Fragment:    "",
		RawFragment: "",
	}
}

// buildDialer constructs the dialer used for Connect.
func buildDialer(config *Config) *dialer {
	return &dialer{
		TLSClientConfig:  config.TLSConfig,
		HandshakeTimeout: config.HandshakeTimeout,
		NetDialContext:   nil,
	}
}

// SetHeaders sets custom headers for the WebSocket connection.
func (c *Client) SetHeaders(headers http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.headers = headers
}

// SetAuth sets authentication headers.
func (c *Client) SetAuth(ticket, csrfToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ticket != "" {
		c.headers.Set("Cookie", "PVEAuthCookie="+ticket)
	}

	if csrfToken != "" {
		c.headers.Set("Csrfpreventiontoken", csrfToken)
	}
}

// Connect establishes the WebSocket connection.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return errAlreadyConnected
	}

	conn, raw, err := c.dial(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Configure the read deadline on the freshly dialed conn before publishing
	// it as c.conn. If the call fails, close the conn and return without ever
	// setting c.conn, so the client stays reconnectable instead of getting
	// stuck returning errAlreadyConnected forever with a leaked connection.
	// Writes are bounded by a context per write instead of a deadline.
	if c.config.ReadTimeout > 0 {
		err := raw.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
		if err != nil {
			_ = conn.CloseNow()

			return fmt.Errorf("failed to set read deadline: %w", err)
		}
	}

	c.conn = conn
	c.rawConn = raw
	c.closed = false

	// Start ping ticker
	if c.config.PingInterval > 0 {
		c.pingTicker = time.NewTicker(c.config.PingInterval)
		go c.pingLoop(context.WithoutCancel(ctx))
	}

	// Start read loop
	go c.readLoop(ctx)

	return nil
}

// ConnectWithRetry connects with automatic retry on failure. It retries every
// error Connect returns, and does not look for the permanent marker the HTTP
// client honors. Config has no hook through which a caller could supply such an
// error, because the dialer is built from Config alone, and the dialer never
// follows a redirect, so no ticket or token can reach a redirect target here.
func (c *Client) ConnectWithRetry(ctx context.Context) error {
	attempts := 0

	maxAttempts := c.config.MaxReconnectAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for attempts < maxAttempts {
		if attempts > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during reconnect: %w", ctx.Err())
			case <-time.After(c.config.ReconnectInterval):
			}
		}

		err := c.Connect(ctx)
		if err == nil {
			return nil
		}

		attempts++
		if attempts < maxAttempts {
			c.sendError(fmt.Errorf("connection attempt %d failed: %w", attempts, err))
		}
	}

	return fmt.Errorf("%w: %d", errFailedToConnect, maxAttempts)
}

// Disconnect closes the WebSocket connection.
func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	c.reconnect = false
	close(c.closeChan)

	if c.pingTicker != nil {
		c.pingTicker.Stop()
		c.pingTicker = nil
	}

	if c.conn != nil {
		conn := c.conn
		c.conn = nil
		c.rawConn = nil

		// Release mu while the close handshake runs, so the read loop and
		// other callers are not held up behind it.
		c.mu.Unlock()

		// Close sends a normal closure, waits a bounded time for the server's
		// close frame, and always closes the network connection. A failed
		// handshake means the server has already gone, so it is not reported.
		_ = conn.Close(websocket.StatusNormalClosure, "")

		// Re-acquire mu so the deferred Unlock does not double-unlock.
		c.mu.Lock()
	}

	return nil
}

// On registers an event handler for a specific event type. Handlers run in
// their own goroutines when a matching event arrives (see handleMessage),
// bounded by Config.MaxConcurrentHandlers concurrently-running handlers
// across the client.
func (c *Client) On(eventType string, handler EventHandler) {
	c.registerHandler(eventType, handler)
}

// OnAll registers a handler for all events.
func (c *Client) OnAll(handler EventHandler) {
	c.On("*", handler)
}

// Off removes event handlers for a specific event type.
func (c *Client) Off(eventType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.handlers, eventType)
}

// Send sends a message to the server.
func (c *Client) Send(data interface{}) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil {
		return errNotConnected
	}

	// encoding/json's Encoder ends the message with a newline, which servers
	// have always received from Send.
	var payload bytes.Buffer

	err := json.NewEncoder(&payload).Encode(data)
	if err != nil {
		return fmt.Errorf("failed to send JSON message: %w", err)
	}

	ctx, cancel := c.writeContext(context.Background())
	defer cancel()

	err = conn.Write(ctx, websocket.MessageText, payload.Bytes())
	if err != nil {
		return fmt.Errorf("failed to send JSON message: %w", err)
	}

	return nil
}

// SendText sends a text message to the server.
func (c *Client) SendText(text string) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil {
		return errNotConnected
	}

	ctx, cancel := c.writeContext(context.Background())
	defer cancel()

	err := conn.Write(ctx, websocket.MessageText, []byte(text))
	if err != nil {
		return fmt.Errorf("failed to send text message: %w", err)
	}

	return nil
}

// Errors returns the error channel.
func (c *Client) Errors() <-chan error {
	return c.errorChan
}

// IsConnected returns whether the client is connected.
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.conn != nil && !c.closed
}

// Subscription manages event subscriptions.
type Subscription struct {
	client    *Client
	eventType string
	handlerID uint64
	cancel    context.CancelFunc
}

// NewSubscription creates a new subscription.
func (c *Client) NewSubscription(eventType string, handler EventHandler) *Subscription {
	ctx, cancel := context.WithCancel(context.Background())

	sub := &Subscription{
		client:    c,
		eventType: eventType,
		cancel:    cancel,
	}

	// Wrapper handler that checks context
	wrappedHandler := func(event *Event) {
		select {
		case <-ctx.Done():
			return
		default:
			handler(event)
		}
	}

	sub.handlerID = c.registerHandler(eventType, wrappedHandler)

	return sub
}

// Cancel cancels the subscription: it stops the wrapped handler from
// forwarding further events (via context) and deregisters it from the
// client's handler map, so long-lived clients don't accumulate dead
// closures. Safe to call more than once.
func (s *Subscription) Cancel() {
	s.cancel()
	s.client.removeHandler(s.eventType, s.handlerID)
}

// Stream provides a channel-based interface for events.
type Stream struct {
	client    *Client
	eventChan chan *Event
	stopChan  chan struct{}
	stopOnce  sync.Once
}

// NewStream creates a new event stream.
func (c *Client) NewStream(bufferSize int) *Stream {
	if bufferSize <= 0 {
		bufferSize = 100
	}

	stream := &Stream{
		client:    c,
		eventChan: make(chan *Event, bufferSize),
		stopChan:  make(chan struct{}),
	}

	// Register handler that sends to channel. Handlers are dispatched in their
	// own goroutines, so check stopChan first to avoid delivering events after
	// Stop. eventChan is deliberately never closed: a late handler must be able
	// to hit the drop path rather than panic on a send to a closed channel.
	handler := func(event *Event) {
		select {
		case <-stream.stopChan:
			return
		default:
		}

		select {
		case stream.eventChan <- event:
		case <-stream.stopChan:
		default:
			// Channel is full, drop the event
		}
	}

	c.OnAll(handler)

	return stream
}

// dial opens the WebSocket connection, bounded by Config.HandshakeTimeout,
// and returns it with the network connection under it. Each pong pushes the
// read deadline out by Config.ReadTimeout.
func (c *Client) dial(ctx context.Context) (*websocket.Conn, net.Conn, error) {
	if c.dialer.HandshakeTimeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, c.dialer.HandshakeTimeout)
		defer cancel()
	}

	spec := &dialSpec{
		target:    c.url,
		header:    c.headers,
		tlsConfig: c.dialer.TLSClientConfig,
		netDial:   c.dialer.NetDialContext,
		onPong:    nil,
	}

	if c.config.ReadTimeout > 0 {
		spec.onPong = func(raw net.Conn) {
			// A failure here surfaces at the next read, which sets the
			// deadline again and counts the failure.
			_ = raw.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
		}
	}

	return dialWebSocket(ctx, spec)
}

// writeContext returns the context one write runs under, which ends after
// Config.WriteTimeout when that is set. A write that runs out of time closes
// the connection, and the read loop then reconnects.
func (c *Client) writeContext(parent context.Context) (context.Context, context.CancelFunc) {
	if c.config.WriteTimeout > 0 {
		return context.WithTimeout(parent, c.config.WriteTimeout)
	}

	return context.WithCancel(parent)
}

// registerHandler is On's implementation, returning the ID assigned to the
// handler so callers within the package (e.g. NewSubscription) can later
// deregister that specific handler via removeHandler.
func (c *Client) registerHandler(eventType string, handler EventHandler) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextHandlerID++
	id := c.nextHandlerID

	c.handlers[eventType] = append(c.handlers[eventType], handlerEntry{id: id, handler: handler})

	return id
}

// removeHandler deregisters the handler previously returned by registerHandler.
// It is a no-op if the handler is already gone (e.g. Off was called, or
// Cancel was called twice).
func (c *Client) removeHandler(eventType string, handlerID uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries := c.handlers[eventType]
	for i, entry := range entries {
		if entry.id == handlerID {
			c.handlers[eventType] = append(entries[:i], entries[i+1:]...)

			return
		}
	}
}

func (c *Client) readLoop(ctx context.Context) {
	// Capture conn once; Disconnect sets c.conn = nil but we hold our own
	// reference for the lifetime of this goroutine.
	c.mu.RLock()
	conn := c.conn
	raw := c.rawConn
	c.mu.RUnlock()

	defer func() {
		if r := recover(); r != nil {
			c.sendError(fmt.Errorf("%w: %v", errPanicInReadLoop, r))
		}

		// Disconnect closes the connection itself. Any other way out of the
		// loop leaves a connection nothing reads from, so close it here.
		select {
		case <-c.closeChan:
		default:
			_ = conn.CloseNow()
		}

		c.handleDisconnect(ctx)
	}()

	// A read is not bound to ctx: the context Connect received ends the
	// handshake only, and Disconnect is what stops the loop.
	readCtx := context.WithoutCancel(ctx)
	deadlineFailures := 0

	for {
		select {
		case <-c.closeChan:
			return
		default:
			switch c.refreshReadDeadline(raw, &deadlineFailures) {
			case deadlineOutcomeFatal:
				return
			case deadlineOutcomeRetry:
				continue
			case deadlineOutcomeOK:
			}

			messageType, data, err := conn.Read(readCtx)
			if err != nil {
				err = readError(err)
				if isUnexpectedClose(err) {
					c.sendError(fmt.Errorf("read error: %w", safetext.CappedError(err, maxCloseErrorBytes)))
				}

				return
			}

			if messageType == websocket.MessageText || messageType == websocket.MessageBinary {
				c.handleMessage(data)
			}
		}
	}
}

// isUnexpectedClose reports whether err ended the connection with a close
// status other than a normal closure or the server going away. A peer that
// vanished without a close frame counts, because readError gives it
// StatusAbnormalClosure. A read timeout and a local close do not.
func isUnexpectedClose(err error) bool {
	status := websocket.CloseStatus(err)

	return status != -1 && status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway
}

// deadlineOutcome is the result of one refreshReadDeadline call, telling
// readLoop whether to proceed to ReadMessage, retry the loop iteration, or
// give up on the connection entirely.
type deadlineOutcome int

const (
	deadlineOutcomeOK deadlineOutcome = iota
	deadlineOutcomeRetry
	deadlineOutcomeFatal
)

// refreshReadDeadline sets conn's read deadline for the next readLoop
// iteration. On failure it backs off (respecting closeChan) and reports
// deadlineOutcomeRetry, unless *failures has reached
// maxConsecutiveDeadlineFailures, in which case the conn is treated as dead
// (same as a fatal ReadMessage error) and deadlineOutcomeFatal is reported.
func (c *Client) refreshReadDeadline(conn net.Conn, failures *int) deadlineOutcome {
	if c.config.ReadTimeout <= 0 {
		return deadlineOutcomeOK
	}

	err := conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
	if err == nil {
		*failures = 0

		return deadlineOutcomeOK
	}

	*failures++

	c.sendError(fmt.Errorf("failed to set read deadline: %w", err))

	// A conn with a broken SetReadDeadline is almost always dead (e.g.
	// already closed). Treat repeated failures as fatal, same as a fatal
	// ReadMessage error, instead of retrying forever.
	if *failures >= maxConsecutiveDeadlineFailures {
		c.sendError(fmt.Errorf("%w: %d consecutive failures", errTooManyDeadlineFailures, *failures))

		return deadlineOutcomeFatal
	}

	// Back off instead of busy-spinning the CPU on a dead conn, while still
	// reacting promptly to Disconnect.
	select {
	case <-c.closeChan:
		return deadlineOutcomeFatal
	case <-time.After(readDeadlineFailureBackoff):
	}

	return deadlineOutcomeRetry
}

func (c *Client) pingLoop(ctx context.Context) {
	// Capture ticker at start; Disconnect stops and nils c.pingTicker but we
	// hold our own reference so the channel read is always safe.
	c.mu.RLock()
	ticker := c.pingTicker
	c.mu.RUnlock()

	defer ticker.Stop()

	for {
		select {
		case <-c.closeChan:
			return
		case <-ticker.C:
			c.mu.RLock()
			conn := c.conn
			c.mu.RUnlock()

			if conn == nil {
				return
			}

			err := c.sendPing(ctx, conn)
			if errors.Is(err, net.ErrClosed) {
				// The connection is gone, and the read loop reports it.
				return
			}

			if err != nil {
				c.sendError(err)

				return
			}
		}
	}
}

func (c *Client) handleMessage(data []byte) {
	var event Event

	err := json.Unmarshal(data, &event)
	if err != nil {
		// Try to handle as raw message
		event = Event{
			Type:      "raw",
			ID:        "",
			Timestamp: time.Now(),
			Node:      "",
			Resource:  "",
			Action:    "",
			User:      "",
			Status:    "",
			Data: map[string]interface{}{
				"message": string(data),
			},
		}
	}

	// Set timestamp if not provided
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	// Call specific handlers
	if entries, ok := c.handlers[event.Type]; ok {
		for _, entry := range entries {
			c.dispatch(entry.handler, &event)
		}
	}

	// Call wildcard handlers
	if entries, ok := c.handlers["*"]; ok {
		for _, entry := range entries {
			c.dispatch(entry.handler, &event)
		}
	}
}

// dispatch runs handler on event in its own goroutine, bounded by
// handlerSem to at most cap(handlerSem) (Config.MaxConcurrentHandlers)
// concurrently-running handlers across the whole client. The spawning
// goroutine itself never blocks handleMessage/readLoop: it queues on the
// semaphore, so a burst of messages cannot deadlock, it only bounds how
// much user handler code runs in parallel.
func (c *Client) dispatch(handler EventHandler, event *Event) {
	go func() {
		c.handlerSem <- struct{}{}
		defer func() { <-c.handlerSem }()

		handler(event)
	}()
}

func (c *Client) handleDisconnect(ctx context.Context) {
	c.mu.Lock()
	wasConnected := c.conn != nil
	c.conn = nil
	shouldReconnect := c.reconnect && !c.closed
	c.mu.Unlock()

	if wasConnected && shouldReconnect {
		c.sendError(errDisconnectedReconnecting)

		go c.reconnectLoop(ctx)
	}
}

func (c *Client) reconnectLoop(ctx context.Context) {
	attempts := 0
	maxAttempts := c.config.MaxReconnectAttempts

	for attempts < maxAttempts {
		select {
		case <-c.closeChan:
			return
		case <-time.After(c.config.ReconnectInterval):
			attempts++

			connectCtx, cancel := context.WithTimeout(ctx, c.config.HandshakeTimeout)
			err := c.Connect(connectCtx)

			cancel()

			if err == nil {
				c.sendError(fmt.Errorf("%w: %d", errReconnected, attempts))

				return
			}

			if attempts < maxAttempts {
				c.sendError(fmt.Errorf("reconnection attempt %d failed: %w", attempts, err))
			}
		}
	}

	c.sendError(fmt.Errorf("%w: %d", errFailedToReconnect, maxAttempts))
}

func (c *Client) sendError(err error) {
	select {
	case c.errorChan <- err:
	default:
		// Channel is full, drop the error
	}
}

// sendPing sends a ping frame and waits for its pong, both bounded by
// Config.WriteTimeout. A pong that does not arrive in that time is not an
// error: the pong, when it comes, still pushes the read deadline out, and a
// server that never answers is caught by Config.ReadTimeout.
func (c *Client) sendPing(parent context.Context, conn *websocket.Conn) error {
	ctx, cancel := c.writeContext(parent)
	defer cancel()

	err := conn.Ping(ctx)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}

	return fmt.Errorf("ping error: %w", err)
}

// Events returns the event channel.
func (s *Stream) Events() <-chan *Event {
	return s.eventChan
}

// Stop stops the stream. It is safe to call more than once and from multiple
// goroutines; only stopChan is closed. eventChan is left open because handler
// goroutines registered with the client may still attempt a send.
func (s *Stream) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopChan)
	})
}
