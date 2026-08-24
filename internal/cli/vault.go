package cli

import (
	"errors"
	"fmt"

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
	v, err := keyring.Get(svc, acct)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		fmt.Fprintln(stdout, "           no saved passphrase: you'll be prompted")
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
	err := keyring.Delete(svc, acct)
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
	pp, err := tui.PromptPassphrase("figaro")
	if err != nil {
		return err
	}
	defer func() {
		for i := range pp {
			pp[i] = 0
		}
	}()
	if err := h.VerifyPassphrase(pp); err != nil {
		return fmt.Errorf("that passphrase does not decrypt the identity: nothing saved: %w", err)
	}
	svc, acct := h.KeyringTarget()
	if svc != "" && acct != "" {
		if err := keyring.Set(svc, acct, string(pp)); err != nil {
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
