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
	"strings"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/gateway"
)

// defaultGatewaySocket sits beside the angelus socket, in the same runtime
// directory and under the same 0700 protection.
func defaultGatewaySocket() string {
	return filepath.Join(angelusRuntimeDir(), "gateway.sock")
}

// ServeOpts is what the verb collected from the command line.
type ServeOpts struct {
	Listen        string
	Authn         string
	DoorkeyFile   string
	Origins       []string
	RequireGroups []string
	Hosts         []string
	TLSDone       bool
	MaxConnAge    time.Duration
}

func runServe(loaded *config.Loaded, o ServeOpts) error {
	listen := o.Listen
	if listen == "" {
		listen = "unix://" + defaultGatewaySocket()
	}

	// An uncapped tunnel on a NETWORK bind is an unbounded authorization:
	// forward_auth runs on the upgrade only, so the socket outlives the
	// session that opened it. Eight hours is a working day, and a re-upgrade
	// is a re-authorization. A unix socket needs no cap -- reaching it
	// already required being the right uid.
	if o.MaxConnAge == 0 && strings.HasPrefix(listen, "tcp://") {
		o.MaxConnAge = 8 * time.Hour
	}

	// THE SECRET COMES FROM A FILE, never a flag. An argument is visible in
	// /proc, in a shell history, and in the process table of every other
	// user on the box; a file is a path plus a mode. It is also what
	// systemd LoadCredential hands you, which is how this arrives in
	// production.
	var doorkey string
	if o.DoorkeyFile != "" {
		b, err := os.ReadFile(o.DoorkeyFile)
		if err != nil {
			return fmt.Errorf("doorkey file: %w", err)
		}
		doorkey = strings.TrimSpace(string(b))
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
		Authn:         gateway.Authn(o.Authn),
		Doorkey:       doorkey,
		Origins:       o.Origins,
		RequireGroups: o.RequireGroups,
		Hosts:         o.Hosts,
		TLSTerminated: o.TLSDone,
		MaxConnAge:    o.MaxConnAge,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stderrw, "figaro gateway on %s -> %s\n", listen, sock)
	if len(o.Origins) == 0 {
		// Say it out loud. An empty allowlist is the SAFE default, but it is
		// also the one that makes a browser client fail with a handshake
		// error that says nothing useful, so the operator should hear about
		// it here rather than discover it in devtools.
		fmt.Fprintf(stderrw, "no browser origins allowed (--origin to add one)\n")
	}
	return srv.Serve(ctx)
}
