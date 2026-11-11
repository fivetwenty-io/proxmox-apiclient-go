package http

import (
	"errors"
)

// permanentMarker is the method an error carries to tell the retry middleware
// that repeating the request cannot succeed. Permanent adds it to any error,
// and an error type of a caller's own can implement it directly.
type permanentMarker interface {
	Permanent() bool
}

// permanentError wraps an error with the permanent marker.
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

func (e *permanentError) Permanent() bool { return true }

// Permanent marks err as one the retry middleware must not retry. A
// WrapTransport wrapper returns it from RoundTrip when it refuses a request on
// purpose, because the middleware otherwise attempts a refused GET again until
// its retry count runs out. The marker survives the wrapping that net/http
// and the middleware add, and errors.Is and errors.As still reach err. A nil
// err stays nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return &permanentError{err: err}
}

// IsPermanent reports whether err, or an error it wraps, carries the permanent
// marker. An error type of a caller's own is permanent when it has a
// Permanent() bool method that returns true.
func IsPermanent(err error) bool {
	var marker permanentMarker

	return errors.As(err, &marker) && marker.Permanent()
}
