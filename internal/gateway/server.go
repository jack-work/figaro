package gateway

// THE SERVER: one listener, two faces, one admission decision in front of both.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is everything `figaro serve` decides.
type Config struct {
	// Listen is a URI: unix:///path, tcp://host:port. Not an http:// URL --
	// that is what a CLIENT calls this gateway, and conflating the two is how
	// a bind address ends up with a scheme that implies TLS it does not have.
	Listen string
	// AngelusSocket is the daemon this gateway fronts.
	AngelusSocket string
	// Authn names the authenticator: "upstream", "doorkey", or "none".
	Authn string
	// Doorkey is the shared secret for Authn == "doorkey".
	Doorkey string
	// Origins that may open a browser connection. Empty refuses all of them.
	Origins []string
	// Policy decides methods once an identity is known. nil allows everything.
	Policy MethodPolicy
	// Insecure waives the structural refusals in Check. It exists so the
	// refusals can be tested and so a developer can override deliberately;
	// it is never set by a config file, only by an explicit flag.
	Insecure bool
	Log      *slog.Logger
}

// MethodPolicy decides whether an identity may call a method.
type MethodPolicy interface {
	Allow(id Identity, method string) Decision
}

// Check enforces the structural safety rules of plans/http-gateway.md §5.
// These are REFUSALS, not warnings: the combinations below are authentication
// bypasses, and a bypass that merely logs is a bypass.
func (c Config) Check() error {
	scheme, addr, err := splitListen(c.Listen)
	if err != nil {
		return err
	}
	local := scheme == "unix" || isLoopback(addr)

	switch c.Authn {
	case "upstream":
		// `upstream` believes Remote-* headers. Those are only meaningful if
		// nothing but the reverse proxy can reach us; on a public bind any
		// client sets them and becomes anyone.
		if !local && !c.Insecure {
			return fmt.Errorf(
				"refusing to start: authn=upstream trusts Remote-* headers, but %s is reachable "+
					"off-host.\nAnyone who can reach it can claim any identity.\n"+
					"Bind unix:// or loopback and let the proxy reach you there", c.Listen)
		}
	case "doorkey":
		if c.Doorkey == "" {
			return errors.New("authn=doorkey needs a secret: set one with `figaro serve --doorkey-stdin`")
		}
		if len(c.Doorkey) < 16 {
			return errors.New("doorkey is too short: use at least 16 bytes of randomness")
		}
		if !local && !c.Insecure {
			return fmt.Errorf(
				"refusing to start: authn=doorkey would send a bearer token over plaintext to %s.\n"+
					"Put TLS in front, bind loopback, or pass --insecure if this is a trusted network", c.Listen)
		}
	case "none", "":
		if !local && !c.Insecure {
			return fmt.Errorf(
				"refusing to start: authn=none on %s is an open door to a daemon that runs shell "+
					"commands.\nBind unix:// or loopback, or choose an authenticator", c.Listen)
		}
	default:
		return fmt.Errorf("unknown authn %q: want upstream, doorkey, or none", c.Authn)
	}
	return nil
}

// Server is the gateway.
type Server struct {
	cfg   Config
	pool  *pool
	http  *http.Server
	ln    net.Listener
	log   *slog.Logger
	authn authenticator
}

// New builds a server. It does NOT listen; Serve does.
func New(cfg Config) (*Server, error) {
	if err := cfg.Check(); err != nil {
		return nil, err
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	authn, err := newAuthenticator(cfg)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:   cfg,
		log:   log,
		authn: authn,
		pool:  newPool(cfg.AngelusSocket, log),
	}

	mux := http.NewServeMux()
	tunnel := &Tunnel{
		Dial:    UnixDialer(cfg.AngelusSocket),
		Origins: cfg.Origins,
		Log:     log,
	}
	mux.Handle("GET /v1/socket", s.admit(tunnelWithPolicy(tunnel, s, cfg)))

	// The form face. Registered per-method so an unsupported verb gets 405
	// from the mux rather than a hand-rolled branch in every handler.
	mux.Handle("GET /v1/forms", s.admit(http.HandlerFunc(s.listForms)))
	mux.Handle("GET /v1/forms/{id}", s.admit(http.HandlerFunc(s.getForm)))
	mux.Handle("PATCH /v1/forms/{id}", s.admit(http.HandlerFunc(s.patchForm)))
	mux.Handle("GET /v1/forms/{id}/deltas", s.admit(http.HandlerFunc(s.streamForm)))

	// Unauthenticated on purpose: it says only that a figaro gateway is here,
	// which the handshake reveals anyway, and a health check that needs a
	// credential is a health check nothing will run.
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "figaro-gateway"})
	})

	s.http = &http.Server{
		Handler: mux,
		// No WriteTimeout: SSE streams and tunnels are long-lived by design,
		// and a write deadline would sever them on a schedule. ReadHeader
		// bounds the only phase that should ever be brief.
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	return s, nil
}

// tunnelWithPolicy attaches the method filter when a policy is configured.
// With no policy the tunnel stays a pure pump, which is the cheaper and more
// honest arrangement: nothing to get wrong.
func tunnelWithPolicy(t *Tunnel, s *Server, cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Policy != nil {
			id := identityFrom(r.Context())
			// A copy per connection: the filter closes over THIS caller's
			// identity, so one connection's policy can never be another's.
			scoped := *t
			scoped.Filter = &Filter{Check: func(method string) Decision {
				return cfg.Policy.Allow(id, method)
			}}
			scoped.ServeHTTP(w, r)
			return
		}
		t.ServeHTTP(w, r)
	})
}

// Serve binds and serves until ctx is done.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := s.listen()
	if err != nil {
		return err
	}
	s.ln = ln
	s.log.Info("gateway listening",
		"addr", s.cfg.Listen, "authn", s.cfg.Authn, "daemon", s.cfg.AngelusSocket)

	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sh)
		s.pool.closeAll()
	}()

	err = s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Addr is where the server actually bound, which for tcp://127.0.0.1:0 is
// only knowable after listening. Tests need it; humans read the log line.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

func (s *Server) listen() (net.Listener, error) {
	scheme, addr, err := splitListen(s.cfg.Listen)
	if err != nil {
		return nil, err
	}
	switch scheme {
	case "unix":
		// A stale socket from a killed process is the common case, not a
		// conflict: bind fails with EADDRINUSE and the file is not a live
		// endpoint. Remove it, then bind, then narrow the mode BEFORE
		// anything can connect.
		if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
			return nil, err
		}
		_ = os.Remove(addr)
		ln, err := net.Listen("unix", addr)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(addr, 0o660); err != nil {
			ln.Close()
			return nil, err
		}
		return ln, nil
	case "tcp":
		return net.Listen("tcp", addr)
	default:
		return nil, fmt.Errorf("cannot listen on scheme %q", scheme)
	}
}

func splitListen(listen string) (scheme, addr string, err error) {
	switch {
	case strings.HasPrefix(listen, "unix://"):
		return "unix", strings.TrimPrefix(listen, "unix://"), nil
	case strings.HasPrefix(listen, "tcp://"):
		return "tcp", strings.TrimPrefix(listen, "tcp://"), nil
	case listen == "":
		return "", "", errors.New("no listen address")
	default:
		return "", "", fmt.Errorf("listen %q: want unix:///path or tcp://host:port", listen)
	}
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	// An empty or wildcard host binds every interface: emphatically not local.
	if host == "" || host == "0.0.0.0" || host == "::" || host == "*" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
