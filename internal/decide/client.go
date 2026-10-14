package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/wirelog"
)

const (
	Name           = "decisions"
	DefaultModel   = "jev-latest"
	DefaultBaseURL = "https://api.typesafe.ai/v1"
	BaseURLEnv     = "DECISIONS_BASE_URL"
)

var CredentialEnvs = []string{"TYPESAFE_API_KEY", "EXPLABS_API_KEY"}

type Settings struct {
	APIKey   string `toml:"api_key"`
	BaseURL  string `toml:"base_url,omitempty"`
	Model    string `toml:"model,omitempty"`
	Provider string `toml:"provider,omitempty"`
	API      string `toml:"api,omitempty"`
}

func LoadSettings(loaded *config.Loaded) (Settings, error) {
	var s Settings
	if loaded != nil {
		path := loaded.ProviderAuthPath(Name)
		data, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return s, err
		default:
			if _, err := toml.Decode(string(data), &s); err != nil {
				return s, fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv(BaseURLEnv)); v != "" {
		s.BaseURL = v
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
	if s.Model == "" {
		s.Model = DefaultModel
	}
	return s, nil
}

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("%s: HTTP %d: %s", Name, e.status, e.body) }

func (e *httpError) tooBig() bool {
	if e.status != http.StatusBadRequest {
		return false
	}
	for _, sign := range []string{`"invalid_request"`, "exceeds the token limit", "context_length_exceeded"} {
		if strings.Contains(e.body, sign) {
			return true
		}
	}
	return false
}

type Client struct {
	auth    auth.TokenResolver
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string, resolver auth.TokenResolver) *Client {
	return &Client{
		auth:    resolver,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 2 * time.Minute, Transport: &wirelog.Transport{Inner: http.DefaultTransport}},
	}
}

func (c *Client) post(ctx context.Context, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.auth != nil {
		if key, err := c.auth.Resolve(); err == nil && key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(raw))}
	}
	return raw, nil
}
