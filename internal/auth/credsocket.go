package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	credExpirySkew     = 30 * time.Second
	credRequestTimeout = 10 * time.Second
	credProbeTimeout   = 2 * time.Second
	credMaxBodyBytes   = 4 << 10
	credMaxReasonRunes = 200
)

type CredSocket struct {
	Socket  string
	Name    string
	Timeout time.Duration
	Now     func() time.Time

	clientOnce sync.Once
	httpClient *http.Client

	mu    sync.Mutex
	token string
	until time.Time
}

var _ TokenResolver = (*CredSocket)(nil)

func (c *CredSocket) client() *http.Client {
	c.clientOnce.Do(func() {
		socket := c.Socket
		dialer := &net.Dialer{Timeout: credRequestTimeout}
		c.httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socket)
				},
				DisableKeepAlives:  true,
				DisableCompression: true,
			},
		}
	})
	return c.httpClient
}

func (c *CredSocket) Resolve() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := nowOr(c.Now)
	if c.token != "" && now.Before(c.until) {
		return c.token, nil
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = credRequestTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tok, expiresAt, err := c.fetch(ctx)
	if err != nil {
		c.token, c.until = "", time.Time{}
		return "", err
	}
	var until time.Time
	if expiresAt > 0 {
		until = time.Unix(expiresAt, 0).Add(-credExpirySkew)
	}
	if now.Before(until) {
		c.token, c.until = tok, until
	} else {
		c.token, c.until = "", time.Time{}
	}
	return tok, nil
}

func (c *CredSocket) Invalidate(token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if token == "" || c.token == token {
		c.token, c.until = "", time.Time{}
	}
	return nil
}

func (c *CredSocket) ProbeStatus() string {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = credProbeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _, err := c.fetch(ctx)
	if err == nil {
		return "ready"
	}
	var statusErr *credStatusError
	if errors.As(err, &statusErr) {
		return statusErr.summary()
	}
	return "socket not answering: " + credDialSummary(err)
}

func (c *CredSocket) fetch(ctx context.Context) (string, int64, error) {
	endpoint := "http://unix/cred/" + url.PathEscape(c.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0, fmt.Errorf("cred %s (%s): %w", c.Name, c.Socket, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.client().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("cred %s (%s): %s", c.Name, c.Socket, credDialSummary(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, credMaxBodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("cred %s (%s): read response: %s", c.Name, c.Socket, credDialSummary(err))
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, parseCredStatusError(c.Socket, c.Name, resp.StatusCode, body)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", 0, fmt.Errorf("cred %s (%s): invalid JSON response", c.Name, c.Socket)
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", 0, fmt.Errorf("cred %s (%s): empty token in response", c.Name, c.Socket)
	}
	return out.Token, out.ExpiresAt, nil
}

type credStatusError struct {
	socket string
	name   string
	status int
	code   string
	reason string
}

func parseCredStatusError(socket, name string, status int, body []byte) *credStatusError {
	var payload struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	se := &credStatusError{socket: socket, name: name, status: status}
	if json.Unmarshal(body, &payload) == nil {
		se.code = sanitizeCredText(payload.Error)
		se.reason = sanitizeCredText(payload.Reason)
	}
	return se
}

func (e *credStatusError) summary() string {
	switch {
	case e.reason != "" && e.code != "":
		return fmt.Sprintf("HTTP %d (%s): %s", e.status, e.code, e.reason)
	case e.reason != "":
		return fmt.Sprintf("HTTP %d: %s", e.status, e.reason)
	case e.code != "":
		return fmt.Sprintf("HTTP %d (%s)", e.status, e.code)
	default:
		return fmt.Sprintf("HTTP %d", e.status)
	}
}

func (e *credStatusError) Error() string {
	msg := fmt.Sprintf("cred %s (%s): %s", e.name, e.socket, e.summary())
	if e.code == "credential_stale" {
		msg += fmt.Sprintf(" (run: hush cred login %s)", e.name)
	}
	return msg
}

func credDialSummary(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		err = uerr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return sanitizeCredText(op.Err.Error())
	}
	return sanitizeCredText(err.Error())
}

func sanitizeCredText(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
		if b.Len() >= credMaxReasonRunes {
			break
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
