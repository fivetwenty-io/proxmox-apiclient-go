package http

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"

	"github.com/fivetwenty-io/pve-apicilent-go/pkg/errors"
)

// ResponseParser handles parsing of HTTP responses from the PVE API.
type ResponseParser struct {
	// StrictMode enforces strict JSON parsing
	StrictMode bool

	// CustomParsers allows registration of custom parsers for specific paths
	CustomParsers map[string]ParseFunc
}

// ParseFunc is a custom parser function for responses.
type ParseFunc func(*http.Response) (interface{}, error)

// NewResponseParser creates a new response parser.
func NewResponseParser() *ResponseParser {
	return &ResponseParser{
		StrictMode:    false,
		CustomParsers: make(map[string]ParseFunc),
	}
}

// Parse parses an HTTP response into the appropriate type.
func (rp *ResponseParser) Parse(resp *http.Response, target interface{}) error {
	// Check for custom parser
	if parser, ok := rp.CustomParsers[resp.Request.URL.Path]; ok {
		result, err := parser(resp)
		if err != nil {
			return err
		}
		return rp.assignResult(result, target)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Check for non-success status codes
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.ParseAPIError(resp.StatusCode, body)
	}

	// Parse based on content type
	contentType := resp.Header.Get("Content-Type")
	switch {
	case isJSONContent(contentType):
		return rp.parseJSON(body, target)
	case isTextContent(contentType):
		return rp.parseText(body, target)
	default:
		// Try JSON first, fall back to text
		if err := rp.parseJSON(body, target); err == nil {
			return nil
		}
		return rp.parseText(body, target)
	}
}

// parseJSON parses JSON response body.
func (rp *ResponseParser) parseJSON(body []byte, target interface{}) error {
	// PVE API wraps data in a response envelope
	var envelope struct {
		Data    json.RawMessage   `json:"data"`
		Success int               `json:"success,omitempty"`
		Message string            `json:"message,omitempty"`
		Errors  map[string]string `json:"errors,omitempty"`
	}

	if err := json.Unmarshal(body, &envelope); err != nil {
		// Try parsing directly into target
		if rp.StrictMode {
			return fmt.Errorf("failed to parse response envelope: %w", err)
		}
		return json.Unmarshal(body, target)
	}

	// Check for API-level errors
	if envelope.Success == 0 && envelope.Message != "" {
		return &errors.APIError{
			Message: envelope.Message,
			Errors:  envelope.Errors,
		}
	}

	// Parse the data field into the target
	if len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, target)
	}

	// No data field, try to use the whole response
	return json.Unmarshal(body, target)
}

// parseText parses text response body.
func (rp *ResponseParser) parseText(body []byte, target interface{}) error {
	text := string(body)

	// Try to assign the text to the target
	return rp.assignResult(text, target)
}

// assignResult assigns a result value to the target.
func (rp *ResponseParser) assignResult(result interface{}, target interface{}) error {
	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Ptr {
		return fmt.Errorf("target must be a pointer")
	}

	targetElem := targetValue.Elem()
	resultValue := reflect.ValueOf(result)

	// Handle nil result
	if !resultValue.IsValid() {
		targetElem.Set(reflect.Zero(targetElem.Type()))
		return nil
	}

	// Try direct assignment
	if resultValue.Type().AssignableTo(targetElem.Type()) {
		targetElem.Set(resultValue)
		return nil
	}

	// Try conversion
	if resultValue.Type().ConvertibleTo(targetElem.Type()) {
		targetElem.Set(resultValue.Convert(targetElem.Type()))
		return nil
	}

	// Special case: string to various types
	if resultValue.Kind() == reflect.String {
		str := resultValue.String()
		switch targetElem.Kind() {
		case reflect.Int, reflect.Int64:
			if val, err := strconv.ParseInt(str, 10, 64); err == nil {
				targetElem.SetInt(val)
				return nil
			}
		case reflect.Float64:
			if val, err := strconv.ParseFloat(str, 64); err == nil {
				targetElem.SetFloat(val)
				return nil
			}
		case reflect.Bool:
			if val, err := strconv.ParseBool(str); err == nil {
				targetElem.SetBool(val)
				return nil
			}
		}
	}

	return fmt.Errorf("cannot assign %T to %T", result, target)
}

