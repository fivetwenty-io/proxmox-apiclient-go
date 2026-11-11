// Package redirecttest holds what the tests of every request path share when
// they check that a hostile response leaves no secret in an error or a log
// line. It serves raw HTTP responses, so a test can send header bytes that
// net/http's own server would refuse to write, and it lists the Location
// values that have broken the SDK's redirect handling.
package redirecttest

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Markers placed in the user information, the query, and the fragment of every
// hostile Location value. A test fails when any of them reaches an error or a
// log line.
const (
	SecretUser     = "HUNTER2"
	SecretQuery    = "QSECRET"
	SecretFragment = "FRAGSECRET"
)

// serveTimeout bounds how long a raw server waits for a request.
const serveTimeout = 10 * time.Second

// Case is one hostile Location header value.
type Case struct {
	// Name labels the case in test output.
	Name string

	// Location is the value sent after "Location: ", byte for byte.
	Location []byte
}

// Secrets returns the markers a clean error and a clean log line never hold.
func Secrets() []string {
	return []string{SecretUser, SecretQuery, SecretFragment, "user:"}
}

// Cases returns the Location values the SDK must not repeat. Each carries all
// three secret markers, so a leak through any part of the URL shows. The
// well-formed ones point at dead, an address nothing listens on, so a client
// that follows them fails to connect rather than reaching a real host. The
// rest are values that cannot be parsed or that the transport rejects, which
// are control bytes, an invalid percent escape, an invalid port, an open IPv6
// bracket, and a value that is not UTF-8.
func Cases(dead string) []Case {
	tail := "?ticket=" + SecretQuery + "#" + SecretFragment
	userinfo := "user:" + SecretUser + "@"

	return []Case{
		{"userinfo", []byte("http://" + userinfo + dead + "/x")},
		{"query", []byte("http://" + dead + "/x?ticket=" + SecretQuery)},
		{"fragment", []byte("http://" + dead + "/x#" + SecretFragment)},
		{"userinfo query and fragment", []byte("http://" + userinfo + dead + "/x" + tail)},
		{"relative with query", []byte("/elsewhere" + tail)},
		{"control byte", []byte("http://" + userinfo + dead + "/\x01x" + tail)},
		{"control byte in host", []byte("http://" + userinfo + "h\x01" + dead + "/x" + tail)},
		{"delete byte", []byte("http://" + userinfo + dead + "/\x7fx" + tail)},
		{"invalid percent escape", []byte("http://" + userinfo + dead + "/%zz" + tail)},
		{"invalid percent escape in query", []byte("http://" + userinfo + dead + "/x?ticket=%zz" + SecretQuery +
			"#" + SecretFragment)},
		{"invalid port", []byte("http://" + userinfo + "127.0.0.1:abc/x" + tail)},
		{"open bracket", []byte("http://" + userinfo + "[::1/x" + tail)},
		{"not UTF-8", []byte("http://" + userinfo + dead + "/x?ticket=\xff\xfe" + SecretQuery + "#\xc3" +
			SecretFragment)},
		{"not UTF-8 path", []byte("http://" + userinfo + dead + "/\xff\xfe" + tail)},
	}
}

// DeadAddr returns host:port for an address nothing listens on.
func DeadAddr(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	addr := listener.Addr().String()

	err = listener.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}

	return addr
}

// Server is a listener that answers every request with one raw response.
type Server struct {
	// Addr is host:port of the listener.
	Addr string

	hits atomic.Int32
	wg   sync.WaitGroup
}

// Hits returns how many requests the server has read.
func (s *Server) Hits() int { return int(s.hits.Load()) }

// URL returns the base URL of the server.
func (s *Server) URL() string { return "http://" + s.Addr }

// Serve starts a server that answers every request with status and a Location
// header holding location byte for byte, then closes the connection. A nil
// location sends no Location header at all. The server stops when the test
// ends.
func Serve(t *testing.T, status int, location []byte) *Server {
	t.Helper()

	var response bytes.Buffer

	fmt.Fprintf(&response, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))

	if location != nil {
		response.WriteString("Location: ")
		response.Write(location)
		response.WriteString("\r\n")
	}

	response.WriteString("Content-Length: 0\r\nConnection: close\r\n\r\n")

	return ServeRaw(t, response.Bytes())
}

