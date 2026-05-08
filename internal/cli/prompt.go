package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/transport"
)

// runPrompt is the default verb: resolve the shell-bound figaro (or
// create one) and prompt it.
func runPrompt(loaded *config.Loaded, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()

	resp, err := acli.Resolve(ctx, ppid)
	if err != nil {
		die("resolve: %s", err)
	}

	var figaroID string
	var figaroEP transport.Endpoint

	if resp.Found {
		figaroID = resp.FigaroID
		figaroEP = transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
	} else {
		figaroID, figaroEP = mustCreateAndBind(ctx, acli, loaded, ppid)
	}
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded)
}

// runQua is sugar over the unified aria-prompt path.
//
//	figaro qua -- <prompt>           # same as bare `figaro -- <prompt>`
//	figaro qua <id> -- <prompt>      # one-shot send to a named aria
//
// In the targeted form, the shell's PPID binding is left untouched.
func runQua(loaded *config.Loaded, args []string) {
	var targetID string
	promptArgs := args
	if len(args) > 0 && args[0] != "--" {
		targetID = args[0]
		promptArgs = args[1:]
	}

	prompt := extractPrompt(promptArgs)
	if prompt == "" {
		die("usage: figaro qua [<id>] -- <prompt>")
	}

	if targetID == "" {
		runPrompt(loaded, prompt)
		return
	}
	promptAria(loaded, targetID, prompt)
}

// runNewPrompt unbinds any current figaro from this shell, creates a
// fresh one, and prompts it.
func runNewPrompt(loaded *config.Loaded, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	acli.Unbind(ctx, ppid)

	figaroID, figaroEP := mustCreateAndBind(ctx, acli, loaded, ppid)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded)
}

// promptAria sends a prompt to a named aria. If the aria is live or
// dormant, attach it; if unknown, create it with the supplied id.
// The shell's pid binding is intentionally left untouched — named
// arias are explicitly addressed.
func promptAria(loaded *config.Loaded, ariaID, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ep, err := resolveOrCreate(ctx, acli, ariaID)
	if err != nil {
		die("%s", err)
	}
	mustPromptFigaro(ctx, ep, ariaID, prompt, loaded)
}

// resolveOrCreate brings a named aria up. Tries Attach first
// (succeeds if live or dormant-on-disk); falls back to CreateWithID
// when Attach reports the id is unknown to the backend. Returns the
// live endpoint either way.
func resolveOrCreate(ctx context.Context, acli *angelus.Client, ariaID string) (transport.Endpoint, error) {
	attachCtx, attachCancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.Attach(attachCtx, ariaID)
	attachCancel()
	if err == nil {
		ep := transport.Endpoint{
			Scheme:  resp.Endpoint.Scheme,
			Address: resp.Endpoint.Address,
		}
		waitForSocket(ep.Address, 3*time.Second)
		return ep, nil
	}

	// Attach failed. Distinguish "not found on disk" (→ create) from
	// genuine errors (→ propagate).
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		return transport.Endpoint{}, fmt.Errorf("attach %q: %w", ariaID, err)
	}

	createCtx, createCancel := context.WithTimeout(ctx, 10*time.Second)
	defer createCancel()
	createResp, err := acli.CreateWithID(createCtx, ariaID, "", nil)
	if err != nil {
		return transport.Endpoint{}, fmt.Errorf("create %q: %w", ariaID, err)
	}
	ep := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}
	waitForSocket(ep.Address, 3*time.Second)
	return ep, nil
}

// waitForSocket polls until the unix socket file exists or timeout
// elapses. The angelus creates the figaro in a goroutine, so newly
// created agents need a brief moment before their socket is dialable.
func waitForSocket(path string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
