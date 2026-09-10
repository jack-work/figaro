package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/hush/managed"
	"github.com/zalando/go-keyring"

	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/tui"
)

// The vault is figaro's embedded hush: its own identity, its own agent,
// its own keyring entry (service "figaro", not "hush"). The hush binary
// on PATH addresses a different instance, so lifecycle lives here.

func runVaultStatus() error {
	h := mustHush()
	fmt.Fprintf(stdout, "mode       %s\n", h.Mode())
	fmt.Fprintf(stdout, "identity   %s\n", h.IdentityFile())
	if !h.HasIdentity() {
		fmt.Fprintln(stdout, "           (absent: the next figaro command sets one up)")
		return nil
	}
	if pub, err := h.PublicKey(); err == nil {
		fmt.Fprintf(stdout, "public key %s\n", pub)
	}

	if err := h.Client().Ping(); err == nil {
		fmt.Fprintln(stdout, "agent      running")
	} else {
		fmt.Fprintln(stdout, "agent      not running")
	}

	method := h.Config().Unlock.Method
	if method == "" {
		method = "auto"
	}
	if method == "file" {
		fmt.Fprintf(stdout, "unlock     file %s\n", h.Config().Unlock.File)
	} else {
		fmt.Fprintf(stdout, "unlock     %s\n", method)
	}

	// A file- or exec-unlocked vault never reads the keyring, so
	// reporting on the entry there invites the reader to fix something
	// that is not in the path.
	if method == "file" || method == "exec" {
		fmt.Fprintln(stdout, "keyring    not used by this unlock method")
		return nil
	}

	svc, acct := h.KeyringTarget()
	if svc == "" || acct == "" {
		fmt.Fprintln(stdout, "keyring    not configured")
		return nil
	}
	fmt.Fprintf(stdout, "keyring    %s:%s\n", svc, acct)
	v, err := keyringGet(svc, acct)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		fmt.Fprintln(stdout, "           no saved passphrase: you'll be prompted")
	case errors.Is(err, errKeyringTimeout):
		fmt.Fprintf(stdout, "           no answer in %s (locked collection with no prompter?)\n", keyringTimeout)
		fmt.Fprintln(stdout, "           this host cannot use the keyring: figaro vault init --file")
	case err != nil:
		fmt.Fprintf(stdout, "           unreadable (%v)\n", err)
	default:
		pp := []byte(v)
		if verr := h.VerifyPassphrase(pp); verr != nil {
			fmt.Fprintf(stdout, "           saved passphrase (%d bytes) does NOT decrypt the identity\n", len(v))
			fmt.Fprintln(stdout, "           fix: figaro vault forget, then run any figaro command")
		} else {
			fmt.Fprintf(stdout, "           saved passphrase (%d bytes) verifies\n", len(v))
		}
	}
	return nil
}

func runVaultForget() error {
	h := mustHush()
	svc, acct := h.KeyringTarget()
	if svc == "" || acct == "" {
		return fmt.Errorf("no keyring entry is configured for this vault")
	}
	err := keyringDelete(svc, acct)
	if errors.Is(err, keyring.ErrNotFound) {
		fmt.Fprintf(stdout, "nothing saved for %s:%s\n", svc, acct)
		return nil
	}
	if err != nil {
		return fmt.Errorf("keyring delete (%s:%s): %w", svc, acct, err)
	}
	fmt.Fprintf(stdout, "cleared %s:%s: the next figaro command will prompt\n", svc, acct)
	return nil
}

func runVaultUnlock() error {
	h := mustHush()
	if !h.HasIdentity() {
		return fmt.Errorf("no identity yet at %s: run any figaro command to set one up", h.IdentityFile())
	}
	pp, err := tui.PromptPassphrase(tui.PassphraseRequest{
		App:    vaultAppName(),
		Mode:   tui.PassphraseUnlock,
		Verify: h.VerifyPassphrase,
	})
	if err != nil {
		return err
	}
	defer func() {
		for i := range pp {
			pp[i] = 0
		}
	}()
	// A vault that unlocks from a file or a helper has no use for a
	// keyring entry, and writing one would leave a second copy of the
	// passphrase in a place the user did not choose.
	if method := h.Config().Unlock.Method; method == "" || method == "auto" || method == "keyring" {
		svc, acct := h.KeyringTarget()
		if svc != "" && acct != "" {
			if err := keyringSet(svc, acct, string(pp)); err != nil {
				fmt.Fprintf(stderrw, "warning: couldn't save to keyring (%v)\n", err)
			} else {
				fmt.Fprintf(stdout, "saved to %s:%s\n", svc, acct)
			}
		}
	}
	if err := h.EnsureReady(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "agent running")
	return nil
}