// ServeRaw starts a server that answers every request with response, byte for
// byte, then closes the connection. The bytes need not be a valid HTTP
// response, so a test can send a status line, a Content-Length, or a
// Transfer-Encoding that net/http rejects. The server stops when the test ends.
func ServeRaw(t *testing.T, response []byte) *Server {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &Server{Addr: listener.Addr().String()}

	server.wg.Add(1)

	go func() {
		defer server.wg.Done()

		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			server.wg.Add(1)

			go func() {
				defer server.wg.Done()

				server.answer(conn, response)
			}()
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()

		server.wg.Wait()
	})

	return server
}

// answer reads one request from conn, writes response, and closes conn.
func (s *Server) answer(conn net.Conn, response []byte) {
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(serveTimeout))

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}

	_, _ = io.Copy(io.Discard, req.Body)
	_ = req.Body.Close()

	s.hits.Add(1)

	_, _ = conn.Write(response)
}

// Render returns the text a caller or a logger could print for value, which is
// what Error returns and what the %+v verb prints.
func Render(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case error:
		return []string{typed.Error(), fmt.Sprintf("%+v", typed)}
	default:
		return []string{fmt.Sprint(typed), fmt.Sprintf("%+v", typed)}
	}
}

// AssertClean fails the test when any secret marker, or any control byte or
// byte sequence that is not UTF-8 from the hostile value, appears in the text
// of err in either form, or in any of the extra lines, which are log lines or
// hook events the test captured.
func AssertClean(t *testing.T, err error, lines ...string) {
	t.Helper()

	if err == nil {
		t.Fatal("got no error, want the request to fail")
	}

	rendered := Render(err)
	rendered = append(rendered, lines...)

	assertCleanLines(t, rendered)
}

// AssertCleanText does what AssertClean does for text a test holds without an
// error value, such as the message a batch response carries.
func AssertCleanText(t *testing.T, text string, lines ...string) {
	t.Helper()

	if text == "" {
		t.Fatal("got no error text, want the request to fail")
	}

	assertCleanLines(t, append([]string{text}, lines...))
}

func assertCleanLines(t *testing.T, rendered []string) {
	t.Helper()

	for _, text := range rendered {
		for _, secret := range Secrets() {
			if strings.Contains(text, secret) {
				t.Errorf("%q holds the secret %q from the response", text, secret)
			}
		}

		for _, bad := range []string{"\x01", "\x7f", "\xff", "\xfe", "\xc3"} {
			if strings.Contains(text, bad) {
				t.Errorf("%q holds the raw byte %s from the response", text, strconv.QuoteToASCII(bad))
			}
		}
	}
}

// TrailerMarker is in the malformed trailer line BadTrailer sends.
const TrailerMarker = "EVILTRAILER"

// BadTrailer returns a 200 response whose chunked body holds body and ends with
// a trailer line the transport cannot read. The line holds an escape sequence,
// TrailerMarker, and the secret markers, so a test sees whether an error from
// reading the body repeats it. A body that is not a whole JSON value makes a
// streaming decoder read up to the trailer, as io.ReadAll always does.
func BadTrailer(body string) []byte {
	return []byte(fmt.Sprintf(
		"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n"+
			"Connection: close\r\n\r\n%x\r\n%s\r\n0\r\n\u001b[2J%s user:%s@x?ticket=%s\r\n\r\n",
		len(body), body, TrailerMarker, SecretUser, SecretQuery,
	))
}

// AssertNoTrailerText fails the test when text holds the escape sequence, the
// marker, or a secret from the trailer line BadTrailer sends.
func AssertNoTrailerText(t *testing.T, text string) {
	t.Helper()

	if text == "" {
		t.Fatal("got no error text, want the body read to fail")
	}

	for _, bad := range append(Secrets(), TrailerMarker, "\u001b") {
		if strings.Contains(text, bad) {
			t.Errorf("%q holds %q from the trailer", text, bad)
		}
	}
}
