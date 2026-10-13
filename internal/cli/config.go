package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/tui"
)

func init() { boot.PromptPassphrase = promptVaultPassphrase }

func MustLoadConfig() *config.Loaded {
	loaded, err := boot.LoadConfig()
	if err != nil {
		die("%s", err)
	}
	return loaded
}

func MustHush() *managed.Hush {
	h, err := boot.Hush()
	if err != nil {
		die("%s", err)
	}
	return h
}

// With no identity on disk: choose one, twice, and the keyring promise only
// where there is a keyring, else a key file. With an identity: ask once,
// bounded retry.
func promptVaultPassphrase(h *managed.Hush, appName string) ([]byte, error) {
	if h == nil {
		return tui.PromptPassphrase(tui.PassphraseRequest{App: appName, Mode: tui.PassphraseCreate})
	}
	if h.HasIdentity() {
		return tui.PromptPassphrase(tui.PassphraseRequest{
			App:    appName,
			Mode:   tui.PassphraseUnlock,
			Verify: h.VerifyPassphrase,
		})
	}
	svc, acct := h.KeyringTarget()
	if keyringReachable(svc, acct) {
		return tui.PromptPassphrase(tui.PassphraseRequest{
			App:            appName,
			Mode:           tui.PassphraseCreate,
			SavesToKeyring: true,
		})
	}
	return promptFirstRunWithoutKeyring(h, appName)
}

// On a host with no keyring a passphrase is a question asked forever, so the
// key file is offered first; the returned bytes are what hush encrypts the new
// identity with.
func promptFirstRunWithoutKeyring(h *managed.Hush, appName string) ([]byte, error) {
	choice := string(unlockKindFile)
	if tui.Available() || isStdinTTY() {
		var err error
		choice, err = tui.PickProvider(
			"No OS keyring is reachable here. How should "+appName+" unlock its vault?",
			[]tui.ProviderOption{
				{Key: string(unlockKindFile), Label: "Key file", Hint: "a random passphrase in a 0600 file, never asks"},
				{Key: string(unlockKindPassphrase), Label: "Passphrase", Hint: "you type it on every start"},
			})
		if err != nil {
			return nil, err
		}
	}
	if choice == string(unlockKindPassphrase) {
		return tui.PromptPassphrase(tui.PassphraseRequest{
			App:  appName,
			Mode: tui.PassphraseCreate,
		})
	}
	pp, path, err := setupFileUnlock(h)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(stderrw, "figaro: vault passphrase written to %s (mode 0600)\n", path)
	fmt.Fprintln(stderrw, "figaro: anyone who can read your home directory can read it, and your provider tokens with it")
	return pp, nil
}

// ensureHush must be called from the CLI process.
func ensureHush() {
	h := MustHush()
	if err := h.EnsureReady(); err != nil {
		// A saved passphrase that stopped decrypting is otherwise a dead end.
		if strings.Contains(err.Error(), "incorrect passphrase") {
			fmt.Fprintf(stderrw, "figaro: hush: %s\n\n", err)
			fmt.Fprintln(stderrw, "The saved passphrase does not decrypt this vault.")
			fmt.Fprintln(stderrw, "  figaro vault status    inspect it")
			fmt.Fprintln(stderrw, "  figaro vault forget    clear it and be prompted again")
			os.Exit(1)
		}
		die("hush: %s", err)
	}
}

// The dressing is carried down from the plan: a package-global meant two
// commands in flight swapped each other's -S, -D and -O.
func buildPromptForm(d dressing) *rpc.FormInput {
	// `cwd` is not sent: system.cwd is written by the harness at create time
	// through the privileged path.
	snap := map[string]json.RawMessage{}
	dt := time.Now().Format("Monday, January 2, 2006, 3PM MST")
	if b, err := json.Marshal(dt); err == nil {
		snap["datetime"] = b
	}
	for k, v := range form.EnvironmentSnapshot() {
		snap[k] = v
	}
	if len(snap) == 0 && d.IsEmpty() {
		return nil
	}
	return &rpc.FormInput{Context: snap, Outfits: d.names, Patch: d.patch}
}

// XDG_CACHE_HOME and FIGARO_CACHE_DIR win in that order.
func cacheDir() string {
	if d := os.Getenv("FIGARO_CACHE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "figaro")
}
