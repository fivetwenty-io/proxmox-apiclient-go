package http

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fivetwenty-io/pve-apicilent-go/pkg/auth"
	"github.com/fivetwenty-io/pve-apicilent-go/pkg/client"
	"github.com/fivetwenty-io/pve-apicilent-go/pkg/errors"
)

// Client implements the HTTP client for PVE API communication.
type Client struct {
	baseURL       string
	httpClient    *http.Client
	authenticator auth.Authenticator
	middleware    []Middleware
	timeout       time.Duration
	maxRetries    int
	retryDelay    time.Duration
}

// Middleware defines a function that can modify requests or responses.
type Middleware func(*http.Request, Handler) (*http.Response, error)

// Handler represents the next handler in the middleware chain.
type Handler func(*http.Request) (*http.Response, error)

// NewClient creates a new HTTP client for PVE API.
func NewClient(options *client.Options) (*Client, error) {
	// Create base HTTP client
	transport := &http.Transport{
		MaxIdleConns:        options.KeepAlive,
		MaxIdleConnsPerHost: options.KeepAlive,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	}

	// Configure TLS if using HTTPS
	if options.Protocol == "https" {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12,
		}

		if options.SSLOptions != nil {
			// Configure TLS based on SSL options
			if options.SSLOptions.VerifyMode == client.SSLVerifyNone {
				tlsConfig.InsecureSkipVerify = true
			}

			// Load client certificates if provided
			if options.SSLOptions.ClientCert != "" && options.SSLOptions.ClientKey != "" {
				cert, err := tls.LoadX509KeyPair(options.SSLOptions.ClientCert, options.SSLOptions.ClientKey)
				if err != nil {
					return nil, fmt.Errorf("failed to load client certificates: %w", err)
				}
				tlsConfig.Certificates = []tls.Certificate{cert}
			}
		}

		transport.TLSClientConfig = tlsConfig
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   options.Timeout,
	}

	// Create authenticator
	var authenticator auth.Authenticator
	if options.APIToken != "" {
		// Use API token authentication
		token := &auth.Token{
			ID:     options.APIToken,
			Secret: options.APIToken, // Will be parsed properly in the authenticator
		}
		authenticator = auth.NewAPITokenAuthenticator(token)
	} else if options.Username != "" {
		// Use ticket authentication
		credentials := &auth.Credentials{
			Username: options.Username,
			Password: options.Password,
			Realm:    "pam", // Default realm, can be extracted from username
		}

		// Extract realm from username if present
		if parts := strings.Split(options.Username, "@"); len(parts) == 2 {
			credentials.Username = parts[0]
			credentials.Realm = parts[1]
		}

		authenticator = auth.NewTicketAuthenticator(options.GetBaseURL(), credentials, httpClient)
	}

	c := &Client{
		baseURL:       options.GetBaseURL(),
		httpClient:    httpClient,
		authenticator: authenticator,
		timeout:       options.Timeout,
		maxRetries:    3,
		retryDelay:    time.Second,
	}

	// Add default middleware
	c.middleware = []Middleware{
		c.authMiddleware,
		c.retryMiddleware,
		c.loggingMiddleware,
	}

	return c, nil
}

// Do performs an HTTP request with the specified method, path, and parameters.
func (c *Client) Do(method, path string, params map[string]interface{}) (*client.Response, error) {
	// Build request
	req, err := c.buildRequest(method, path, params)
	if err != nil {
		return nil, err
	}

	// Execute request through middleware chain
	resp, err := c.executeWithMiddleware(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Parse response
	return c.parseResponse(resp)
}

// SetHeader sets a header value for all requests.
func (c *Client) SetHeader(key, value string) {
	// This would be implemented with a header storage mechanism
	// For now, headers are set per request
}

// RemoveHeader removes a header from all requests.
func (c *Client) RemoveHeader(key string) {
	// This would be implemented with a header storage mechanism
}

// buildRequest creates an HTTP request for the given method, path, and parameters.
func (c *Client) buildRequest(method, path string, params map[string]interface{}) (*http.Request, error) {
	// Ensure path starts with /
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	fullURL := c.baseURL + path

	var body io.Reader
	var contentType string

	switch method {
	case "GET", "DELETE":
		// Add parameters to query string
		if len(params) > 0 {
			query := url.Values{}
			for key, value := range params {
				query.Add(key, fmt.Sprintf("%v", value))
			}
			fullURL += "?" + query.Encode()
		}
	case "POST", "PUT":
		// Encode parameters as form data
		if len(params) > 0 {
			formData := url.Values{}
			for key, value := range params {
				formData.Add(key, fmt.Sprintf("%v", value))
			}
			body = strings.NewReader(formData.Encode())
			contentType = "application/x-www-form-urlencoded"
		}
	}

	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, err
	}

	// Set content type if needed
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Set standard headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "pve-apiclient-go/1.0")

	return req, nil
}

