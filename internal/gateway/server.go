package gateway

// THE SERVER: one listener, the tunnel face, and one admission decision.
//
// The listener may be a unix socket or a TCP port. Which combinations of
// (authenticator, exposure) are legal lives in refusal.go, and is decided
// AFTER binding -- see classify() for why a config string cannot answer it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Config is everything `figaro serve` decides.
type Config struct {
	// Listen is a URI: unix:///path or tcp://host:port.
	Listen string
	// AngelusSocket is the daemon this gateway fronts.
	AngelusSocket string
	// Authn is the ordered list of authenticators. More than one is the
	// normal case behind Caddy, which forward-auths only requests without a
	// bearer -- see anyOf. A caller picks which credential to present, so
	// the exposure check below demands that EVERY entry be legal.
	Authn []Authn
	// Doorkey is the shared secret when Authn is AuthnDoorkey.
	Doorkey string
	// Origins that may open a BROWSER connection. Empty refuses every
	// browser origin, which is the default and the safe answer: CORS does
	// not apply to WebSocket upgrades, so a page carrying an ambient session
	// cookie could otherwise open a tunnel and speak raw JSON-RPC.
	Origins []string
	// RequireGroups admits only callers holding one of these groups. Empty
	// admits anyone the authenticator accepted, which is correct for a
	// doorkey (holding the key IS the authorization) and dangerous behind a
	// proxy that authenticates a whole directory.
	RequireGroups []string
	// Policy gates methods once an identity is known. Nil allows every
	// method, which is a decision the caller states rather than inherits.
	Policy MethodPolicy
	// Hosts is the Host: header allowlist. Empty accepts any Host, which is
	// only safe on a unix socket; a loopback TCP bind wants this set, or a
	// DNS rebinding attack reaches it from a browser on the same machine.
	Hosts []string
	// TLSTerminated asserts that something in front terminates TLS, which is
	// what makes a doorkey on an exposed address defensible. It is a promise
	// the operator makes; figaro cannot verify it, so it is named honestly
	// rather than inferred.
	TLSTerminated bool
	// MaxConnAge bounds one tunnel's lifetime. It exists because forward
	// authentication happens on the UPGRADE REQUEST ONLY: once a WebSocket
	// is established no frame is ever re-authorized, so a revoked operator
	// keeps a live shell until the socket drops. Capping the connection
	// forces a re-upgrade, and a re-upgrade is re-authorized. Zero means no
	// cap, which is only honest on a unix socket.
	MaxConnAge time.Duration
	// Keepalive is the interval between server pings. Cloudflare reaps an
	// idle WebSocket at roughly 100s and it is not configurable on a tunnel,
	// so silence has to be filled or the connection dies exactly when the
	// operator stops typing -- which works in every test and fails in use.
	Keepalive time.Duration
	Log       *slog.Logger
}

// Defaults fills the unset fields that have a safe answer.
func (c Config) Defaults() Config {
	if len(c.Authn) == 0 {
		c.Authn = []Authn{AuthnNone}
	}
	if c.Keepalive == 0 {
		c.Keepalive = 30 * time.Second
	}
	return c
}

// Check is the part of validation that does NOT need a bound socket: the
// address is well-formed, the authenticator is known, and its secret is
// present. Exposure is judged later, by admit(), against the real listener.
func (c Config) Check() error {
	_, addr, err := splitListen(c.Listen)
	if err != nil {
		return err
	}
	if addr == "" {
		return fmt.Errorf("listen %q: no address", c.Listen)
	}
	if c.AngelusSocket == "" {
		return errors.New("no angelus socket to front")
	}
	for _, a := range c.Authn {
		if err := admitKnown(a); err != nil {
			return err
		}
	}
	if slices.Contains(c.Authn, AuthnDoorkey) {
		if c.Doorkey == "" {
			return errors.New("authn=doorkey needs a secret: give --doorkey-file a path")
		}
		if len(c.Doorkey) < 16 {
			return errors.New("doorkey is shorter than 16 bytes: use real randomness")
		}
		// The plaintext question is answered by admit() against the BOUND
		// address, not here: "tcp://" says nothing about whether the socket
		// is routable, and loopback plaintext is fine.
	}
	return nil
}

