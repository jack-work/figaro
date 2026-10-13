package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

const maxRequestBody = 64 << 20

type Upstream interface {
	Endpoint(t routed.Transport) string
	Base() string
	Headers(t routed.Transport) map[string]string
	Credential() auth.TokenResolver
	Models(ctx context.Context) ([]provider.ModelInfo, error)
}

type Server struct {
	upstreams map[string]Upstream
	client    *http.Client
	log       *slog.Logger
}

func New(upstreams map[string]Upstream, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{
		upstreams: upstreams,
		client: &http.Client{Transport: &http.Transport{
			Proxy:              http.ProxyFromEnvironment,
			DisableCompression: true,
			ForceAttemptHTTP2:  true,
		}},
		log: log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/chat/completions", s.routeByModel(routed.Chat))
	mux.HandleFunc("POST /v1/responses", s.routeByModel(routed.Responses))
	mux.HandleFunc("POST /v1/messages", s.routeByModel(routed.Messages))
	mux.HandleFunc("POST /v1/systemone", s.routeByModelTo("", systemOne))
	mux.HandleFunc("/u/{provider}/{rest...}", s.passthrough)
	return loopbackOnly(mux)
}

func ListenLoopback(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("relay: refusing %q: the router is unauthenticated and binds loopback only", addr)
	}
	return net.Listen("tcp", addr)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !isLoopbackHost(host) {
			http.Error(w, "relay: Host must be a loopback name", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !isLoopbackHost(u.Hostname()) {
				http.Error(w, "relay: cross-origin requests are refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) names() []string {
	names := make([]string, 0, len(s.upstreams))
	for n := range s.upstreams {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *Server) upstream(w http.ResponseWriter, name string) (Upstream, bool) {
	up, ok := s.upstreams[name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("relay: no provider %q (serving: %s)", name, strings.Join(s.names(), ", ")))
	}
	return up, ok
}

func (s *Server) passthrough(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	up, ok := s.upstream(w, name)
	if !ok {
		return
	}
	rest := "/" + r.PathValue("rest")
	target := up.Base() + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	s.relay(w, r, name, up, routed.TransportForPath(rest), target, body)
}

func systemOne(up Upstream) string { return up.Base() + "/systemone" }

func (s *Server) routeByModel(t routed.Transport) http.HandlerFunc {
	return s.routeByModelTo(t, func(up Upstream) string { return up.Endpoint(t) })
}

func (s *Server) routeByModelTo(t routed.Transport, target func(Upstream) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			writeError(w, http.StatusBadRequest, "relay: request body is not a JSON object: "+err.Error())
			return
		}
		var model string
		if err := json.Unmarshal(fields["model"], &model); err != nil || model == "" {
			writeError(w, http.StatusBadRequest, "relay: request names no model")
			return
		}
		name, bare, ok := strings.Cut(model, "/")
		if !ok || bare == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("relay: model %q must be <provider>/<model> (serving: %s)", model, strings.Join(s.names(), ", ")))
			return
		}
		up, ok := s.upstream(w, name)
		if !ok {
			return
		}
		fields["model"], _ = json.Marshal(bare)
		rewritten, err := json.Marshal(fields)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.relay(w, r, name, up, t, target(up), rewritten)
	}
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRequestBody {
		return nil, fmt.Errorf("relay: request body exceeds %d bytes", maxRequestBody)
	}
	return body, nil
}

var hopByHop = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

var clientOnly = []string{
	"Authorization", "X-Api-Key", "Cookie", "Origin", "Referer", "Content-Length",
}

func (s *Server) relay(w http.ResponseWriter, r *http.Request, name string, up Upstream, t routed.Transport, target string, body []byte) {
	start := time.Now()
	cred := up.Credential()
	token, err := cred.Resolve()
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("relay: %s: resolve credential: %v", name, err))
		return
	}
	resp, err := s.forward(r, up, t, target, body, token)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && token != "" {
		resp.Body.Close()
		if ierr := cred.Invalidate(token); ierr != nil {
			s.log.Warn("invalidate credential", "provider", name, "err", ierr)
		}
		if token, err = cred.Resolve(); err == nil {
			resp, err = s.forward(r, up, t, target, body, token)
		}
	}
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.Canceled) {
			status = 499
		}
		s.log.Info("relay", "provider", name, "method", r.Method, "target", target, "err", err)
		writeError(w, status, fmt.Sprintf("relay: %s: %v", name, err))
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	for _, h := range hopByHop {
		w.Header().Del(h)
	}
	w.WriteHeader(resp.StatusCode)
	n, copyErr := copyFlushing(w, resp.Body)
	s.log.Info("relay", "provider", name, "method", r.Method, "target", target,
		"status", resp.StatusCode, "bytes", n, "ms", time.Since(start).Milliseconds(), "err", copyErr)
}

func (s *Server) forward(r *http.Request, up Upstream, t routed.Transport, target string, body []byte, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body == nil {
		req.Body = http.NoBody
	}
	req.Header = r.Header.Clone()
	for _, h := range hopByHop {
		req.Header.Del(h)
	}
	for _, h := range clientOnly {
		req.Header.Del(h)
	}
	if t == routed.Messages && req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	for k, v := range up.Headers(t) {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return s.client.Do(req)
}

func copyFlushing(w http.ResponseWriter, src io.Reader) (int64, error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

type modelRow struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	OwnedBy       string `json:"owned_by"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxOutput     int    `json:"max_output_tokens,omitempty"`
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	rows := []modelRow{}
	var failures []string
	for _, name := range s.names() {
		list, err := s.upstreams[name].Models(r.Context())
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for _, m := range list {
			rows = append(rows, modelRow{
				ID: name + "/" + m.ID, Object: "model", OwnedBy: name,
				Name: m.Name, ContextWindow: m.ContextWindow, MaxOutput: m.MaxTokens,
			})
		}
	}
	for _, f := range failures {
		s.log.Warn("models", "err", f)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": rows, "errors": failures})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "figaro_relay", "message": msg}})
}
