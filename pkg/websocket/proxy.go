// Package websocket provides WebSocket support for real-time PVE events and
// console/terminal proxy sessions.
package websocket

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
)

// Errors returned by proxy operations.
var (
	ErrNodeRequired    = errors.New("node is required")
	ErrVMIDInvalid     = errors.New("vmid must be >= 100")
	ErrPortOutOfRange  = errors.New("vnc port must be in range 5900-5999")
	ErrNilSession      = errors.New("session is nil")
	ErrSessionNoTicket = errors.New("session has no ticket")
	ErrSessionNoPort   = errors.New("session has no port")
	ErrWriteAfterClose = errors.New("write on closed connection")
	ErrSocketRequired  = errors.New("socket path is required for tunnel websocket")
	ErrTicketRequired  = errors.New("ticket is required for tunnel websocket")
	ErrHTTPError       = errors.New("http error from PVE API")
)

// HTTPDoer is a minimal interface for posting to the PVE REST API.
// Caller supplies an implementation (e.g. wrapping internal/http.Client).
type HTTPDoer interface {
	// PostJSON sends a POST request and decodes the JSON response body into dst.
	PostJSON(ctx context.Context, path string, params map[string]string, dst interface{}) error
}

// ProxyConfig holds connection parameters for building WebSocket URLs.
type ProxyConfig struct {
	// Host is the PVE host (required).
	Host string

	// Port is the HTTPS port (default: 8006).
	Port int

	// Scheme overrides the HTTP scheme ("http" or "https"). Default: "https".
	// Set to "http" when using plain-HTTP test servers.
	Scheme string

	// Insecure disables TLS certificate verification.
	Insecure bool

	// TLSConfig overrides TLS settings (optional; Insecure flag is applied
	// when nil and Insecure is true).
	TLSConfig *tls.Config

	// HTTPClient performs POST calls to obtain proxy tickets.
	HTTPClient HTTPDoer

	// Ticket is the PVEAuthCookie value for authenticating WebSocket upgrades.
	Ticket string

	// CSRFToken is the CSRF prevention token.
	CSRFToken string
}

// VNCSession holds the result of a vncproxy or vncshell POST call.
type VNCSession struct {
	// Port is the VNC port returned by the server (5900-5999).
	Port int `json:"port"`

	// Ticket is the VNC ticket for authenticating the WebSocket connection.
	Ticket string `json:"ticket"`

	// UPID is the task ID.
	UPID string `json:"upid"`

	// User is the authenticated user.
	User string `json:"user"`

	// Cert is the server certificate (optional, for VMID sessions).
	Cert string `json:"cert,omitempty"`
}

// TermSession holds the result of a termproxy POST call.
type TermSession struct {
	// Port is the terminal port.
	Port int `json:"port"`

	// Ticket is the terminal ticket.
	Ticket string `json:"ticket"`

	// UPID is the task ID.
	UPID string `json:"upid"`

	// User is the authenticated user.
	User string `json:"user"`
}

// SpiceSession holds the result of a spiceproxy or spiceshell POST call.
type SpiceSession struct {
	// Host is the SPICE host.
	Host string `json:"host"`

	// Password is the SPICE password.
	Password string `json:"password"`

	// Proxy is the SPICE proxy.
	Proxy string `json:"proxy"`

	// TLSPort is the TLS port for SPICE.
	TLSPort int `json:"tls-port"`

	// Type is the connection type.
	Type string `json:"type"`
}

// MTunnelSession holds the result of an mtunnel POST call.
type MTunnelSession struct {
	// Ticket is the tunnel ticket.
	Ticket string `json:"ticket"`

	// Upid is the task ID.
	Upid string `json:"upid"`
}

// Conn wraps a gorilla WebSocket connection and exposes typed I/O.
type Conn struct {
	ws     *websocket.Conn
	closed bool
}