func authnNames(as []Authn) string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = string(a)
	}
	return strings.Join(out, ",")
}

func admitKnown(a Authn) error {
	for _, k := range KnownAuthn {
		if a == k {
			return nil
		}
	}
	names := make([]string, len(KnownAuthn))
	for i, k := range KnownAuthn {
		names[i] = string(k)
	}
	return fmt.Errorf("unknown authn %q: want one of %s", a, strings.Join(names, ", "))
}

// Server is the gateway.
type Server struct {
	cfg  Config
	http *http.Server
	ln   net.Listener
	log  *slog.Logger
}

// New builds a server. It does NOT bind; Serve does, because the exposure
// check needs a real listener.
func New(cfg Config) (*Server, error) {
	cfg = cfg.Defaults()
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
	s := &Server{cfg: cfg, log: log}

	mux := http.NewServeMux()
	mux.Handle("GET /v1/socket", &Tunnel{
		Dial:      UnixDialer(cfg.AngelusSocket),
		Origins:   cfg.Origins,
		MaxAge:    cfg.MaxConnAge,
		Keepalive: cfg.Keepalive,
		Authn:     authn,
		Policy:    cfg.Policy,
		Log:       log,
	})
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"figaro-gateway"}` + "\n"))
	})

	s.http = &http.Server{
		Handler: s.checkHost(mux),
		// No WriteTimeout: a tunnel is long-lived by design and a write
		// deadline would sever it on a schedule. MaxConnAge is the bound
		// that belongs here, and it is applied per-tunnel.
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	return s, nil
}

// checkHost enforces the Host allowlist. Without it, a page in a browser on
// the same machine can point a hostname it controls at 127.0.0.1 and reach a
// loopback-bound gateway -- DNS rebinding, against which binding loopback is
// no defence at all.
func (s *Server) checkHost(next http.Handler) http.Handler {
	if len(s.cfg.Hosts) == 0 {
		return next
	}
	allowed := make(map[string]bool, len(s.cfg.Hosts))
	for _, h := range s.cfg.Hosts {
		allowed[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !allowed[host] {
			http.Error(w, "unrecognized Host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve binds, judges the exposure it actually got, and serves until ctx is
// done. A listener that fails admit() is CLOSED, not served.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := s.listen()
	if err != nil {
		return err
	}

	// THE ORDER MATTERS. Bind, then interrogate the bound address, then
	// decide. Judging the config string instead would miss ":9090",
	// "0.0.0.0", "::ffff:127.0.0.1", and a hostname resolving off-box.
	r := classify(ln)
	// EVERY authenticator must be legal on this exposure, not merely one:
	// a caller chooses which credential to present, so the door is as
	// strong as its weakest option.
	for _, a := range s.cfg.Authn {
		if err := admit(a, r, s.cfg.TLSTerminated); err != nil {
			ln.Close()
			return fmt.Errorf("refusing to serve: %w", err)
		}
	}
	if r != reachUnix && len(s.cfg.Hosts) == 0 {
		s.log.Warn("no Host allowlist on a TCP listener: a browser can reach this by DNS rebinding",
			"addr", ln.Addr().String())
	}
	s.ln = ln

	s.log.Info("gateway listening",
		"addr", ln.Addr().String(), "exposure", r.String(),
		"authn", authnNames(s.cfg.Authn), "daemon", s.cfg.AngelusSocket,
		"max_conn_age", s.cfg.MaxConnAge, "keepalive", s.cfg.Keepalive)

	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sh)
	}()

	err = s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Addr is where the server actually bound.
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
	if scheme == "tcp" {
		return net.Listen("tcp", addr)
	}
	// The DIRECTORY carries the real protection: 0700 means only this user
	// can reach the socket inode at all. The socket mode is belt to that
	// brace, and both are set before anything can connect.
	if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
		return nil, err
	}
	// A stale socket from a killed process is the common case, not a
	// conflict: bind fails with EADDRINUSE and the file is not a live
	// endpoint.
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
