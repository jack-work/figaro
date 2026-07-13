// Package copilot implements a Provider for GitHub Copilot's API,
// which exposes Claude models via the Anthropic Messages wire format
// behind a Copilot-specific auth layer and endpoint.
package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"text/template"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/store"
)

const (
	providerName      = "copilot"
	defaultBaseURL    = "https://api.individual.githubcopilot.com"
	copilotAPIVersion = "2026-06-01"
)

var copilotStaticHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

// copilotGitHubHeaders are added to GitHub API calls (token exchange,
// model listing) but NOT to the Anthropic messages endpoint.
var copilotGitHubHeaders = map[string]string{
	"X-GitHub-Api-Version": copilotAPIVersion,
}

type Copilot struct {
	inner     *anthropic.Anthropic
	tokenSrc  *CopilotTokenSource
	mu        sync.Mutex
	Templates *template.Template
}

func New(knobs provider.Knobs, githubToken auth.TokenResolver, enterpriseDomain string, cacheOpen func(string) (store.Log[[]json.RawMessage], error)) (*Copilot, error) {
	if githubToken == nil {
		return nil, fmt.Errorf("copilot: nil token resolver (need GitHub access token)")
	}
	tokenSrc := NewCopilotTokenSource(githubToken, enterpriseDomain)
	inner, err := anthropic.New(knobs, tokenSrc, cacheOpen)
	if err != nil {
		return nil, err
	}
	return &Copilot{inner: inner, tokenSrc: tokenSrc}, nil
}

func (c *Copilot) Name() string        { return providerName }
func (c *Copilot) Fingerprint() string  { return c.inner.Fingerprint() }
func (c *Copilot) SetModel(model string) { c.inner.SetModel(model) }

func (c *Copilot) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	token, err := c.tokenSrc.Resolve()
	if err != nil {
		return nil, fmt.Errorf("copilot models: %w", err)
	}
	baseURL := baseURLFromToken(token, c.tokenSrc.domain)
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotStaticHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range copilotGitHubHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot models %d: %s", resp.StatusCode, body)
	}
	var raw struct {
		Data []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled bool   `json:"model_picker_enabled"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var models []provider.ModelInfo
	for _, m := range raw.Data {
		if !m.ModelPickerEnabled {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, provider.ModelInfo{
			ID:       m.ID,
			Name:     name,
			Provider: providerName,
		})
	}
	return models, nil
}

func (c *Copilot) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	token, err := c.tokenSrc.Resolve()
	if err != nil {
		return fmt.Errorf("resolve copilot token: %w", err)
	}
	baseURL := baseURLFromToken(token, c.tokenSrc.domain)
	return c.inner.SendWithTransport(ctx, in, bus, func(ctx context.Context, body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		req.Header.Set("Openai-Intent", "conversation-edits")
		req.Header.Set("X-Initiator", "user")
		for k, v := range copilotStaticHeaders {
			req.Header.Set(k, v)
		}
		if os.Getenv("FIGARO_COPILOT_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "\n[copilot debug] POST %s\n", req.URL)
			for k, v := range req.Header {
				fmt.Fprintf(os.Stderr, "  %s: %s\n", k, v)
			}
			if len(body) < 2000 {
				fmt.Fprintf(os.Stderr, "  body: %s\n", body)
			} else {
				fmt.Fprintf(os.Stderr, "  body: %s...(%d bytes)\n", body[:500], len(body))
			}
		}
		resp, rerr := c.inner.HTTPClient.Do(req)
		if rerr != nil {
			return nil, rerr
		}
		if os.Getenv("FIGARO_COPILOT_DEBUG") != "" && resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "  response %d: %s\n", resp.StatusCode, respBody)
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
		}
		return resp, nil
	})
}

func baseURLFromToken(token, enterpriseDomain string) string {
	// Copilot tokens contain proxy-ep=<host> which determines the API endpoint.
	start := bytes.Index([]byte(token), []byte("proxy-ep="))
	if start >= 0 {
		start += len("proxy-ep=")
		end := bytes.IndexByte([]byte(token)[start:], ';')
		var host string
		if end < 0 {
			host = string([]byte(token)[start:])
		} else {
			host = string([]byte(token)[start : start+end])
		}
		if len(host) > 6 && host[:6] == "proxy." {
			host = "api." + host[6:]
		}
		return "https://" + host
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return defaultBaseURL
}

// CopilotTokenSource exchanges a GitHub access token for a short-lived
// Copilot session token, caching it until near expiry.
type CopilotTokenSource struct {
	github    auth.TokenResolver
	domain    string
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewCopilotTokenSource(github auth.TokenResolver, enterpriseDomain string) *CopilotTokenSource {
	return &CopilotTokenSource{github: github, domain: enterpriseDomain}
}

func (s *CopilotTokenSource) Resolve() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expiresAt) {
		return s.token, nil
	}
	githubToken, err := s.github.Resolve()
	if err != nil {
		return "", fmt.Errorf("copilot: resolve github token: %w", err)
	}
	tok, exp, err := exchangeCopilotToken(githubToken, s.domain)
	if err != nil {
		return "", err
	}
	s.token = tok
	s.expiresAt = exp
	return tok, nil
}

func (s *CopilotTokenSource) Invalidate(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token = ""
		s.expiresAt = time.Time{}
	}
	return s.github.Invalidate("")
}

func exchangeCopilotToken(githubAccessToken, enterpriseDomain string) (token string, expiresAt time.Time, err error) {
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	tokenURL := fmt.Sprintf("https://api.%s/copilot_internal/v2/token", domain)
	req, err := http.NewRequest("GET", tokenURL, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+githubAccessToken)
	for k, v := range copilotStaticHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range copilotGitHubHeaders {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("copilot token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("copilot token exchange %d: %s", resp.StatusCode, body)
	}
	var parsed struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("copilot token parse: %w", err)
	}
	if parsed.Token == "" {
		return "", time.Time{}, fmt.Errorf("copilot token exchange returned empty token")
	}
	exp := time.Unix(parsed.ExpiresAt, 0).Add(-5 * time.Minute)
	return parsed.Token, exp, nil
}
