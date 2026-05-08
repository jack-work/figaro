package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/transport"
)

// runContext dumps the bound figaro's IR message log as JSON.
func runContext(loaded *config.Loaded) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	resp, err := acli.Resolve(ctx, ppid)
	if err != nil {
		die("resolve: %s", err)
	}
	if !resp.Found {
		die("no figaro bound to this shell")
	}

	figaroEP := transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
	fcli, err := figaro.DialClient(figaroEP, nil)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	ctxResp, err := fcli.Context(ctx)
	if err != nil {
		die("context: %s", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(ctxResp.Messages)
}

// runRehydrate re-runs the credo on the figaro currently bound to
// this shell, applying the diff to its chalkboard.system.* keys as a
// state-only tic. With --dry-run, the diff is printed but not applied.
func runRehydrate(loaded *config.Loaded) {
	dryRun := false
	for _, arg := range os.Args[2:] {
		switch arg {
		case "--dry-run", "-n":
			dryRun = true
		default:
			die("unknown flag: %s", arg)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	resp, err := acli.Resolve(ctx, ppid)
	if err != nil {
		die("resolve: %s", err)
	}
	if !resp.Found {
		die("no figaro bound to this shell")
	}

	figaroEP := transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
	fcli, err := figaro.DialClient(figaroEP, nil)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	rresp, err := fcli.ReloadConfig(ctx, dryRun)
	if err != nil {
		die("rehydrate: %s", err)
	}

	if len(rresp.SetKeys) == 0 && len(rresp.RemoveKeys) == 0 {
		fmt.Fprintln(os.Stderr, "rehydrate: no changes")
		return
	}
	verb := "applied"
	if dryRun {
		verb = "would apply"
	}
	fmt.Fprintf(os.Stderr, "rehydrate: %s set=%v remove=%v\n", verb, rresp.SetKeys, rresp.RemoveKeys)
}
