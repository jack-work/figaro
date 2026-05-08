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

// runSearch reads a registered durable derivation off disk and
// prints it. Default: pretty-printed JSON. With -json: raw bytes.
//
// Usage: figaro -s <alias> [-json]
func runSearch(loaded *config.Loaded, args []string) {
	if len(args) == 0 {
		listAliases()
		return
	}
	alias := args[0]
	rawJSON := false
	for _, a := range args[1:] {
		if a == "-json" || a == "--json" {
			rawJSON = true
		}
	}

	reg, ok := figaro.LookupRegistration(alias)
	if !ok {
		fmt.Fprintf(os.Stderr, "no derivation registered with alias %q\n", alias)
		listAliases()
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	r, err := acli.Resolve(ctx, os.Getppid())
	if err != nil {
		die("resolve: %s", err)
	}
	if !r.Found {
		die("no figaro bound to this shell")
	}

	ep := transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
	fcli, err := figaro.DialClient(ep, nil)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	cbResp, err := fcli.Chalkboard(ctx)
	if err != nil {
		die("chalkboard: %s", err)
	}
	providerName := unquote(cbResp.Snapshot["system.provider"])

	backend, err := ariaBackend()
	if err != nil {
		die("aria backend: %s", err)
	}
	deps := figaro.DurDerivDeps{AriaID: r.FigaroID, ProviderName: providerName}
	path := figaro.DerivationFilePath(backend, deps, reg)
	if path == "" {
		die("derivation %q has no on-disk path (backend doesn't support file derivations)", alias)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		die("read %s: %s", path, err)
	}

	if rawJSON {
		os.Stdout.Write(data)
		return
	}
	var any interface{}
	if err := json.Unmarshal(data, &any); err != nil {
		os.Stdout.Write(data)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(any)
}

func listAliases() {
	regs := figaro.Registrations()
	fmt.Fprintln(os.Stderr, "available derivations:")
	for _, r := range regs {
		fmt.Fprintf(os.Stderr, "  %s\n", r.Alias)
	}
}

func unquote(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
