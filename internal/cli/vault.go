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
	fmt.Printf("mode       %s\n", h.Mode())
	fmt.Printf("identity   %s\n", h.IdentityFile())
	if !h.HasIdentity() {
		fmt.Println("           (absent: the next figaro command sets one up)")
		return nil
	}
	if pub, err := h.PublicKey(); err == nil {
		fmt.Printf("public key %s\n", pub)
	}

	if err := h.Client().Ping(); err == nil {
		fmt.Println("agent      running")
	} else {
		fmt.Println("agent      not running")
	}

	method := h.Config().Unlock.Method
	if method == "" {
		method = "auto"
	}
	fmt.Printf("unlock     %s\n", method)

	svc, acct := h.KeyringTarget()
	if svc == "" || acct == "" {
		fmt.Println("keyring    not configured")
		return nil
	}
	fmt.Printf("keyring    %s:%s\n", svc, acct)
	v, err := keyring.Get(svc, acct)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		fmt.Println("           no saved passphrase: you'll be prompted")
	case err != nil:
		fmt.Printf("           unreadable (%v)\n", err)
	default:
		pp := []byte(v)
		if verr := h.VerifyPassphrase(pp); verr != nil {
			fmt.Printf("           saved passphrase (%d bytes) does NOT decrypt the identity\n", len(v))
			fmt.Println("           fix: figaro vault forget, then run any figaro command")
		} else {
			fmt.Printf("           saved passphrase (%d bytes) verifies\n", len(v))
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
		fmt.Printf("nothing saved for %s:%s\n", svc, acct)
		return nil
	}
	if err != nil {
		return fmt.Errorf("keyring delete (%s:%s): %w", svc, acct, err)
	}
	fmt.Printf("cleared %s:%s: the next figaro command will prompt\n", svc, acct)
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
			fmt.Fprintf(os.Stderr, "warning: couldn't save to keyring (%v)\n", err)
		} else {
			fmt.Printf("saved to %s:%s\n", svc, acct)
		}
	}
	if err := h.EnsureReady(); err != nil {
		return err
	}
	fmt.Println("agent running")
	return nil
}

func runVaultLock() error {
	h := mustHush()
	if err := h.Client().Ping(); err != nil {
		fmt.Println("agent already stopped")
		return nil
	}
	if err := h.Client().Shutdown(); err != nil {
		return fmt.Errorf("shutdown agent: %w", err)
	}
	fmt.Println("agent stopped; the decrypted identity is gone from memory")
	return nil
}
