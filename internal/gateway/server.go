package gateway

// THE SERVER: minimal first increment.
//
// A UNIX SOCKET AND THE TUNNEL FACE, AND NOTHING ELSE. That is deliberate,
// and it is the smallest thing that is defensibly safe:
//
//   - A unix socket is exactly the trust model figaro already has. The
//     angelus socket is 0600 and its security argument is "you had to be me
//     to reach it". This door inherits that argument unchanged.
//   - No TCP listener means no network surface, so the whole family of
//     findings about loopback detection, DNS rebinding, X-Forwarded-For and
//     header smuggling cannot apply yet. They are not solved; they are not
//     REACHABLE, which is a different and much stronger claim.
//   - Caddy proxies to a unix socket happily, so this is deployable on spain
//     without figaro ever opening a port itself.
//
// TCP, the form face, and the peer mesh land on top of this once the
// refusal table (plans/http-gateway.md §4 C5) is built and tested.

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
	// Listen is a URI. Only unix:///path is accepted in this increment; a
	// tcp:// address is REFUSED rather than quietly downgraded, because the
	// authenticators that would make one safe are not written yet.
	Listen string
	// AngelusSocket is the daemon this gateway fronts.
	AngelusSocket string
	// Origins that may open a browser connection. Empty refuses every
	// browser origin, which is the default and the safe answer: CORS does
	// not apply to WebSockets, so a page carrying an ambient session cookie
	// could otherwise open a tunnel and speak raw JSON-RPC.
	Origins []string
	Log     *slog.Logger
}

// Check enforces what this increment will and will not serve.
func (c Config) Check() error {
	scheme, addr, err := splitListen(c.Listen)
	if err != nil {
		return err
	}
	if scheme != "unix" {
		return fmt.Errorf(
			"refusing to listen on %s: this figaro serves the gateway on a unix socket only.\n"+
				"A TCP listener needs an authenticator and a bind-address refusal table that are "+
				"not built yet; putting a reverse proxy in front of a unix socket is the supported "+
				"way to reach this from a network", c.Listen)
	}
	if addr == "" {
		return errors.New("listen: unix:// needs a path")
	}
	if c.AngelusSocket == "" {
		return errors.New("no angelus socket to front")
	}
	return nil
}

// Server is the gateway.
type Server struct {
	cfg  Config
	http *http.Server
	ln   net.Listener
	log  *slog.Logger
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
	s := &Server{cfg: cfg, log: log}

	mux := http.NewServeMux()
	mux.Handle("GET /v1/socket", &Tunnel{
		Dial:    UnixDialer(cfg.AngelusSocket),
		Origins: cfg.Origins,
		Log:     log,
	})
	// Unauthenticated on purpose: it says only that a figaro gateway is
	// here, which the handshake reveals anyway, and a health check that
	// needs a credential is a health check nothing will run.
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"figaro-gateway"}` + "\n"))
	})

	s.http = &http.Server{
		Handler: mux,
		// No WriteTimeout: a tunnel is long-lived by design and a write
		// deadline would sever it on a schedule. ReadHeader bounds the only
		// phase that should ever be brief.
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	return s, nil
}

// Serve binds and serves until ctx is done.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := s.listen()
	if err != nil {
		return err
	}
	s.ln = ln
	s.log.Info("gateway listening", "addr", s.cfg.Listen, "daemon", s.cfg.AngelusSocket)

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

// Addr is where the server actually bound. Tests need it.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

func (s *Server) listen() (net.Listener, error) {
	_, addr, err := splitListen(s.cfg.Listen)
	if err != nil {
		return nil, err
	}
	// The directory carries the real protection: 0700 means only this user
	// can even reach the socket inode. The socket mode is belt to that brace,
	// and both are set BEFORE anything can connect.
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
	if err := os.Chmod(addr, 0o600); err != nil {
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
		return "", "", fmt.Errorf("listen %q: want unix:///path", listen)
	}
}
