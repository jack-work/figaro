// Package cli — first-run setup wizard.
//
// The wizard fires when the user runs any prompt-producing command
// before figaro has enough configuration to satisfy it. It runs in
// three stations, each independent and skippable:
//
//   1. Hush identity (owned by hush's `managed` package; we just call
//      EnsureReady and let it prompt for a passphrase + persist to the
//      OS keyring). Invisible if the identity already exists.
//
//   2. Provider + credentials. Picks a provider/mode from a numbered
//      menu, then runs that mode's credential setup (OAuth flow or
//      API-key prompt). Stores the result through hush so secrets
//      never touch disk in plaintext.
//
//   3. Default loadout. Scaffolds a minimal `loadouts/default.toml`
//      bound to the provider chosen in (2) and points config.toml's
//      default_loadout at it. So `fig "..."` works after this returns.
//
// Triggers: angelus emits a typed JSON-RPC error
// (ErrNoDefaultLoadout / ErrNoProvider). createWithFirstRun catches
// it, drives the wizard, retries the underlying call.
//
// Non-TTY callers get a clear error directing them to set things up
// interactively; we never auto-mutate config silently when there's no
// human at the keyboard.
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
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/jkrpc"
)

// providerChoice describes one entry in the first-run menu. A single
// underlying provider (e.g. "anthropic") can appear multiple times
// here with different modes (OAuth vs API key) — the menu shows the
// human-facing options; the underlying provider name is what gets
// written into the loadout.
type providerChoice struct {
	label    string // shown in the menu
	provider string // value for loadout's [system].provider
	mode     string // "oauth" | "apikey"
	hint     string // short description after the label
}

// catalog is the menu shown for each underlying provider. Ordering
// matters: first entry is "recommended" by virtue of position. Add
// new providers here and they appear in the wizard automatically.
//
// Today there's only one underlying provider (anthropic) with two
// modes. When OpenAI/etc. land, append two more entries here.
var providerCatalog = []providerChoice{
	{
		label:    "Anthropic (Claude.ai login)",
		provider: "anthropic",
		mode:     "oauth",
		hint:     "recommended — no API key to manage",
	},
	{
		label:    "Anthropic (API key)",
		provider: "anthropic",
		mode:     "apikey",
		hint:     "paste a key from console.anthropic.com",
	},
}

// catalogFor filters the catalog to entries whose underlying
// provider appears in the available list. Lets us hide options the
// build doesn't actually support.
func catalogFor(available []string) []providerChoice {
	if len(available) == 0 {
		return providerCatalog
	}
	allow := map[string]bool{}
	for _, p := range available {
		allow[p] = true
	}
	out := make([]providerChoice, 0, len(providerCatalog))
	for _, c := range providerCatalog {
		if allow[c.provider] {
			out = append(out, c)
		}
	}
	return out
}

// createFn is the shape of the `acli.Create*` family. We accept a
// closure so the same retry wrapper covers Create, CreateWithID, and
// CreateEphemeral without per-call duplication.
type createFn func() (*rpc.CreateResponse, error)

// createWithFirstRun invokes fn once. On a typed first-run error,
// drives the wizard and retries.
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
		if werr := runWizard(loaded, data); werr != nil {
			return nil, werr
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

// runWizard orchestrates the three-station first-run flow. Hush
// (Station 1) was already handled by ensureHush before any RPC went
// out, so this drives Stations 2 (provider + credentials) and 3
// (default loadout).
func runWizard(loaded *config.Loaded, data rpc.ErrorData) error {
	if !isStdinTTY() {
		return fmt.Errorf(
			"figaro needs initial setup but stdin is not a TTY.\n"+
				"  Run an interactive `figaro` invocation once to walk through setup,\n"+
				"  or configure manually:\n"+
				"    - set default_loadout in %s\n"+
				"    - create %s with `[system]\\nprovider = \"<name>\"`\n"+
				"    - run `figaro login <provider>` to add credentials",
			loaded.ConfigPath, loaded.LoadoutPath("default"))
	}

	options := catalogFor(data.AvailableProviders)
	if len(options) == 0 {
		return fmt.Errorf("first-run: no providers available to choose from")
	}

	printWelcome(loaded)

	// --- Station 2: provider + credentials -------------------------------
	printStep(2, 3, "Provider")
	fmt.Fprintln(os.Stderr, dim("     Where should your prompts go? You can add more later with `figaro login`."))
	fmt.Fprintln(os.Stderr)

	chosen, err := pickFromMenu(options)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr)

	if err := setupCredentialsFor(loaded, chosen); err != nil {
		return fmt.Errorf("provider setup: %w", err)
	}
	fmt.Fprintln(os.Stderr)

	// --- Station 3: default loadout --------------------------------------
	printStep(3, 3, "Default loadout")
	fmt.Fprintln(os.Stderr, dim("     A loadout bundles a provider + model so `fig` knows what to do"))
	fmt.Fprintln(os.Stderr, dim("     when you don't pass flags. We'll make one for you and set it as default."))
	fmt.Fprintln(os.Stderr)

	loadoutName, err := createDefaultLoadout(loaded, chosen.provider)
	if err != nil {
		return fmt.Errorf("loadout: %w", err)
	}
	fmt.Fprintln(os.Stderr, "  "+green("✓")+" wrote loadout "+cyan(loadoutName)+" → provider="+cyan(chosen.provider))
	fmt.Fprintln(os.Stderr, "  "+green("✓")+" set as default_loadout in "+dim(loaded.ConfigPath))
	fmt.Fprintln(os.Stderr)

	printDone()
	return nil
}

