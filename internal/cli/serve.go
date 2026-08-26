package cli

// `figaro serve` -- expose this peer over HTTP.
//
// The verb is deliberately not called `gateway`: what it does is make an
// angelus REACHABLE, and in the architecture this is the first step of
// (plans/http-gateway.md) there is no server and no client, only peers that
// may own arias. Serving is a property a node takes on, not a role it is.

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/gateway"
)

// defaultGatewaySocket sits beside the angelus socket, in the same runtime
// directory and under the same 0700 protection.
func defaultGatewaySocket() string {
	return filepath.Join(angelusRuntimeDir(), "gateway.sock")
}

func runServe(loaded *config.Loaded, listen string, origins []string) error {
	if listen == "" {
		listen = "unix://" + defaultGatewaySocket()
	}

	// The gateway fronts a LOCAL angelus, so one must be running. We do not
	// start it: ensureAngelus detaches a grandchild, which under systemd
	// Type=exec escapes the unit's lifecycle and leaves an orphan the
	// supervisor cannot see. On a workstation the daemon is already up
	// because something spoke to it; under systemd it is a separate unit
	// with BindsTo=.
	sock := angelusSocketPath()
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf(
			"no angelus socket at %s.\n"+
				"`figaro serve` fronts a running daemon and will not start one: "+
				"run any figaro command first, or start `figaro --angelus` as its own unit", sock)
	}

	srv, err := gateway.New(gateway.Config{
		Listen:        listen,
		AngelusSocket: sock,
		Origins:       origins,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stderrw, "figaro gateway on %s -> %s\n", listen, sock)
	if len(origins) == 0 {
		// Say it out loud. An empty allowlist is the SAFE default, but it is
		// also the one that makes a browser client fail with a handshake
		// error that says nothing useful, so the operator should hear about
		// it here rather than discover it in devtools.
		fmt.Fprintf(stderrw, "no browser origins allowed (--origin to add one)\n")
	}
	return srv.Serve(ctx)
}