func runVaultLock() error {
	h := mustHush()
	if err := h.Client().Ping(); err != nil {
		fmt.Fprintln(stdout, "agent already stopped")
		return nil
	}
	if err := h.Client().Shutdown(); err != nil {
		return fmt.Errorf("shutdown agent: %w", err)
	}
	fmt.Fprintln(stdout, "agent stopped; the decrypted identity is gone from memory")
	return nil
}

// vaultAppName is the name the vault answers to, and the keyring
// service it scopes itself under. Dev shells pivot it with
// FIGARO_HUSH_APP; mustHush reads the same variable.
func vaultAppName() string {
	if n := os.Getenv("FIGARO_HUSH_APP"); n != "" {
		return n
	}
	return "figaro"
}

// vaultUnlockKind is what the caller asked for on the command line, or
// what we work out for him when he asked for nothing.
type vaultUnlockKind string

const (
	unlockKindFile       vaultUnlockKind = "file"
	unlockKindPassphrase vaultUnlockKind = "passphrase"
	unlockKindAuto       vaultUnlockKind = ""
)

// resolveUnlockKind turns "no flag" into a choice. The rule is the one
// the host imposes: with no keyring to remember a passphrase, asking
// for one means asking for it forever, so the file is the default.
func resolveUnlockKind(kind vaultUnlockKind, h *managed.Hush) vaultUnlockKind {
	if kind != unlockKindAuto {
		return kind
	}
	svc, acct := h.KeyringTarget()
	if keyringReachable(svc, acct) {
		return unlockKindPassphrase
	}
	return unlockKindFile
}

func runVaultInit(kind vaultUnlockKind) error {
	h := mustHush()
	if h.HasIdentity() {
		return fmt.Errorf("this vault already has an identity at %s\n"+
			"  figaro vault status   inspect it\n"+
			"  figaro vault reset    replace it (provider logins are redone)", h.IdentityFile())
	}
	return initVault(h, resolveUnlockKind(kind, h))
}

// initVault creates the identity and leaves the agent running.
func initVault(h *managed.Hush, kind vaultUnlockKind) error {
	var pp []byte
	var err error
	switch kind {
	case unlockKindFile:
		var path string
		pp, path, err = setupFileUnlock(h)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "passphrase file %s (mode 0600)\n", path)
		fmt.Fprintln(stdout, "anyone who can read your home directory can read it, and therefore your provider tokens")
	default:
		svc, acct := h.KeyringTarget()
		pp, err = tui.PromptPassphrase(tui.PassphraseRequest{
			App:            vaultAppName(),
			Mode:           tui.PassphraseCreate,
			SavesToKeyring: keyringReachable(svc, acct),
		})
		if err != nil {
			return err
		}
	}
	defer wipeBytes(pp)

	pub, err := h.Init(pp)
	if err != nil {
		return fmt.Errorf("create identity: %w", err)
	}
	fmt.Fprintf(stdout, "identity        %s\n", h.IdentityFile())
	fmt.Fprintf(stdout, "public key      %s\n", pub)

	if kind != unlockKindFile {
		svc, acct := h.KeyringTarget()
		if svc != "" && acct != "" && keyringReachable(svc, acct) {
			if err := keyringSet(svc, acct, string(pp)); err != nil {
				fmt.Fprintf(stderrw, "warning: couldn't save to keyring (%v): you'll be asked again\n", err)
			} else {
				fmt.Fprintf(stdout, "keyring         %s:%s\n", svc, acct)
			}
		}
	}
	if err := h.EnsureReady(); err != nil {
		return fmt.Errorf("start the vault agent: %w", err)
	}
	fmt.Fprintln(stdout, "agent           running")
	return nil
}