// ReadMessage reads the next message from the connection.
// Returns messageType (websocket.TextMessage / BinaryMessage), data, error.
func (c *Conn) ReadMessage() (int, []byte, error) {
	if c.closed {
		return 0, nil, ErrWriteAfterClose
	}

	msgType, data, err := c.ws.ReadMessage()
	if err != nil {
		return 0, nil, fmt.Errorf("read message: %w", err)
	}

	return msgType, data, nil
}

// WriteMessage writes a message to the connection.
func (c *Conn) WriteMessage(messageType int, data []byte) error {
	if c.closed {
		return ErrWriteAfterClose
	}

	err := c.ws.WriteMessage(messageType, data)
	if err != nil {
		return fmt.Errorf("write message: %w", err)
	}

	return nil
}

// Close closes the WebSocket connection gracefully.
func (c *Conn) Close() error {
	if c.closed {
		return nil
	}

	c.closed = true

	_ = c.ws.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)

	err := c.ws.Close()
	if err != nil {
		return fmt.Errorf("close connection: %w", err)
	}

	return nil
}

// Underlying returns the raw gorilla websocket.Conn for callers that need it.
func (c *Conn) Underlying() *websocket.Conn {
	return c.ws
}

// ProxyClient performs PVE proxy operations: obtaining tickets via POST and
// opening the resulting WebSocket connections.
type ProxyClient struct {
	cfg *ProxyConfig
}

// NewProxyClient creates a ProxyClient from cfg. cfg must not be nil and
// cfg.Host must be non-empty.
func NewProxyClient(cfg *ProxyConfig) (*ProxyClient, error) {
	if cfg == nil {
		return nil, ErrNodeRequired
	}

	if cfg.Host == "" {
		return nil, ErrNodeRequired
	}

	if cfg.Port <= 0 {
		cfg.Port = 8006
	}

	return &ProxyClient{cfg: cfg}, nil
}

// --- QEMU (VM) endpoints ---

// VMVNCProxy calls POST /nodes/{node}/qemu/{vmid}/vncproxy.
// Returns a VNCSession containing port and ticket needed to open the WebSocket.
func (p *ProxyClient) VMVNCProxy(ctx context.Context, node string, vmid int) (*VNCSession, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/vncproxy", node, vmid)

	return p.postVNCSession(ctx, path, nil)
}

// VMVNCConnect opens the VNC WebSocket for a VM session obtained via VMVNCProxy.
// The PVE vncwebsocket path receives port and vncticket as query parameters.
func (p *ProxyClient) VMVNCConnect(ctx context.Context, node string, vmid int, session *VNCSession) (*Conn, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	err = validateVNCSession(session)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/vncwebsocket", node, vmid)

	return p.dialVNCWebSocket(ctx, path, session.Port, session.Ticket)
}

// VMTermProxy calls POST /nodes/{node}/qemu/{vmid}/termproxy.
func (p *ProxyClient) VMTermProxy(ctx context.Context, node string, vmid int, serial string) (*TermSession, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/termproxy", node, vmid)
	params := map[string]string{}

	if serial != "" {
		params["serial"] = serial
	}

	return p.postTermSession(ctx, path, params)
}

// VMSpiceProxy calls POST /nodes/{node}/qemu/{vmid}/spiceproxy.
// proxy is the optional SPICE proxy address.
func (p *ProxyClient) VMSpiceProxy(ctx context.Context, node string, vmid int, proxy string) (*SpiceSession, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/spiceproxy", node, vmid)
	params := map[string]string{}

	if proxy != "" {
		params["proxy"] = proxy
	}

	return p.postSpiceSession(ctx, path, params)
}

