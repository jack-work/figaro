// Package wirelog provides an http.RoundTripper that captures the
// raw bytes flowing to and from an upstream HTTP service. Intended
// for debugging — particularly the Anthropic provider's SSE stream
// where you want to see what was actually sent and received for a
// specific aria.
//
// The transport is opt-in per request: it only logs when the
// request's context has been stamped with WithAria. Empty Dir or
// no aria id means full passthrough; logging never fails the
// underlying request.
package wirelog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ctxKey struct{}

// WithAria stamps ctx with an aria id. Transport.RoundTrip extracts
// the id and uses it as the per-aria subdirectory under Dir.
// No-op when ariaID is empty.
func WithAria(ctx context.Context, ariaID string) context.Context {
	if ariaID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, ariaID)
}

func ariaFromContext(ctx context.Context) string {
	a, _ := ctx.Value(ctxKey{}).(string)
	return a
}

// Transport is an http.RoundTripper that mirrors request and
// response bytes to disk. With Dir set and ctx carrying an aria,
// it writes:
//
//	<Dir>/<aria>/<unix_ns>.req.http   request line + headers + body
//	<Dir>/<aria>/<unix_ns>.resp.http  status line + headers + body (tee'd live)
//
// SSE responses are tee'd as they stream — no buffering. Logging
// failures are swallowed so the underlying request never fails
// because of telemetry.
type Transport struct {
	Inner http.RoundTripper
	Dir   string
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	aria := ariaFromContext(req.Context())
	if t.Dir == "" || aria == "" {
		return inner.RoundTrip(req)
	}

	dir := filepath.Join(t.Dir, sanitize(aria))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return inner.RoundTrip(req)
	}
	base := filepath.Join(dir, fmt.Sprintf("%d", time.Now().UnixNano()))

	// Capture+replay the request body so we can both log and forward.
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return inner.RoundTrip(req)
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}
	writeReqLog(base+".req.http", req, bodyBytes)

	resp, err := inner.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	respFile, ferr := os.Create(base + ".resp.http")
	if ferr != nil {
		return resp, nil
	}
	fmt.Fprintf(respFile, "%s %s\r\n", resp.Proto, resp.Status)
	resp.Header.Write(respFile)
	fmt.Fprintf(respFile, "\r\n")

	resp.Body = &teeBody{
		body: resp.Body,
		tee:  io.TeeReader(resp.Body, respFile),
		out:  respFile,
	}
	return resp, nil
}

func writeReqLog(path string, req *http.Request, body []byte) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s %s\r\n", req.Method, req.URL.RequestURI(), req.Proto)
	if req.Host != "" {
		fmt.Fprintf(f, "Host: %s\r\n", req.Host)
	}
	req.Header.Write(f)
	fmt.Fprintf(f, "\r\n")
	f.Write(body)
}

type teeBody struct {
	body io.ReadCloser
	tee  io.Reader
	out  *os.File
}

func (t *teeBody) Read(p []byte) (int, error) { return t.tee.Read(p) }

func (t *teeBody) Close() error {
	err1 := t.body.Close()
	err2 := t.out.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// sanitize keeps only the characters that are valid in a figaro
// aria id (`[A-Za-z0-9_-]`); anything else becomes `_`. Defense in
// depth — aria ids should already be safe, but the dir name is
// untrusted from the transport's perspective.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}
