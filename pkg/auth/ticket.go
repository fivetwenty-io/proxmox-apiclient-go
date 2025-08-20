package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fivetwenty-io/pve-apicilent-go/pkg/errors"
)

// TicketAuthenticator provides ticket-based authentication for PVE.
type TicketAuthenticator struct {
	baseURL     string
	httpClient  *http.Client
	credentials *Credentials
	ticket      *Ticket
	cookieName  string
}

// NewTicketAuthenticator creates a new ticket authenticator.
func NewTicketAuthenticator(baseURL string, credentials *Credentials, httpClient *http.Client) *TicketAuthenticator {
	if credentials.Realm == "" {
		credentials.Realm = "pam" // Default realm
	}

	return &TicketAuthenticator{
		baseURL:     baseURL,
		httpClient:  httpClient,
		credentials: credentials,
		cookieName:  "PVEAuthCookie",
	}
}

// Authenticate performs the authentication process.
func (ta *TicketAuthenticator) Authenticate() error {
	result, err := ta.login()
	if err != nil {
		return err
	}

	if result.TFAChallenge != nil {
		return &errors.TFARequiredError{
			Ticket:    result.TFAChallenge.Ticket,
			Challenge: result.TFAChallenge.Challenge,
			Types:     result.TFAChallenge.Types,
		}
	}

	if result.Ticket != nil {
		ta.ticket = result.Ticket
		return nil
	}

	return fmt.Errorf("authentication failed: no ticket received")
}

// IsAuthenticated checks if the current session is authenticated.
func (ta *TicketAuthenticator) IsAuthenticated() bool {
	return ta.ticket != nil && ta.ticket.IsValid()
}

// GetHeaders returns the authentication headers.
func (ta *TicketAuthenticator) GetHeaders() map[string]string {
	if ta.ticket == nil {
		return nil
	}
	return ta.ticket.GetHeaders()
}

// Refresh refreshes the authentication if necessary.
func (ta *TicketAuthenticator) Refresh() error {
	if ta.IsAuthenticated() {
		// Ticket is still valid
		return nil
	}

	// Re-authenticate
	return ta.Authenticate()
}

// Logout performs logout operations.
func (ta *TicketAuthenticator) Logout() error {
	if ta.ticket == nil {
		return nil
	}

	// Create logout request
	logoutURL := fmt.Sprintf("%s/access/ticket", ta.baseURL)
	req, err := http.NewRequest("DELETE", logoutURL, nil)
	if err != nil {
		return err
	}

	// Add authentication headers
	for key, value := range ta.GetHeaders() {
		req.Header.Set(key, value)
	}

	// Send logout request
	resp, err := ta.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Clear the ticket
	ta.ticket = nil

	return nil
}

// SetTicket sets the authentication ticket.
func (ta *TicketAuthenticator) SetTicket(ticket *Ticket) {
	ta.ticket = ticket
}

// GetTicket returns the current authentication ticket.
func (ta *TicketAuthenticator) GetTicket() *Ticket {
	return ta.ticket
}