// VMTunnelWebSocket opens a GET /nodes/{node}/qemu/{vmid}/mtunnelwebsocket connection.
// socket is the unix socket path and ticket is from a prior mtunnel POST.
func (p *ProxyClient) VMTunnelWebSocket(
	ctx context.Context, node string, vmid int, socket, ticket string,
) (*Conn, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	if socket == "" {
		return nil, ErrSocketRequired
	}

	if ticket == "" {
		return nil, ErrTicketRequired
	}

	path := fmt.Sprintf("/nodes/%s/qemu/%d/mtunnelwebsocket", node, vmid)

	return p.dialMTunnelWebSocket(ctx, path, socket, ticket)
}

// --- LXC (container) endpoints ---

// LXCVNCProxy calls POST /nodes/{node}/lxc/{vmid}/vncproxy.
func (p *ProxyClient) LXCVNCProxy(ctx context.Context, node string, vmid int) (*VNCSession, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/lxc/%d/vncproxy", node, vmid)

	return p.postVNCSession(ctx, path, nil)
}

// LXCVNCConnect opens the VNC WebSocket for an LXC session.
func (p *ProxyClient) LXCVNCConnect(ctx context.Context, node string, vmid int, session *VNCSession) (*Conn, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	err = validateVNCSession(session)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/lxc/%d/vncwebsocket", node, vmid)

	return p.dialVNCWebSocket(ctx, path, session.Port, session.Ticket)
}

// LXCTermProxy calls POST /nodes/{node}/lxc/{vmid}/termproxy.
func (p *ProxyClient) LXCTermProxy(ctx context.Context, node string, vmid int) (*TermSession, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/lxc/%d/termproxy", node, vmid)

	return p.postTermSession(ctx, path, nil)
}

// LXCSpiceProxy calls POST /nodes/{node}/lxc/{vmid}/spiceproxy.
func (p *ProxyClient) LXCSpiceProxy(ctx context.Context, node string, vmid int, proxy string) (*SpiceSession, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/lxc/%d/spiceproxy", node, vmid)
	params := map[string]string{}

	if proxy != "" {
		params["proxy"] = proxy
	}

	return p.postSpiceSession(ctx, path, params)
}

// LXCTunnelWebSocket opens a GET /nodes/{node}/lxc/{vmid}/mtunnelwebsocket connection.
func (p *ProxyClient) LXCTunnelWebSocket(
	ctx context.Context, node string, vmid int, socket, ticket string,
) (*Conn, error) {
	err := validateNodeVMID(node, vmid)
	if err != nil {
		return nil, err
	}

	if socket == "" {
		return nil, ErrSocketRequired
	}

	if ticket == "" {
		return nil, ErrTicketRequired
	}

	path := fmt.Sprintf("/nodes/%s/lxc/%d/mtunnelwebsocket", node, vmid)

	return p.dialMTunnelWebSocket(ctx, path, socket, ticket)
}

// --- Node-level endpoints ---

// NodeVNCShell calls POST /nodes/{node}/vncshell.
// cmd is optional (e.g. "login", "upgrade", "ceph_install"); empty uses server default.
func (p *ProxyClient) NodeVNCShell(ctx context.Context, node string, cmd string) (*VNCSession, error) {
	if node == "" {
		return nil, ErrNodeRequired
	}

	path := fmt.Sprintf("/nodes/%s/vncshell", node)
	params := map[string]string{}

	if cmd != "" {
		params["cmd"] = cmd
	}

	return p.postVNCSession(ctx, path, params)
}

// NodeVNCConnect opens the VNC WebSocket at /nodes/{node}/vncwebsocket.
func (p *ProxyClient) NodeVNCConnect(ctx context.Context, node string, session *VNCSession) (*Conn, error) {
	if node == "" {
		return nil, ErrNodeRequired
	}

	err := validateVNCSession(session)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/nodes/%s/vncwebsocket", node)

	return p.dialVNCWebSocket(ctx, path, session.Port, session.Ticket)
}

