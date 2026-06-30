package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPDoer is the minimal client interface refresh uses. *http.Client
// satisfies it; tests inject fakes.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// RefreshRequest is the input to the standard OAuth 2.0 refresh_token
// grant. Provider-specific refresh flows (Copilot's GET-style exchange,
// etc.) live in their own packages and don't go through here.
type RefreshRequest struct {
	TokenURL     string
	ClientID     string
	RefreshToken string

	// SafetyWindow is subtracted from the server's expires_in when
	// computing the returned Credential's ExpiresAt. Zero -> DefaultSafetyWindow.
	SafetyWindow time.Duration

	// HTTPClient defaults to a 30-second-timeout http.Client.
	HTTPClient HTTPDoer
}

// RefreshStandard performs the canonical refresh_token POST and returns
// a freshly-stamped Credential. The Extra map of any pre-existing
// credential is the caller's to merge back in.
//
// Errors wrap ErrRefreshTransient (5xx, network, parse) or
// ErrRefreshPermanent (4xx, empty access_token).
func RefreshStandard(ctx context.Context, in RefreshRequest) (Credential, error) {
	client := in.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	safety := in.SafetyWindow
	if safety == 0 {
		safety = DefaultSafetyWindow
	}

	bodyBytes, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     in.ClientID,
		"refresh_token": in.RefreshToken,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.TokenURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return Credential{}, fmt.Errorf("%w: build request: %v", ErrRefreshTransient, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Credential{}, fmt.Errorf("%w: %v", ErrRefreshTransient, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode >= 500:
		return Credential{}, fmt.Errorf("%w: token endpoint %d: %s", ErrRefreshTransient, resp.StatusCode, string(body))
	case resp.StatusCode != http.StatusOK:
		return Credential{}, fmt.Errorf("%w: token endpoint %d: %s", ErrRefreshPermanent, resp.StatusCode, string(body))
	}

	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Credential{}, fmt.Errorf("%w: parse token response: %v", ErrRefreshTransient, err)
	}
	if parsed.AccessToken == "" {
		return Credential{}, fmt.Errorf("%w: token endpoint returned empty access_token", ErrRefreshPermanent)
	}
	// Some providers omit refresh_token on refresh (no rotation). Keep the old.
	if parsed.RefreshToken == "" {
		parsed.RefreshToken = in.RefreshToken
	}

	var expiresAt time.Time
	if parsed.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second).Add(-safety)
	}
	return Credential{
		Access:    parsed.AccessToken,
		Refresh:   parsed.RefreshToken,
		ExpiresAt: expiresAt,
	}, nil
}
