package cli

import (
	"context"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/transport"
)

// Session holds the resolved connections for a CLI command.
type Session struct {
	Loaded   *config.Loaded
	Angelus  *angelus.Client
	Figaro   *figaro.Client
	AriaID   string
	Endpoint transport.Endpoint
}

// WithAngelus connects to the angelus and calls fn.
func WithAngelus(loaded *config.Loaded, fn func(acli *angelus.Client) error) {
	ensureHush()
	ensureAngelus()
	ep := transport.UnixEndpoint(angelusSocketPath())
	acli, err := angelus.DialClient(ep)
	if err != nil {
		die("connect angelus: %s", err)
	}
	defer acli.Close()
	if err := fn(acli); err != nil {
		die("%s", err)
	}
}

// WithSession resolves the pid-bound figaro and calls fn.
func WithSession(loaded *config.Loaded, fn func(s *Session) error) {
	WithSessionFor(loaded, "", fn)
}

// WithSessionFor resolves the target aria (explicit id > pid binding)
// and calls fn. When explicitID is empty, behaves like WithSession.
func WithSessionFor(loaded *config.Loaded, explicitID string, fn func(s *Session) error) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var ariaID string
		var ep transport.Endpoint

		if explicitID != "" {
			resp, err := acli.Attach(ctx, explicitID)
			if err != nil {
				die("attach %q: %s", explicitID, err)
			}
			ariaID = explicitID
			ep = transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
			waitForSocket(ep.Address, 3*time.Second)
		} else {
			r, err := resolveBinding(ctx, acli, os.Getppid())
			if err != nil {
				return err
			}
			if !r.Found {
				die("no figaro bound to this shell (try: --id <id> or attend <id>)")
			}
			ariaID = r.FigaroID
			ep = transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
		}

		fcli, err := figaro.DialClient(ep, nil)
		if err != nil {
			return err
		}
		defer fcli.Close()

		return fn(&Session{
			Loaded:   loaded,
			Angelus:  acli,
			Figaro:   fcli,
			AriaID:   ariaID,
			Endpoint: ep,
		})
	})
}