// runVaultReset throws away the identity and everything it encrypted,
// makes a new one, and walks back through the provider logins that the
// old identity was holding. The old files are renamed, never deleted:
// if the tokens turn out to be recoverable after all, they are still
// there, and if they are not, a stale file costs a kilobyte.
func runVaultReset(loaded *config.Loaded, kind vaultUnlockKind, yes bool) error {
	h := mustHush()
	providers := vaultOAuthProviders(h)

	if !yes {
		fmt.Fprintf(stderrw, "\nReset the %s vault at %s.\n\n", vaultAppName(), h.Config().ConfigDir)
		fmt.Fprintln(stderrw, "What is lost: the provider credentials this identity encrypted.")
		if len(providers) > 0 {
			fmt.Fprintf(stderrw, "  %s: you will be sent through `figaro login` for each, right after.\n", strings.Join(providers, ", "))
		} else {
			fmt.Fprintln(stderrw, "  (none stored yet)")
		}
		fmt.Fprintln(stderrw, "What is not: your arias, forms and outfits. They are not encrypted with it.")
		fmt.Fprintln(stderrw, "The old identity and token files are renamed, not deleted.")
		fmt.Fprintln(stderrw)
		line, err := term.ReadLine("Type yes to continue: ")
		if err != nil {
			return fmt.Errorf("read answer: %w", err)
		}
		if strings.TrimSpace(strings.ToLower(line)) != "yes" {
			fmt.Fprintln(stdout, "nothing changed")
			return nil
		}
	}

	// Stop the agent first: it holds the decrypted identity, and it
	// would go on serving the old one from memory while we write a new
	// one to disk.
	if err := h.Client().Ping(); err == nil {
		if err := h.Client().Shutdown(); err != nil {
			fmt.Fprintf(stderrw, "warning: couldn't stop the vault agent (%v)\n", err)
		}
	}

	svc, acct := h.KeyringTarget()
	if svc != "" && acct != "" {
		err := keyringDelete(svc, acct)
		switch {
		case err == nil:
			fmt.Fprintf(stdout, "forgot          %s:%s\n", svc, acct)
		case errors.Is(err, keyring.ErrNotFound):
			// Nothing was saved. Not worth a line.
		default:
			fmt.Fprintf(stderrw, "warning: couldn't clear the keyring entry (%v)\n", err)
		}
	}

	stamp := time.Now().Format("20060102-150405")
	moved, err := archiveVaultFiles(h, providers, stamp)
	if err != nil {
		return err
	}
	for _, m := range moved {
		fmt.Fprintf(stdout, "renamed         %s\n", m)
	}

	if err := initVault(h, resolveUnlockKind(kind, h)); err != nil {
		return err
	}

	if len(providers) == 0 {
		fmt.Fprintln(stdout, "\nno provider logins to redo")
		return nil
	}
	return reloginProviders(loaded, providers)
}

// vaultOAuthProviders names the providers whose tokens this identity is
// holding, read before anything is moved: after the files are renamed
// there is nothing left to ask.
func vaultOAuthProviders(h *managed.Hush) []string {
	cfg := h.Config()
	if cfg == nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(cfg.StateDir, "oauth"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".toml"))
	}
	sort.Strings(out)
	return out
}

// archiveVaultFiles renames the identity and every oauth token file out
// of the way, returning what it moved.
func archiveVaultFiles(h *managed.Hush, providers []string, stamp string) ([]string, error) {
	cfg := h.Config()
	paths := []string{h.IdentityFile(), h.IdentityFile() + ".pub"}
	for _, p := range providers {
		paths = append(paths, filepath.Join(cfg.StateDir, "oauth", p+".toml"))
	}
	var moved []string
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		dst := path + ".reset-" + stamp
		if err := os.Rename(path, dst); err != nil {
			return moved, fmt.Errorf("move %s aside: %w", path, err)
		}
		moved = append(moved, dst)
	}
	return moved, nil
}

// reloginProviders drives `figaro login <provider>` for each provider
// the old identity held, in turn and interactively, and says which ones
// took. One failure does not stop the rest: a browser flow that the
// user abandons should not cost him the others.
func reloginProviders(loaded *config.Loaded, providers []string) error {
	var ok, failed []string
	for _, name := range providers {
		fmt.Fprintf(stderrw, "\n--- figaro login %s ---\n", name)
		reg := providerPkg.Lookup(name)
		if reg == nil || reg.Login == nil {
			fmt.Fprintf(stderrw, "no login flow for provider %q: skipped\n", name)
			failed = append(failed, name)
			continue
		}
		if err := reg.Login(loaded); err != nil {
			fmt.Fprintf(stderrw, "login %s failed: %v\n", name, err)
			failed = append(failed, name)
			continue
		}
		ok = append(ok, name)
	}
	fmt.Fprintln(stdout)
	if len(ok) > 0 {
		fmt.Fprintf(stdout, "logged in       %s\n", strings.Join(ok, ", "))
	}
	if len(failed) > 0 {
		fmt.Fprintf(stdout, "still missing   %s\n", strings.Join(failed, ", "))
		fmt.Fprintf(stdout, "                retry with: figaro login %s\n", failed[0])
	}
	return nil
}

func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// vaultUnlockKindFromFlags reads --file / --passphrase. Neither means
// "decide for me"; both together is a contradiction and says so rather
// than silently preferring one.
func vaultUnlockKindFromFlags(file, passphrase bool) (vaultUnlockKind, error) {
	switch {
	case file && passphrase:
		return unlockKindAuto, fmt.Errorf("--file and --passphrase ask for different things: pick one")
	case file:
		return unlockKindFile, nil
	case passphrase:
		return unlockKindPassphrase, nil
	}
	return unlockKindAuto, nil
}
