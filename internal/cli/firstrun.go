// Package cli — first-run flow.
//
// When `figaro create` fails with a typed first-run error
// (rpc.ErrNoDefaultLoadout / ErrNoProvider), this module drives the
// recovery: it prompts the user to pick a provider from the angelus's
// AvailableProviders list, writes a trivial loadout file containing
// `[system]\nprovider = "<chosen>"`, and patches the top-level
// config.toml so default_loadout points at it. The caller retries
// the create request after this returns.
//
// Non-TTY callers get a clear error directing them to set
// default_loadout manually; we never auto-mutate config silently
// when there's no human at the keyboard.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/jkrpc"
)

// createFn is the shape of the `acli.Create*` family. We accept a
// closure so the same retry wrapper covers Create, CreateWithID, and
// CreateEphemeral without per-call duplication.
type createFn func() (*rpc.CreateResponse, error)

// createWithFirstRun invokes fn once. On a typed first-run error,
// drives the recovery flow (prompt + scaffold) and retries.
func createWithFirstRun(ctx context.Context, loaded *config.Loaded, fn createFn) (*rpc.CreateResponse, error) {
	resp, err := fn()
	if err == nil {
		return resp, nil
	}
	data, code, ok := decodeTypedError(err)
	if !ok {
		return nil, err
	}
	switch code {
	case rpc.ErrNoDefaultLoadout, rpc.ErrNoProvider:
		if err := handleFirstRun(loaded, data); err != nil {
			return nil, err
		}
		return fn()
	default:
		return nil, err
	}
}

// decodeTypedError extracts the (Data, Code) pair from a typed
// JSON-RPC error. Returns ok=false for any other error type.
func decodeTypedError(err error) (rpc.ErrorData, int, bool) {
	var jerr *jkrpc.Error
	if !errors.As(err, &jerr) {
		return rpc.ErrorData{}, 0, false
	}
	var data rpc.ErrorData
	if len(jerr.Data) > 0 {
		_ = json.Unmarshal(jerr.Data, &data)
	}
	return data, jerr.Code, true
}

// handleFirstRun prompts the user, scaffolds a loadout, and updates
// config.toml. Errors out (preserving the original RPC failure for
// the caller) when stdin is not a TTY.
func handleFirstRun(loaded *config.Loaded, data rpc.ErrorData) error {
	providers := data.AvailableProviders
	if len(providers) == 0 {
		providers = KnownProviders
	}
	if len(providers) == 0 {
		return fmt.Errorf("first-run: no providers available to choose from")
	}
	if !isStdinTTY() {
		return fmt.Errorf(
			"no default loadout configured. Set default_loadout in %s "+
				"or create %s with `[system]\\nprovider = \"<name>\"`.",
			loaded.ConfigPath, loaded.LoadoutPath("default"))
	}

	chosen, err := pickProvider(providers)
	if err != nil {
		return err
	}

	// Loadout name defaults to "default". If a file already exists at
	// that path, fall back to "default-<provider>" to avoid clobbering.
	loadoutName := "default"
	if _, err := os.Stat(loaded.LoadoutPath(loadoutName)); err == nil {
		loadoutName = "default-" + chosen
	}

	if err := writeStarterLoadout(loaded.LoadoutPath(loadoutName), chosen); err != nil {
		return fmt.Errorf("scaffold loadout: %w", err)
	}
	if err := patchDefaultLoadout(loaded.ConfigPath, loadoutName); err != nil {
		return fmt.Errorf("patch config.toml: %w", err)
	}
	loaded.Config.DefaultLoadout = loadoutName // keep in-process state coherent

	fmt.Fprintf(os.Stderr, "Wrote starter loadout %s (provider=%s).\n",
		loaded.LoadoutPath(loadoutName), chosen)
	fmt.Fprintf(os.Stderr, "Set default_loadout=%q in %s.\n",
		loadoutName, loaded.ConfigPath)
	return nil
}

// pickProvider prompts the user to pick from a numbered menu. When a
// single provider is available, it's chosen without prompting.
func pickProvider(providers []string) (string, error) {
	if len(providers) == 1 {
		fmt.Fprintf(os.Stderr, "Using provider %q (only option available).\n", providers[0])
		return providers[0], nil
	}
	fmt.Fprintln(os.Stderr, "No default loadout configured. Pick a provider:")
	for i, p := range providers {
		fmt.Fprintf(os.Stderr, "  [%d] %s\n", i+1, p)
	}
	fmt.Fprint(os.Stderr, "Choice (1-", len(providers), "): ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read choice: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(providers) {
		return "", fmt.Errorf("invalid choice %q", strings.TrimSpace(line))
	}
	return providers[n-1], nil
}

// writeStarterLoadout writes a minimal loadout file:
//
//	[system]
//	provider = "<chosen>"
//
// Parent directories are created with 0700.
func writeStarterLoadout(path, providerName string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body := fmt.Sprintf("# Scaffolded by `figaro` first-run.\n# Edit to taste; see docs/loadouts.\n\n[system]\nprovider = %q\n", providerName)
	return os.WriteFile(path, []byte(body), 0600)
}

// patchDefaultLoadout rewrites config.toml in place to set
// default_loadout = "<name>". Existing keys are preserved.
// Creates the file if absent.
func patchDefaultLoadout(configPath, loadoutName string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(configPath); err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse existing config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	raw["default_loadout"] = loadoutName

	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(raw)
}

// isStdinTTY returns true when stdin is attached to a terminal.
func isStdinTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// Compile-time check: angelus.Client.Create matches createFn shape
// when bound (modulo context — caller supplies one).
var _ = func(acli *angelus.Client, ctx context.Context) createFn {
	return func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, "", nil)
	}
}
