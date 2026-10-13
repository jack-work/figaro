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

// promptVaultPassphrase asks the question the vault's state actually
// poses. With no identity on disk, that is "choose one", twice, and the
// keyring promise is made only if there is a keyring to keep it: where
// there is none, the user is offered a key file instead, which is the
// only arrangement on such a host that does not ask him something on
// every command. With an identity, it is "what is yours", once, checked
// against the identity, with a bounded retry.
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

// promptFirstRunWithoutKeyring offers the key file first, because on a
// host with no keyring a passphrase is a question asked forever. The
// returned bytes are the ones hush will encrypt the new identity with,
// so writing the file here and handing back its contents is the whole
// of the file arrangement: hush writes the identity, and the next
// process reads the file and never asks.
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

// ensureHush initializes hush. Must be called from the CLI process.
func ensureHush() {
	h := MustHush()
	if err := h.EnsureReady(); err != nil {
		// A saved passphrase that stopped decrypting is otherwise a dead
		// end: every command fails and nothing names the lever.
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

// buildPromptForm collects the per-prompt form values and the dressing THIS
// prompt asked for. The dressing is the caller's, carried down from the plan
// that parsed it: a package-global here meant two commands in flight swapped
// each other's -S, -D and -O.
func buildPromptForm(d dressing) *rpc.FormInput {
	// `cwd` IS NOT SENT. system.cwd is the canonical working directory, written
	// by the harness at create time through the privileged path; the CLI used
	// to write a second, unprivileged spelling of the same fact on every
	// prompt. Gluck, 2026-08-19: keep whichever is canonical, a stale cwd is
	// acceptable.
	snap := map[string]json.RawMessage{}
	dt := time.Now().Format("Monday, January 2, 2006, 3PM MST")
	if b, err := json.Marshal(dt); err == nil {
		snap["datetime"] = b
	}
	// Allowlisted env vars from the caller's shell.
	for k, v := range form.EnvironmentSnapshot() {
		snap[k] = v
	}
	if len(snap) == 0 && d.IsEmpty() {
		return nil
	}
	return &rpc.FormInput{Context: snap, Outfits: d.names, Patch: d.patch}
}

// cacheDir returns the directory for ephemeral figaro data that can
// be regenerated (update-check memo, etc). XDG_CACHE_HOME and
// FIGARO_CACHE_DIR win in that order.
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