// pickFromMenu prints a numbered list and returns the chosen entry.
// The first entry is the default (Enter selects it). Reads from
// stdin via bufio (line-buffered).
func pickFromMenu(options []providerChoice) (providerChoice, error) {
	for i, opt := range options {
		num := fmt.Sprintf("[%d]", i+1)
		hint := ""
		if opt.hint != "" {
			hint = "   " + dim(opt.hint)
		}
		fmt.Fprintf(os.Stderr, "       %s  %s%s\n", cyan(num), opt.label, hint)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "       Pick [1]: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return providerChoice{}, fmt.Errorf("read choice: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return options[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(options) {
		return providerChoice{}, fmt.Errorf("invalid choice %q (pick 1-%d)", line, len(options))
	}
	return options[n-1], nil
}

// setupCredentialsFor runs the credential acquisition flow for the
// chosen provider+mode. OAuth opens a browser (or prints a URL),
// awaits a callback or pasted code, and persists tokens via hush.
// API-key mode prompts (no echo), encrypts via hush, writes to
// providers/<name>.toml.
func setupCredentialsFor(loaded *config.Loaded, choice providerChoice) error {
	switch choice.mode {
	case "oauth":
		cfg, ok := oauthConfigFor(choice.provider)
		if !ok {
			return fmt.Errorf("provider %q has no OAuth config", choice.provider)
		}
		return runOAuthInline(choice.provider, cfg)
	case "apikey":
		return runAPIKeyInline(loaded, choice.provider)
	default:
		return fmt.Errorf("unknown setup mode %q", choice.mode)
	}
}

// runOAuthInline calls auth.Login, which drives the PKCE handshake
// and persists the result through hush. Errors propagate.
func runOAuthInline(providerName string, cfg auth.OAuthConfig) error {
	h := mustHush()
	hushClient := h.Client()
	return auth.Login(hushClient, cfg, func() (string, error) {
		fmt.Fprint(os.Stderr, "       Paste the code here: ")
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		return strings.TrimSpace(line), err
	})
}

// runAPIKeyInline prompts for a key (no echo), encrypts it via hush,
// and writes it as `api_key = "AGE-ENC[...]"` in providers/<name>.toml.
func runAPIKeyInline(loaded *config.Loaded, providerName string) error {
	fmt.Fprintf(os.Stderr, "       API key: ")
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read api key: %w", err)
	}
	if len(key) == 0 {
		return fmt.Errorf("empty api key")
	}

	h := mustHush()
	encrypted, err := h.Client().Encrypt(map[string]string{"api_key": string(key)})
	// wipe the in-memory plaintext immediately
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return fmt.Errorf("encrypt api key via hush: %w", err)
	}
	enc, ok := encrypted["api_key"]
	if !ok || enc == "" {
		return fmt.Errorf("hush returned no ciphertext for api_key")
	}

	pa := config.ProviderAuth{APIKey: enc}
	path := loaded.ProviderAuthPath(providerName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(pa); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintln(os.Stderr, "  "+green("✓")+" stored encrypted api key → "+dim(path))
	return nil
}

// createDefaultLoadout writes loadouts/default.toml (or
// default-<provider>.toml if the former exists) and points
// config.toml's default_loadout at it. Returns the loadout name.
func createDefaultLoadout(loaded *config.Loaded, providerName string) (string, error) {
	name := "default"
	if _, err := os.Stat(loaded.LoadoutPath(name)); err == nil {
		name = "default-" + providerName
	}
	if err := writeStarterLoadout(loaded.LoadoutPath(name), providerName); err != nil {
		return "", fmt.Errorf("scaffold loadout: %w", err)
	}
	if err := patchDefaultLoadout(loaded.ConfigPath, name); err != nil {
		return "", fmt.Errorf("patch config.toml: %w", err)
	}
	loaded.Config.DefaultLoadout = name
	return name, nil
}

// writeStarterLoadout writes a minimal loadout file. Parent
// directories are created with 0700.
func writeStarterLoadout(path, providerName string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body := fmt.Sprintf(`# Scaffolded by figaro first-run setup.
# Edit to taste; see docs/loadouts for the schema.

[system]
provider = %q
`, providerName)
	return os.WriteFile(path, []byte(body), 0600)
}

// patchDefaultLoadout rewrites config.toml in place to set
// default_loadout = "<name>". Existing keys are preserved.
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

// --- pretty bits -----------------------------------------------------------
//
// Routed through internal/term so NO_COLOR / non-TTY are respected.

func dim(s string) string   { return term.Dim(s) }
func cyan(s string) string  { return term.Cyan(s) }
func green(s string) string { return term.Green(s) }

func printWelcome(loaded *config.Loaded) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, cyan("  ▌ figaro setup")+dim("  ·  one minute, three steps  ·  config: "+loaded.ConfigPath))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, dim("       (Step 1/3 — secrets vault — already done by hush.)"))
	fmt.Fprintln(os.Stderr)
}

func printStep(n, of int, title string) {
	fmt.Fprintln(os.Stderr, cyan(fmt.Sprintf("  %d/%d", n, of))+"  "+title)
}

func printDone() {
	fmt.Fprintln(os.Stderr, "  "+green("─────────────────────────────────────────────────────────────────────────"))
	fmt.Fprintln(os.Stderr, "  All set. Running your prompt now.")
	fmt.Fprintln(os.Stderr)
}

// --- compile-time wiring ---------------------------------------------------

// Compile-time check: angelus.Client.Create matches createFn shape
// when bound (modulo context — caller supplies one).
var _ = func(acli *angelus.Client, ctx context.Context) createFn {
	return func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, "", nil)
	}
}

// isStdinTTY returns true when stdin is attached to a terminal.
func isStdinTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
