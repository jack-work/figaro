// Package wirelog wraps http.RoundTripper to emit OTel span events
// (always-on metadata) and optionally dump raw bytes to disk
// (opt-in via WithLogging). Logging errors never fail requests.
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

// WithLogging stamps ctx to enable raw byte dumps for this call.
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

// Transport wraps an http.RoundTripper with telemetry.
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

	// Only materialize the body when writing to disk.
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

// sanitize prevents path traversal from aria ids.
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