// NodeTermProxy calls POST /nodes/{node}/termproxy.
func (p *ProxyClient) NodeTermProxy(ctx context.Context, node string) (*TermSession, error) {
	if node == "" {
		return nil, ErrNodeRequired
	}

	path := fmt.Sprintf("/nodes/%s/termproxy", node)

	return p.postTermSession(ctx, path, nil)
}

// NodeSpiceShell calls POST /nodes/{node}/spiceshell.
// cmd is optional.
func (p *ProxyClient) NodeSpiceShell(ctx context.Context, node string, cmd, proxy string) (*SpiceSession, error) {
	if node == "" {
		return nil, ErrNodeRequired
	}

	path := fmt.Sprintf("/nodes/%s/spiceshell", node)
	params := map[string]string{}

	if cmd != "" {
		params["cmd"] = cmd
	}

	if proxy != "" {
		params["proxy"] = proxy
	}

	return p.postSpiceSession(ctx, path, params)
}

// --- Internal helpers ---

// pveDataEnvelope matches the top-level {"data": ...} PVE JSON wrapper.
type pveDataEnvelope struct {
	Data json.RawMessage `json:"data"`
}

func (p *ProxyClient) postVNCSession(
	ctx context.Context, apiPath string, params map[string]string,
) (*VNCSession, error) {
	raw, err := p.httpPost(ctx, apiPath, params)
	if err != nil {
		return nil, err
	}

	var session VNCSession

	err = json.Unmarshal(raw, &session)
	if err != nil {
		return nil, fmt.Errorf("decode vncproxy response: %w", err)
	}

	return &session, nil
}

func (p *ProxyClient) postTermSession(
	ctx context.Context, apiPath string, params map[string]string,
) (*TermSession, error) {
	raw, err := p.httpPost(ctx, apiPath, params)
	if err != nil {
		return nil, err
	}

	var session TermSession

	err = json.Unmarshal(raw, &session)
	if err != nil {
		return nil, fmt.Errorf("decode termproxy response: %w", err)
	}

	return &session, nil
}

func (p *ProxyClient) postSpiceSession(
	ctx context.Context, apiPath string, params map[string]string,
) (*SpiceSession, error) {
	raw, err := p.httpPost(ctx, apiPath, params)
	if err != nil {
		return nil, err
	}

	var session SpiceSession

	err = json.Unmarshal(raw, &session)
	if err != nil {
		return nil, fmt.Errorf("decode spiceproxy response: %w", err)
	}

	return &session, nil
}

// httpPost calls the PVE API via HTTPDoer if set, otherwise falls back to a
// direct net/http POST with ticket auth headers. Returns the inner data bytes.
func (p *ProxyClient) httpPost(
	ctx context.Context, apiPath string, params map[string]string,
) (json.RawMessage, error) {
	if p.cfg.HTTPClient != nil {
		var envelope pveDataEnvelope

		err := p.cfg.HTTPClient.PostJSON(ctx, apiPath, params, &envelope)
		if err != nil {
			return nil, fmt.Errorf("PostJSON %s: %w", apiPath, err)
		}

		return envelope.Data, nil
	}

	// Fallback: direct net/http POST with form-encoded body.
	return p.directHTTPPost(ctx, apiPath, params)
}

// directHTTPPost performs a net/http POST, applying ticket auth headers.
func (p *ProxyClient) directHTTPPost(
	ctx context.Context, apiPath string, params map[string]string,
) (json.RawMessage, error) {
	scheme := p.cfg.Scheme
	if scheme == "" {
		scheme = "https"
	}

	baseURL := fmt.Sprintf("%s://%s:%d/api2/json%s", scheme, p.cfg.Host, p.cfg.Port, apiPath)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, baseURL, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("build POST request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	applyAuthHeaders(req.Header, p.cfg.Ticket, p.cfg.CSRFToken)

	httpClient := p.buildHTTPClient()

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", apiPath, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read POST response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: POST %s HTTP %d", ErrHTTPError, apiPath, resp.StatusCode)
	}

	var envelope pveDataEnvelope

	err = json.Unmarshal(body, &envelope)
	if err != nil {
		return nil, fmt.Errorf("decode PVE response envelope: %w", err)
	}

	return envelope.Data, nil
}

