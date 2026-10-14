package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/tui"
	"github.com/jack-work/jkrpc"
)

// One underlying provider can appear several times with different modes;
// provider is what the outfit gets.
type providerChoice struct {
	label    string
	provider string
	hint     string
	setup    func(loaded *config.Loaded) error
}

func init() {
	if r := providerPkg.Lookup("copilot"); r != nil {
		r.Setup = runCopilotLogin
		r.Login = runCopilotLogin
	}
	if r := providerPkg.Lookup("meta"); r != nil {
		r.Setup = runMetaLogin
		r.Login = runMetaLogin
	}
	if r := providerPkg.Lookup(azureProvider); r != nil {
		r.Setup = runAzureLogin
		r.Login = runAzureLogin
	}
	if r := providerPkg.Lookup(rou2Provider); r != nil {
		r.Setup = runRou2Login
		r.Login = runRou2Login
	}
	if r := providerPkg.Lookup("anthropic"); r != nil {
		login := func(loaded *config.Loaded) error {
			return runOAuthInline("anthropic", auth.AnthropicOAuth)
		}
		r.Setup = login
		r.Login = login
	}
}

// Position is the recommendation: the first entry is the default.
var providerCatalog = []providerChoice{
	{
		label:    "GitHub Copilot (device code login)",
		provider: "copilot",
		hint:     "recommended: uses your Copilot subscription",
		setup:    runCopilotLogin,
	},
	{
		label:    "Meta Muse Code (device code login)",
		provider: "meta",
		hint:     "uses your Muse Code subscription",
		setup:    runMetaLogin,
	},
	{
		label:    "Azure AI Foundry (Microsoft Entra device code)",
		provider: azureProvider,
		hint:     "your Azure OpenAI or Foundry resource",
		setup:    runAzureLogin,
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

type createFn func() (*rpc.CreateResponse, error)

// createWithFirstRun invokes fn once; on a typed first-run error, drives the
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

// Hush (station 1) was handled by ensureHush before any RPC; this drives
// stations 2 and 3.
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

	printStep(3, 3, "Default outfit")
	fmt.Fprintln(os.Stderr, dim("     An outfit bundles a provider + model so `fig` knows what to do"))
	fmt.Fprintln(os.Stderr, dim("     when you don't pass flags. We'll make one for you and set it as default."))
	fmt.Fprintln(os.Stderr)

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

// The first option is the default.
func pickFromMenu(loaded *config.Loaded, options []providerChoice) (providerChoice, error) {
	if loaded != nil && !loaded.Interactive() {
		return pickFromMenuNumbered(options)
	}
	tuiOpts := make([]tui.ProviderOption, len(options))
	for i, o := range options {
		tuiOpts[i] = tui.ProviderOption{
			Key:   strconv.Itoa(i),
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

func setupCredentialsFor(loaded *config.Loaded, choice providerChoice) error {
	return choice.setup(loaded)
}

func runOAuthInline(providerName string, cfg auth.OAuthConfig) error {
	h := MustHush()
	hushClient := h.Client()
	return auth.Login(hushClient, cfg, func() (string, error) {
		line, err := term.ReadLine("")
		return strings.TrimSpace(line), err
	})
}

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

	// The domain is not a secret; it says where to exchange.
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

	h := MustHush()
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
	err = storeEncryptedAPIKey(loaded, providerName, string(key))
	for i := range key {
		key[i] = 0
	}
	return err
}

func runMetaLogin(loaded *config.Loaded) error {
	minted, err := auth.LoginMeta()
	if err != nil {
		return err
	}
	if minted.Email != "" {
		fmt.Fprintln(os.Stderr, "       "+dim("Muse Code subscription key minted for "+minted.Email))
	}
	return storeEncryptedAPIKey(loaded, "meta", minted.APIKey)
}

func storeEncryptedAPIKey(loaded *config.Loaded, providerName, key string) error {
	encrypted, err := MustHush().Client().Encrypt(map[string]string{"api_key": key})
	if err != nil {
		return fmt.Errorf("encrypt api key via hush: %w", err)
	}
	enc, ok := encrypted["api_key"]
	if !ok || enc == "" {
		return fmt.Errorf("hush returned no ciphertext for api_key")
	}

	path := loaded.ProviderAuthPath(providerName)
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if _, err := toml.Decode(string(data), &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	doc["api_key"] = enc

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(doc); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintln(os.Stderr, "  "+green("✓")+" stored encrypted api key → "+dim(path))
	return nil
}

// The angelus writes the outfit and the config: the file is server state.
func createDefaultOutfit(ctx context.Context, acli *sdk.Angelus, existing []string, providerName, model string) (string, error) {
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

// The starter outfit declares no skills: first-party skills ship inside the
// binary, and a copy in config would be stale and unread.
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

var _ = func(acli *sdk.Angelus, ctx context.Context) createFn {
	return func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, nil, nil)
	}
}

func isStdinTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// On any failure it falls back to defaultModelFor: the wizard never blocks on a
// listing.
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
