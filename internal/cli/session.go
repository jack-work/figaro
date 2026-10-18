package cli

import (
	"context"
	"github.com/jack-work/figaro/sdk"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

type Session struct {
	Loaded  *config.Loaded
	Angelus *sdk.Angelus
	Figaro  *sdk.Aria
	AriaID  string
	Addr    sdk.Addr
}

func WithAngelus(loaded *config.Loaded, fn func(acli *sdk.Angelus) error) {
	ensureAngelus()
	acli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		die("connect angelus: %s", err)
	}
	defer acli.Close()
	if err := fn(acli); err != nil {
		die("%s", err)
	}
}

// WithAngelusIfRunning starts no daemon and unlocks no vault.
func WithAngelusIfRunning(loaded *config.Loaded, fn func(acli *sdk.Angelus) error) {
	acli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		die("angelus is not running")
	}
	defer acli.Close()
	if err := fn(acli); err != nil {
		die("%s", err)
	}
}

// WithSessionFor resolves explicit id, else the pid binding.
func WithSessionFor(loaded *config.Loaded, explicitID string, fn func(s *Session) error) {
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var ariaID string
		var ep sdk.Addr

		if explicitID != "" {
			resp, err := acli.Attach(ctx, explicitID)
			if err != nil {
				die("attach %q: %s", explicitID, err)
			}
			ariaID = resp.FigaroID
		} else {
			r, err := resolveBinding(ctx, acli, shellPID)
			if err != nil {
				return err
			}
			if !r.Found {
				die("no figaro bound to this shell (try: --id <id> or attend <id>)")
			}
			ariaID = r.FigaroID
		}
		ep = ariaAddr(ariaID)

		fcli, err := sdk.DialAria(ep, nil)
		if err != nil {
			return err
		}
		defer fcli.Close()

		return fn(&Session{
			Loaded:  loaded,
			Angelus: acli,
			Figaro:  fcli,
			AriaID:  ariaID,
			Addr:    ep,
		})
	})
}
