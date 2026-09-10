package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"

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
	fmt.Fprintf(stdout, "unlock     %s\n", method)

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
		fmt.Fprintln(stdout, "           this host cannot use the keyring: figaro vault status is the one command that must never hang")
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
	svc, acct := h.KeyringTarget()
	if svc != "" && acct != "" {
		if err := keyringSet(svc, acct, string(pp)); err != nil {
			fmt.Fprintf(stderrw, "warning: couldn't save to keyring (%v)\n", err)
		} else {
			fmt.Fprintf(stdout, "saved to %s:%s\n", svc, acct)
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