// executeWithMiddleware executes the request through the middleware chain.
func (c *Client) executeWithMiddleware(req *http.Request) (*http.Response, error) {
	// Create the final handler that executes the actual request
	finalHandler := func(r *http.Request) (*http.Response, error) {
		return c.httpClient.Do(r)
	}

	// Build the middleware chain in reverse order
	handler := finalHandler
	for i := len(c.middleware) - 1; i >= 0; i-- {
		middleware := c.middleware[i]
		currentHandler := handler
		handler = func(r *http.Request) (*http.Response, error) {
			return middleware(r, currentHandler)
		}
	}

	// Execute the chain
	return handler(req)
}

// parseResponse parses the HTTP response into a client.Response.
func (c *Client) parseResponse(resp *http.Response) (*client.Response, error) {
	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check for non-success status codes
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.ParseAPIError(resp.StatusCode, body)
	}

	// Parse JSON response
	var result struct {
		Data    interface{}       `json:"data"`
		Success int               `json:"success,omitempty"`
		Message string            `json:"message,omitempty"`
		Errors  map[string]string `json:"errors,omitempty"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		// If JSON parsing fails, return raw body as data
		return &client.Response{
			Data: string(body),
			Code: resp.StatusCode,
		}, nil
	}

	// Check for API-level errors
	if result.Success == 0 && result.Message != "" {
		return nil, &errors.APIError{
			Message:  result.Message,
			Code:     resp.StatusCode,
			Errors:   result.Errors,
			HTTPCode: resp.StatusCode,
		}
	}

	return &client.Response{
		Data:   result.Data,
		Errors: result.Errors,
		Code:   resp.StatusCode,
	}, nil
}

// authMiddleware adds authentication headers to requests.
func (c *Client) authMiddleware(req *http.Request, next Handler) (*http.Response, error) {
	if c.authenticator != nil {
		// Ensure we're authenticated
		if !c.authenticator.IsAuthenticated() {
			if err := c.authenticator.Authenticate(); err != nil {
				return nil, fmt.Errorf("authentication failed: %w", err)
			}
		}

		// Add authentication headers
		headers := c.authenticator.GetHeaders()
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}

	// Execute the request
	resp, err := next(req)
	if err != nil {
		return nil, err
	}

	// Check if we need to re-authenticate
	if resp.StatusCode == 401 && c.authenticator != nil {
		// Close the response body
		_ = resp.Body.Close()

		// Try to refresh authentication
		if err := c.authenticator.Refresh(); err != nil {
			return nil, fmt.Errorf("failed to refresh authentication: %w", err)
		}

		// Update headers with new authentication
		headers := c.authenticator.GetHeaders()
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		// Retry the request
		return next(req)
	}

	return resp, nil
}

// retryMiddleware implements retry logic for failed requests.
func (c *Client) retryMiddleware(req *http.Request, next Handler) (*http.Response, error) {
	var lastErr error
	var lastResp *http.Response

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			time.Sleep(c.retryDelay * time.Duration(attempt))

			// Clone the request for retry
			bodyBytes, _ := io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := next(req)
		if err != nil {
			lastErr = err
			continue
		}

		// Check if we should retry based on status code
		if errors.IsRetryableCode(resp.StatusCode) {
			lastResp = resp
			_ = resp.Body.Close()
			continue
		}

		// Success or non-retryable error
		return resp, nil
	}

	if lastResp != nil {
		return lastResp, nil
	}

	return nil, fmt.Errorf("request failed after %d retries: %w", c.maxRetries, lastErr)
}

// loggingMiddleware logs requests and responses (placeholder for now).
func (c *Client) loggingMiddleware(req *http.Request, next Handler) (*http.Response, error) {
	// Could add logging here
	return next(req)
}

// SetTimeout sets the request timeout.
func (c *Client) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
	c.httpClient.Timeout = timeout
}

// SetMaxRetries sets the maximum number of retries.
func (c *Client) SetMaxRetries(retries int) {
	c.maxRetries = retries
}

// SetRetryDelay sets the delay between retries.
func (c *Client) SetRetryDelay(delay time.Duration) {
	c.retryDelay = delay
}
