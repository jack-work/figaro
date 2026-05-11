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
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		r, err := acli.Resolve(ctx, os.Getppid())
		if err != nil {
			return err
		}
		if !r.Found {
			die("no figaro bound to this shell")
		}

		ep := transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
		fcli, err := figaro.DialClient(ep, nil)
		if err != nil {
			return err
		}
		defer fcli.Close()

		return fn(&Session{
			Loaded:   loaded,
			Angelus:  acli,
			Figaro:   fcli,
			AriaID:   r.FigaroID,
			Endpoint: ep,
		})
	})
}