// RegisterCustomParser registers a custom parser for a specific path.
func (rp *ResponseParser) RegisterCustomParser(path string, parser ParseFunc) {
	rp.CustomParsers[path] = parser
}

// isJSONContent checks if the content type is JSON.
func isJSONContent(contentType string) bool {
	return contentType == "application/json" ||
		contentType == "application/json; charset=utf-8" ||
		contentType == "text/json"
}

// isTextContent checks if the content type is text.
func isTextContent(contentType string) bool {
	return contentType == "text/plain" ||
		contentType == "text/plain; charset=utf-8" ||
		contentType == "text/html" ||
		contentType == "text/html; charset=utf-8"
}

// ResponseHandler provides higher-level response handling.
type ResponseHandler struct {
	parser *ResponseParser
}

// NewResponseHandler creates a new response handler.
func NewResponseHandler() *ResponseHandler {
	return &ResponseHandler{
		parser: NewResponseParser(),
	}
}

// Handle processes an HTTP response and extracts the data.
func (rh *ResponseHandler) Handle(resp *http.Response) (interface{}, error) {
	var result interface{}
	if err := rh.parser.Parse(resp, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// HandleInto processes an HTTP response and unmarshals into the target.
func (rh *ResponseHandler) HandleInto(resp *http.Response, target interface{}) error {
	return rh.parser.Parse(resp, target)
}

// HandleList processes a response containing a list of items.
func (rh *ResponseHandler) HandleList(resp *http.Response) ([]interface{}, error) {
	var items []interface{}
	if err := rh.parser.Parse(resp, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// HandleMap processes a response containing a map.
func (rh *ResponseHandler) HandleMap(resp *http.Response) (map[string]interface{}, error) {
	var data map[string]interface{}
	if err := rh.parser.Parse(resp, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// HandleString processes a response containing a string.
func (rh *ResponseHandler) HandleString(resp *http.Response) (string, error) {
	var result string
	if err := rh.parser.Parse(resp, &result); err != nil {
		return "", err
	}
	return result, nil
}

// HandleBool processes a response containing a boolean.
func (rh *ResponseHandler) HandleBool(resp *http.Response) (bool, error) {
	var result bool
	if err := rh.parser.Parse(resp, &result); err != nil {
		return false, err
	}
	return result, nil
}

// HandleInt processes a response containing an integer.
func (rh *ResponseHandler) HandleInt(resp *http.Response) (int64, error) {
	var result int64
	if err := rh.parser.Parse(resp, &result); err != nil {
		return 0, err
	}
	return result, nil
}

// HandleFloat processes a response containing a float.
func (rh *ResponseHandler) HandleFloat(resp *http.Response) (float64, error) {
	var result float64
	if err := rh.parser.Parse(resp, &result); err != nil {
		return 0, err
	}
	return result, nil
}

// StreamHandler handles streaming responses.
type StreamHandler struct {
	// BufferSize is the size of the buffer for reading chunks
	BufferSize int

	// OnChunk is called for each chunk of data
	OnChunk func([]byte) error

	// OnComplete is called when streaming is complete
	OnComplete func() error
}

// NewStreamHandler creates a new stream handler.
func NewStreamHandler() *StreamHandler {
	return &StreamHandler{
		BufferSize: 4096,
	}
}

// Handle processes a streaming response.
func (sh *StreamHandler) Handle(resp *http.Response) error {
	defer resp.Body.Close()

	buffer := make([]byte, sh.BufferSize)
	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 && sh.OnChunk != nil {
			if err := sh.OnChunk(buffer[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}

	if sh.OnComplete != nil {
		return sh.OnComplete()
	}

	return nil
}
