package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/wirelog"
)

const (
	Name              = "decide"
	statusEdgeTimeout = 524
)

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("%s: HTTP %d: %s", Name, e.status, e.body) }

func (e *httpError) tooBig() bool {
	if e.status == statusEdgeTimeout {
		return true
	}
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
