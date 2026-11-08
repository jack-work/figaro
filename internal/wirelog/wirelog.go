// Package wirelog wraps http.RoundTripper to emit OTel span events and,
// with WithLogging, dump raw bytes to disk. Logging errors never fail requests.
package wirelog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func WithLogging(ctx context.Context, ariaID, dir string) context.Context {
	if ariaID == "" || dir == "" {
		return ctx
	}
	c, _ := cfgFromContext(ctx)
	c.aria, c.dir = ariaID, dir
	return context.WithValue(ctx, ctxKey{}, c)
}

// WithAria attributes this call's round-trips to an aria without turning on
// byte dumps.
func WithAria(ctx context.Context, ariaID string) context.Context {
	if ariaID == "" {
		return ctx
	}
	c, _ := cfgFromContext(ctx)
	c.aria = ariaID
	return context.WithValue(ctx, ctxKey{}, c)
}

func cfgFromContext(ctx context.Context) (cfg, bool) {
	c, ok := ctx.Value(ctxKey{}).(cfg)
	return c, ok
}

type Transport struct {
	Inner http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}

	logCfg, _ := cfgFromContext(req.Context())
	var (
		bodyBytes []byte
		logBase   string
	)

	logBase = dumpBase(logCfg)

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
	reqBytes := req.ContentLength
	if reqBytes <= 0 {
		reqBytes = int64(len(bodyBytes))
	}
	seq := depart(logCfg.aria, req.Method, redactedURL(req.URL).String(), reqBytes)

	var counted countingBody
	if req.Body != nil && req.Body != http.NoBody {
		counted.ReadCloser = req.Body
		req.Body = &counted
	}
	resp, err := inner.RoundTrip(req)
	duration := time.Since(start)
	if reqBytes <= 0 {
		reqBytes = counted.n
	}

	if f, ok := arrive(seq); ok {
		f.ReqBytes = reqBytes
		logRound(req.Context(), f, resp, duration, err)
	}
	emitMeta(req, resp, duration, reqBytes, logBase, err)

	if err != nil || resp == nil {
		return resp, err
	}

	if logBase != "" {
		respFile, ferr := createPrivate(logBase + ".resp.http")
		if ferr == nil {
			fmt.Fprintf(respFile, "%s %s\r\n", resp.Proto, resp.Status)
			redacted(resp.Header).Write(respFile)
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

func emitMeta(req *http.Request, resp *http.Response, duration time.Duration, reqBytes int64, logBase string, err error) {
	attrs := []attribute.KeyValue{
		attribute.String("http.method", req.Method),
		attribute.String("http.url", redactedURL(req.URL).String()),
		attribute.Int64("http.duration_ms", duration.Milliseconds()),
		attribute.Int64("http.req_bytes", reqBytes),
	}
	if c, ok := cfgFromContext(req.Context()); ok && c.aria != "" {
		attrs = append(attrs, attribute.String("figaro.aria", c.aria))
	}
	if resp != nil {
		attrs = append(attrs, attribute.Int("http.status_code", resp.StatusCode))
		if rid := requestID(resp.Header); rid != "" {
			attrs = append(attrs, attribute.String("http.request_id", rid))
		}
		if ra := RetryAfter(resp.Header); ra > 0 {
			attrs = append(attrs, attribute.Int64("http.retry_after_s", int64(ra/time.Second)))
		}
		for k, v := range rateLimitHeaders(resp.Header) {
			attrs = append(attrs, attribute.String("http.ratelimit."+k, v))
		}
	}
	if err != nil {
		attrs = append(attrs, attribute.String("http.error", err.Error()))
	}
	if logBase != "" {
		attrs = append(attrs, attribute.String("wirelog.path", logBase))
	}
	figOtel.Event(req.Context(), "http.request", attrs...)
	figOtel.RecordRequestBytes(req.Context(), reqBytes, attribute.String("host", req.URL.Host))
}

type countingBody struct {
	io.ReadCloser
	n int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)
	return n, err
}

func dumpBase(c cfg) string {
	if c.dir == "" || c.aria == "" {
		return ""
	}
	dir := filepath.Join(c.dir, sanitize(c.aria))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return ""
	}
	return filepath.Join(dir, fmt.Sprintf("%d", time.Now().UnixNano()))
}

type FrameDump struct {
	base string
	resp *os.File
}

func OpenFrameDump(ctx context.Context) *FrameDump {
	c, _ := cfgFromContext(ctx)
	base := dumpBase(c)
	if base == "" {
		return nil
	}
	return &FrameDump{base: base}
}

func (d *FrameDump) Request(method, rawURL string, header http.Header, body []byte) {
	if d == nil {
		return
	}
	f, err := createPrivate(d.base + ".req.http")
	if err != nil {
		return
	}
	defer f.Close()
	target, host := rawURL, ""
	if u, err := url.Parse(rawURL); err == nil {
		target, host = redactedURL(u).RequestURI(), u.Host
	}
	fmt.Fprintf(f, "%s %s HTTP/1.1\r\n", method, target)
	if host != "" {
		fmt.Fprintf(f, "Host: %s\r\n", host)
	}
	redacted(header).Write(f)
	fmt.Fprintf(f, "\r\n")
	f.Write(body)
}

func (d *FrameDump) Response(status string, header http.Header) {
	if d == nil || d.resp != nil {
		return
	}
	f, err := createPrivate(d.base + ".resp.http")
	if err != nil {
		return
	}
	d.resp = f
	fmt.Fprintf(f, "%s\r\n", status)
	redacted(header).Write(f)
	fmt.Fprintf(f, "\r\n")
}

func (d *FrameDump) Frame(b []byte) {
	if d == nil || d.resp == nil {
		return
	}
	d.resp.Write(b)
	d.resp.Write([]byte("\n"))
}

func (d *FrameDump) Close() {
	if d == nil || d.resp == nil {
		return
	}
	d.resp.Close()
	d.resp = nil
}

func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func redactedURL(u *url.URL) *url.URL {
	if u == nil {
		return &url.URL{}
	}
	out := *u
	if _, has := u.User.Password(); has {
		out.User = url.UserPassword(u.User.Username(), "[redacted]")
	}
	if u.RawQuery == "" {
		return &out
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		out.RawQuery = "[redacted]"
		return &out
	}
	masked := false
	for name, values := range q {
		if !isCredentialName(name) {
			continue
		}
		for i := range values {
			values[i] = "[redacted]"
		}
		masked = true
	}
	if masked {
		out.RawQuery = q.Encode()
	}
	return &out
}

func writeReqLog(path string, req *http.Request, body []byte) {
	f, err := createPrivate(path)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s %s\r\n", req.Method, redactedURL(req.URL).RequestURI(), req.Proto)
	if req.Host != "" {
		fmt.Fprintf(f, "Host: %s\r\n", req.Host)
	}
	redacted(req.Header).Write(f)
	fmt.Fprintf(f, "\r\n")
	f.Write(body)
}

func redacted(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		if !isCredentialName(name) {
			out[name] = values
			continue
		}
		masked := make([]string, len(values))
		for i, v := range values {
			masked[i] = "[redacted]"
			if scheme, _, ok := strings.Cut(v, " "); ok && strings.HasSuffix(strings.ToLower(name), "authorization") {
				masked[i] = scheme + " [redacted]"
			}
		}
		out[name] = masked
	}
	return out
}

func isCredentialName(name string) bool {
	n := strings.ToLower(name)
	if n == "cookie" || n == "set-cookie" {
		return true
	}
	for _, part := range []string{"auth", "key", "token", "secret"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
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