// login performs the login operation.
func (ta *TicketAuthenticator) login() (*AuthResult, error) {
	loginURL := fmt.Sprintf("%s/access/ticket", ta.baseURL)

	// Prepare login data
	data := url.Values{}
	data.Set("username", fmt.Sprintf("%s@%s", ta.credentials.Username, ta.credentials.Realm))
	data.Set("password", ta.credentials.Password)

	if ta.credentials.OTP != "" {
		data.Set("otp", ta.credentials.OTP)
	}

	// Create login request
	req, err := http.NewRequest("POST", loginURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Send login request
	resp, err := ta.httpClient.Do(req)
	if err != nil {
		return nil, &errors.ConnectionError{
			Message: "login request failed",
			Cause:   err,
		}
	}
	defer resp.Body.Close()

	// Parse response
	var response struct {
		Data struct {
			Ticket              string                 `json:"ticket"`
			CSRFPreventionToken string                 `json:"CSRFPreventionToken"`
			Username            string                 `json:"username"`
			Cap                 map[string]interface{} `json:"cap"`
			// TFA fields
			NeedTFA   bool     `json:"NeedTFA,omitempty"`
			Ticket2   string   `json:"ticket2,omitempty"`
			Challenge string   `json:"challenge,omitempty"`
			TFATypes  []string `json:"tfa-types,omitempty"`
		} `json:"data"`
		Success int               `json:"success,omitempty"`
		Message string            `json:"message,omitempty"`
		Errors  map[string]string `json:"errors,omitempty"`
	}

	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to parse login response: %w", err)
	}

	// Check for errors
	if resp.StatusCode != http.StatusOK {
		return &AuthResult{
			Success: false,
			Error:   errors.ParseAPIError(resp.StatusCode, []byte(response.Message)),
		}, nil
	}

	// Check if TFA is required
	if response.Data.NeedTFA || response.Data.Ticket2 != "" {
		return &AuthResult{
			Success: false,
			TFAChallenge: &TFAChallenge{
				Ticket:    response.Data.Ticket2,
				Challenge: response.Data.Challenge,
				Types:     response.Data.TFATypes,
			},
		}, nil
	}

	// Successful login
	if response.Data.Ticket != "" {
		// Calculate ticket expiration (PVE tickets are valid for 2 hours)
		validUntil := time.Now().Add(2 * time.Hour)

		return &AuthResult{
			Success: true,
			Ticket: &Ticket{
				Value:      response.Data.Ticket,
				CSRFToken:  response.Data.CSRFPreventionToken,
				Username:   response.Data.Username,
				ValidUntil: validUntil,
			},
		}, nil
	}

	return &AuthResult{
		Success: false,
		Error:   fmt.Errorf("login failed: no ticket received"),
	}, nil
}

// CompleteTFA completes the two-factor authentication process.
func (ta *TicketAuthenticator) CompleteTFA(challenge *TFAChallenge, response *TFAResponse) (*AuthResult, error) {
	tfaURL := fmt.Sprintf("%s/access/tfa", ta.baseURL)

	// Prepare TFA data
	data := url.Values{}
	data.Set("response", response.Response)

	// Create TFA request
	req, err := http.NewRequest("POST", tfaURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Add the partial ticket
	if challenge.Ticket != "" {
		req.Header.Set("Cookie", fmt.Sprintf("%s=%s", ta.cookieName, challenge.Ticket))
	}

	// Send TFA request
	resp, err := ta.httpClient.Do(req)
	if err != nil {
		return nil, &errors.ConnectionError{
			Message: "TFA request failed",
			Cause:   err,
		}
	}
	defer resp.Body.Close()

	// Parse response
	var tfaResp struct {
		Data struct {
			Ticket              string `json:"ticket"`
			CSRFPreventionToken string `json:"CSRFPreventionToken"`
			Username            string `json:"username"`
		} `json:"data"`
		Success int               `json:"success,omitempty"`
		Message string            `json:"message,omitempty"`
		Errors  map[string]string `json:"errors,omitempty"`
	}

	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&tfaResp); err != nil {
		return nil, fmt.Errorf("failed to parse TFA response: %w", err)
	}

	// Check for errors
	if resp.StatusCode != http.StatusOK || tfaResp.Success != 1 {
		return &AuthResult{
			Success: false,
			Error:   errors.ParseAPIError(resp.StatusCode, []byte(tfaResp.Message)),
		}, nil
	}

	// Successful TFA
	if tfaResp.Data.Ticket != "" {
		// Calculate ticket expiration (PVE tickets are valid for 2 hours)
		validUntil := time.Now().Add(2 * time.Hour)

		ticket := &Ticket{
			Value:      tfaResp.Data.Ticket,
			CSRFToken:  tfaResp.Data.CSRFPreventionToken,
			Username:   tfaResp.Data.Username,
			ValidUntil: validUntil,
		}

		ta.ticket = ticket

		return &AuthResult{
			Success: true,
			Ticket:  ticket,
		}, nil
	}

	return &AuthResult{
		Success: false,
		Error:   fmt.Errorf("TFA failed: no ticket received"),
	}, nil
}
