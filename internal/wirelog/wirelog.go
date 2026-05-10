// Package wirelog provides an http.RoundTripper that emits
// per-request control metadata as OTel span events and, when the
// caller opts in via WithLogging, mirrors raw bytes (request + tee'd
// response) to disk under a per-aria directory.
//
// Two channels of telemetry, kept deliberately separate:
//
//   - Always-on span events on the surrounding turn span, carrying
//     non-PII control fields (method, url, status, duration,
//     request_id, byte counts). Cheap, queryable, safe to retain.
//   - Opt-in raw-byte dumps to <Dir>/<aria>/<unix_ns>.{req,resp}.http
//     for byte-for-byte replay/debug. Contains tokens and prompt
//     content — gate behind a chalkboard / env-var setting per aria.
//
// Logging never fails the underlying request: any I/O error in the
// telemetry path falls through to a passthrough.
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

	"go.opentelemetry.io/otel/attribute"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

type ctxKey struct{}

type cfg struct {
	aria string
	dir  string
}

// WithLogging stamps ctx so the Transport will dump raw request and
// response bytes for this call to <dir>/<aria>/<unix_ns>.{req,resp}.http.
// Empty aria or dir → no stamp; the Transport falls back to the
// always-on metadata-only path.
func WithLogging(ctx context.Context, ariaID, dir string) context.Context {
	if ariaID == "" || dir == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, cfg{aria: ariaID, dir: dir})
}

func cfgFromContext(ctx context.Context) (cfg, bool) {
	c, ok := ctx.Value(ctxKey{}).(cfg)
	return c, ok
}

// Transport wraps an http.RoundTripper. It always emits a
// "http.request" span event on the request's surrounding span with
// control metadata; it additionally writes raw request and response
// bytes to disk when ctx has been stamped via WithLogging.
type Transport struct {
	Inner http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}

	logCfg, doLog := cfgFromContext(req.Context())
	var (
		bodyBytes []byte
		logBase   string // file path without suffix; empty when not logging
	)

	if doLog {
		dir := filepath.Join(logCfg.dir, sanitize(logCfg.aria))
		if err := os.MkdirAll(dir, 0o700); err == nil {
			logBase = filepath.Join(dir, fmt.Sprintf("%d", time.Now().UnixNano()))
		}
	}

	// Materialize + replay the body only when we're actually going
	// to write it to disk. Skipping this in the metadata-only path
	// keeps multi-MB request bodies streaming without an extra
	// in-memory copy.
	if logBase != "" && req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err == nil {
			bodyBytes = b
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
		writeReqLog(logBase+".req.http", req, bodyBytes)
	}

	start := time.Now()
	resp, err := inner.RoundTrip(req)
	duration := time.Since(start)

	// Always emit metadata, even on transport error.
	emitMeta(req, resp, duration, len(bodyBytes), logBase, err)

	if err != nil || resp == nil {
		return resp, err
	}

	if logBase != "" {
		respFile, ferr := os.Create(logBase + ".resp.http")
		if ferr == nil {
			fmt.Fprintf(respFile, "%s %s\r\n", resp.Proto, resp.Status)
			resp.Header.Write(respFile)
			fmt.Fprintf(respFile, "\r\n")
			resp.Body = &teeBody{
				body: resp.Body,
				tee:  io.TeeReader(resp.Body, respFile),
				out:  respFile,
			}
		}
	}

	return resp, nil
}

func emitMeta(req *http.Request, resp *http.Response, duration time.Duration, reqBytes int, logBase string, err error) {
	attrs := []attribute.KeyValue{
		attribute.String("http.method", req.Method),
		attribute.String("http.url", req.URL.String()),
		attribute.Int64("http.duration_ms", duration.Milliseconds()),
		attribute.Int("http.req_bytes", reqBytes),
	}
	if resp != nil {
		attrs = append(attrs, attribute.Int("http.status_code", resp.StatusCode))
		if rid := resp.Header.Get("request-id"); rid != "" {
			attrs = append(attrs, attribute.String("http.request_id", rid))
		} else if rid := resp.Header.Get("x-request-id"); rid != "" {
			attrs = append(attrs, attribute.String("http.request_id", rid))
		}
	}
	if err != nil {
		attrs = append(attrs, attribute.String("http.error", err.Error()))
	}
	if logBase != "" {
		attrs = append(attrs, attribute.String("wirelog.path", logBase))
	}
	figOtel.Event(req.Context(), "http.request", attrs...)
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

// sanitize defends against a hostile aria id leaking into the
// filesystem. Aria ids today are constrained to [A-Za-z0-9_-] so
// this is normally a no-op.
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
