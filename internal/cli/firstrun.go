// Package cli: first-run setup wizard.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/tui"
	"github.com/jack-work/jkrpc"
)

// providerChoice describes one entry in the first-run menu. A single
// underlying provider (e.g. "anthropic") can appear multiple times
// here with different modes (OAuth vs API key): the menu shows the
// human-facing options; the underlying provider name is what gets
// written into the outfit.
type providerChoice struct {
	label    string // shown in the menu
	provider string // value for outfit's [system].provider
	hint     string // short description after the label
	setup    func(loaded *config.Loaded) error
}

func init() {
	if r := providerPkg.Lookup("copilot"); r != nil {
		r.Setup = runCopilotLogin
		r.Login = runCopilotLogin
	}
	if r := providerPkg.Lookup("anthropic"); r != nil {
		login := func(loaded *config.Loaded) error {
			return runOAuthInline("anthropic", auth.AnthropicOAuth)
		}
		r.Setup = login
		r.Login = login
	}
}

// catalog is the menu shown for each underlying provider. Ordering
// matters: first entry is "recommended" by virtue of position. Add
// new providers here and they appear in the wizard automatically.
var providerCatalog = []providerChoice{
	{
		label:    "GitHub Copilot (device code login)",
		provider: "copilot",
		hint:     "recommended: uses your Copilot subscription",
		setup:    runCopilotLogin,
	},
	{
		label:    "Anthropic (Claude.ai login)",
		provider: "anthropic",
		hint:     "no API key to manage",
		setup: func(loaded *config.Loaded) error {
			return runOAuthInline("anthropic", auth.AnthropicOAuth)
		},
	},
	{
		label:    "Anthropic (API key)",
		provider: "anthropic",
		hint:     "paste a key from console.anthropic.com",
		setup:    func(loaded *config.Loaded) error { return runAPIKeyInline(loaded, "anthropic") },
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
// closure so the same retry wrapper covers Create and CreateWithID.
type createFn func() (*rpc.CreateResponse, error)

// createWithFirstRun invokes fn once. On a typed first-run error, drives the
// wizard and retries.
func createWithFirstRun(ctx context.Context, loaded *config.Loaded, d dressing, fn createFn) (*rpc.CreateResponse, error) {
	resp, err := fn()
	if err == nil {
		return resp, nil
	}
	data, code, ok := decodeTypedError(err)
	if !ok {
		return nil, err
	}
	switch code {
	case rpc.ErrNoDefaultOutfit, rpc.ErrNoProvider:
		if !d.IsEmpty() {
			return nil, fmt.Errorf("-O %s sets no system.provider: add one to that outfit, or layer one that has it",
				d.label())
		}
		if werr := runWizard(ctx, loaded, data); werr != nil {
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
// (default outfit).
func runWizard(ctx context.Context, loaded *config.Loaded, data rpc.ErrorData) error {
	if !isStdinTTY() {
		return fmt.Errorf(
			"figaro needs initial setup but stdin is not a TTY.\n"+
				"  Run an interactive `figaro` invocation once to walk through setup,\n"+
				"  or configure manually:\n"+
				"    - set default_outfit in %s\n"+
				"    - create %s with `[system]\\nprovider = \"<name>\"`\n"+
				"    - run `figaro login <provider>` to add credentials",
			loaded.ConfigPath, loaded.OutfitPath("default"))
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

	chosen, err := pickFromMenu(loaded, options)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr)

	if err := setupCredentialsFor(loaded, chosen); err != nil {
		return fmt.Errorf("provider setup: %w", err)
	}
	fmt.Fprintln(os.Stderr)

	// --- Station 3: default outfit --------------------------------------
	printStep(3, 3, "Default outfit")
	fmt.Fprintln(os.Stderr, dim("     An outfit bundles a provider + model so `fig` knows what to do"))
	fmt.Fprintln(os.Stderr, dim("     when you don't pass flags. We'll make one for you and set it as default."))
	fmt.Fprintln(os.Stderr)

	// Try to fetch the live model list and let the user pick. If the
	// listing fails for any reason (transient API hiccup, OAuth token
	// not yet active, provider missing a /models endpoint), we silently
	// fall back to defaultModelFor so first-run never blocks on it.
	chosenModel := pickModelOrFallback(loaded, chosen.provider)

	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	var existing []string
	if list, lerr := acli.Outfits(ctx, ""); lerr == nil {
		existing = list.Names
	}
	outfitName, err := createDefaultOutfit(ctx, acli, existing, chosen.provider, chosenModel)
	if err != nil {
		return fmt.Errorf("outfit: %w", err)
	}
	fmt.Fprintln(os.Stderr, "  "+green("✓")+" wrote outfit "+cyan(outfitName)+" → provider="+cyan(chosen.provider))
	fmt.Fprintln(os.Stderr, "  "+green("✓")+" set as default_outfit in "+dim(loaded.ConfigPath))
	fmt.Fprintln(os.Stderr)

	printDone()
	return nil
}

// pickFromMenu uses the TUI picker when interactive mode is on (the
// default); falls back to a numbered prompt otherwise. The first
// option is the default (Enter / no input selects it).
func pickFromMenu(loaded *config.Loaded, options []providerChoice) (providerChoice, error) {
	if loaded != nil && !loaded.Interactive() {
		return pickFromMenuNumbered(options)
	}
	tuiOpts := make([]tui.ProviderOption, len(options))
	for i, o := range options {
		tuiOpts[i] = tui.ProviderOption{
			Key:   strconv.Itoa(i), // index round-trip; cleaner than label match
			Label: o.label,
			Hint:  o.hint,
		}
	}
	key, err := tui.PickProvider("Provider", tuiOpts)
	if err != nil {
		return providerChoice{}, err
	}
	idx, err := strconv.Atoi(key)
	if err != nil || idx < 0 || idx >= len(options) {
		return providerChoice{}, fmt.Errorf("invalid TUI selection %q", key)
	}
	return options[idx], nil
}

// pickFromMenuNumbered prints a numbered list and returns the chosen
// entry. The first entry is the default (Enter selects it). Used as
// the explicit-non-interactive fallback when interactive=false.
func pickFromMenuNumbered(options []providerChoice) (providerChoice, error) {
	for i, opt := range options {
		num := fmt.Sprintf("[%d]", i+1)
		hint := ""
		if opt.hint != "" {
			hint = "   " + dim(opt.hint)
		}
		fmt.Fprintf(os.Stderr, "       %s  %s%s\n", cyan(num), opt.label, hint)
	}
	fmt.Fprintln(os.Stderr)
	line, err := term.ReadLine("       Pick [1]: ")
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
	return choice.setup(loaded)
}

// runOAuthInline calls auth.Login, which drives the PKCE handshake
// and persists the result through hush. Errors propagate.
func runOAuthInline(providerName string, cfg auth.OAuthConfig) error {
	h := mustHush()
	hushClient := h.Client()
	return auth.Login(hushClient, cfg, func() (string, error) {
		// Login prints the (styled) paste prompt; we only read.
		line, err := term.ReadLine("")
		return strings.TrimSpace(line), err
	})
}

// runAPIKeyInline prompts for a key (no echo), encrypts it via hush,
// and writes it as `api_key = "AGE-ENC[...]"` in providers/<name>.toml.
// runCopilotLogin runs the device code flow and hands the GitHub token to
// hush under the copilot grant.
func runCopilotLogin(loaded *config.Loaded) error {
	line, err := term.ReadLine("       GitHub Enterprise domain (blank for github.com): ")
	if err != nil {
		return fmt.Errorf("read domain: %w", err)
	}
	domain := strings.TrimSpace(line)

	githubToken, err := auth.LoginCopilot(domain)
	if err != nil {
		return err
	}

	// Keep the domain on disk: it is not a secret, and it is what tells
	// the provider where to exchange and which host to talk to.
	if domain != "" {
		if err := writeCopilotDomain(loaded, domain); err != nil {
			return err
		}
	}

	reg := providerPkg.Lookup("copilot")
	if reg == nil || reg.ExchangeURL == nil {
		return fmt.Errorf("copilot provider is not registered")
	}
	tokenURL := reg.ExchangeURL(loaded)
	if tokenURL == "" {
		return fmt.Errorf("copilot is configured for direct mode; nothing to exchange")
	}

	h := mustHush()
	if err := h.Client().OAuthRegister(hush.OAuthRegisterRequest{
		Name:         "copilot",
		TokenURL:     tokenURL,
		Grant:        hush.GrantCopilot,
		RefreshToken: githubToken,
	}); err != nil {
		return fmt.Errorf("hand the GitHub token to hush: %w", err)
	}
	fmt.Fprintln(os.Stderr, "  "+green("\u2713")+" hush holds the Copilot credential and minted a session "+dim("(hush oauth list)"))
	return nil
}

// writeCopilotDomain persists the enterprise domain (plaintext, not a
// secret) without disturbing anything else in the file.
func writeCopilotDomain(loaded *config.Loaded, domain string) error {
	cfg := struct {
		EnterpriseDomain string `toml:"enterprise_domain"`
	}{EnterpriseDomain: domain}
	path := loaded.ProviderAuthPath("copilot")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

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

// createDefaultOutfit asks the ANGELUS to write outfits/default.toml (or
// default-<provider>.toml if that name is taken) and point default_outfit at
// it. The wizard composes the body: that is client ergonomics: but the file
// and the config are the server's state, so the server writes them.
func createDefaultOutfit(ctx context.Context, acli *angelus.Client, existing []string, providerName, model string) (string, error) {
	name := "default"
	for _, n := range existing {
		if n == name {
			name = "default-" + providerName
			break
		}
	}
	if model == "" {
		model = defaultModelFor(providerName)
	}
	resp, err := acli.Configure(ctx, rpc.ConfigureRequest{
		DefaultOutfit: name,
		Outfit:        name,
		Body:          starterOutfitBody(providerName, model),
	})
	if err != nil {
		return "", err
	}
	if resp.DefaultOutfit != "" {
		name = resp.DefaultOutfit
	}
	return name, nil
}

// starterOutfitBody is a minimal outfit. It declares NO skills of its own: the
// outfit references the skills directory via `skills = { dirName = "skills" }`,
// and that alone is enough, because first-party skills ship inside the binary
// and load from there. A copy in config would be overridden by the shipped one
// anyway, so it would sit there stale and unread: which is the pointlessness
// this deliberately avoids.
func starterOutfitBody(providerName, model string) string {
	body := fmt.Sprintf(`# Scaffolded by figaro first-run setup.
# Edit to taste; see docs/outfits for the schema.

# Skills may be markdown files or folders containing SKILL.md plus
# supplemental files. dirName fans them out as skills.<name>.
skills = { dirName = "skills" }

[system]
provider = %q
`, providerName)
	if model != "" {
		body += fmt.Sprintf("model = %q\n", model)
	}
	return body
}

// --- pretty bits -----------------------------------------------------------

func dim(s string) string   { return term.Dim(s) }
func cyan(s string) string  { return term.Cyan(s) }
func green(s string) string { return term.Green(s) }

func printWelcome(loaded *config.Loaded) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, cyan("  ▌ figaro setup")+dim("  ·  one minute, three steps  ·  config: "+loaded.ConfigPath))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, dim("       (Step 1/3, secrets vault, already done by hush.)"))
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
// when bound (modulo context: caller supplies one).
var _ = func(acli *angelus.Client, ctx context.Context) createFn {
	return func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, nil, nil)
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

// pickModelOrFallback queries the freshly-credentialed provider for
// its model list and prompts the user to pick one. On any failure
// (no network, listing endpoint missing, user dismisses the prompt)
// it falls back to defaultModelFor(providerName). Never blocks the
// wizard: the outfit always gets *some* model.
func pickModelOrFallback(loaded *config.Loaded, providerName string) string {
	fallback := defaultModelFor(providerName)

	prov, _ := buildProvider(loaded, providerName)
	if prov == nil {
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	models, err := prov.Models(ctx)
	if err != nil || len(models) == 0 {
		fmt.Fprintln(os.Stderr, dim("     (could not list models; using built-in default "+fallback+")"))
		return fallback
	}

	// Newest-first when ids carry a sortable suffix; otherwise stable.
	sort.SliceStable(models, func(i, j int) bool { return models[i].ID > models[j].ID })

	opts := make([]tui.ProviderOption, len(models))
	for i, m := range models {
		label := m.ID
		hint := m.Name
		if i == 0 {
			hint = strings.TrimSpace(hint + " (default)")
		}
		opts[i] = tui.ProviderOption{Key: m.ID, Label: label, Hint: hint}
	}

	fmt.Fprintln(os.Stderr, dim("     Choose a model (Enter for the newest):"))
	key, err := tui.PickProvider("Model", opts)
	if err != nil || key == "" {
		return fallback
	}
	return key
}