// buildHTTPClient constructs an http.Client honoring TLS settings.
func (p *ProxyClient) buildHTTPClient() *http.Client {
	tlsCfg := p.cfg.TLSConfig
	if tlsCfg == nil && p.cfg.Insecure {
		tlsCfg = &tls.Config{InsecureSkipVerify: true}
	}

	transport := &http.Transport{TLSClientConfig: tlsCfg}

	return &http.Client{Transport: transport}
}

// dialVNCWebSocket opens a WebSocket at apiPath with vncticket and port query params.
func (p *ProxyClient) dialVNCWebSocket(
	ctx context.Context, apiPath string, port int, ticket string,
) (*Conn, error) {
	if port < 5900 || port > 5999 {
		return nil, ErrPortOutOfRange
	}

	q := url.Values{}
	q.Set("port", strconv.Itoa(port))
	q.Set("vncticket", ticket)

	return p.dial(ctx, apiPath, q)
}

// dialMTunnelWebSocket opens a WebSocket at apiPath with socket and ticket query params.
func (p *ProxyClient) dialMTunnelWebSocket(
	ctx context.Context, apiPath string, socket, ticket string,
) (*Conn, error) {
	q := url.Values{}
	q.Set("socket", socket)
	q.Set("ticket", ticket)

	return p.dial(ctx, apiPath, q)
}

// dial opens the WebSocket connection, injecting auth headers.
func (p *ProxyClient) dial(ctx context.Context, apiPath string, query url.Values) (*Conn, error) {
	// Derive WebSocket scheme from HTTP scheme config.
	// "http" → "ws", anything else → "wss".
	scheme := "wss"
	if p.cfg.Scheme == "http" {
		scheme = "ws"
	}

	wsURL := url.URL{
		Scheme:   scheme,
		Host:     fmt.Sprintf("%s:%d", p.cfg.Host, p.cfg.Port),
		Path:     "/api2/json" + apiPath,
		RawQuery: query.Encode(),
	}

	tlsCfg := p.cfg.TLSConfig
	if tlsCfg == nil && p.cfg.Insecure {
		tlsCfg = &tls.Config{InsecureSkipVerify: true}
	}

	dialer := websocket.Dialer{
		TLSClientConfig: tlsCfg,
	}

	headers := http.Header{}
	applyAuthHeaders(headers, p.cfg.Ticket, p.cfg.CSRFToken)

	conn, resp, err := dialer.DialContext(ctx, wsURL.String(), headers)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}

		return nil, fmt.Errorf("dial %s: %w", wsURL.String(), err)
	}

	return &Conn{ws: conn}, nil
}

// minVMID is the minimum valid PVE virtual machine/container ID.
const minVMID = 100

// applyAuthHeaders sets Cookie and CSRFPreventionToken headers when values present.
func applyAuthHeaders(headers http.Header, ticket, csrf string) {
	if ticket != "" {
		headers.Set("Cookie", "PVEAuthCookie="+ticket)
	}

	if csrf != "" {
		headers.Set("Csrfpreventiontoken", csrf)
	}
}

// validateNodeVMID returns an error if node is empty or vmid < minVMID.
func validateNodeVMID(node string, vmid int) error {
	if node == "" {
		return ErrNodeRequired
	}

	if vmid < minVMID {
		return ErrVMIDInvalid
	}

	return nil
}

// validateVNCSession returns an error if session is nil, lacks ticket or port.
func validateVNCSession(session *VNCSession) error {
	if session == nil {
		return ErrNilSession
	}

	if session.Ticket == "" {
		return ErrSessionNoTicket
	}

	if session.Port == 0 {
		return ErrSessionNoPort
	}

	return nil
}
