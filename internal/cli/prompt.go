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

// runPrompt resolves the shell-bound figaro and prompts it.
func runPrompt(loaded *config.Loaded, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()

	resp, err := resolveBinding(ctx, acli, ppid)
	if err != nil {
		die("resolve: %s", err)
	}

	var figaroID string
	var figaroEP transport.Endpoint

	if resp.Found {
		// Bound at a pending fork-point (attend <id>:<LT>): this prompt forks
		// there and moves to the new branch (one-shot — the rebind clears it).
		if resp.AtMainLT > 0 {
			runSendForkAt(loaded, resp.FigaroID, resp.AtMainLT, false, prompt, set)
			return
		}
		figaroID = resp.FigaroID
		figaroEP = transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
	} else {
		figaroID, figaroEP = mustCreateAndBind(ctx, acli, loaded, ppid)
	}
	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set)
}

// runNewPrompt creates a fresh figaro and prompts it.
func runNewPrompt(loaded *config.Loaded, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	unbindBinding(ctx, acli, ppid)

	figaroID, figaroEP := mustCreateAndBind(ctx, acli, loaded, ppid)
	// In no-bind mode nothing was bound to the shell, so print the id
	// to stderr so callers can capture it (mirrors `send -f`'s notice).
	if bindingDisabled() {
		fmt.Fprintf(os.Stderr, "created %s\n", figaroID)
	}
	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set)
}

// runSendForkAt implements `send <trunk>:<LT>`: fork the trunk at atMainLT
// (imperative interior fork, empty alternative), then send the prompt to the
// trunk we end up attended to. By default we rebind this shell to the new
// alternative and send there; with stay (--attend=false) we leave the shell
// on the original trunk and send there (the alternative is parked at LT).
func runSendForkAt(loaded *config.Loaded, trunkID string, atMainLT uint64, stay bool, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	if trunkID == "" {
		r, err := resolveBinding(ctx, acli, ppid)
		if err != nil || !r.Found {
			die("send: no trunk bound to this shell (try: <id>:<LT> or attend <id>)")
		}
		trunkID = r.FigaroID
	}

	fctx, fcancel := context.WithTimeout(ctx, 10*time.Second)
	fr, err := acli.Fork(fctx, trunkID, atMainLT)
	fcancel()
	if err != nil {
		die("send: fork %s at LT %d: %s", trunkID, atMainLT, err)
	}
	if fr.OwnerNote != "" {
		fmt.Fprintf(os.Stderr, "%s\n", fr.OwnerNote)
	}

	target := fr.Alternative
	if stay {
		target = trunkID // parked alternative; shell stays on the original
		fmt.Fprintf(os.Stderr, "forked %s at LT %d -> %s (parked; staying on %s)\n", trunkID, atMainLT, fr.Alternative, trunkID)
	} else {
		unbindBinding(ctx, acli, ppid)
		if err := bindBinding(ctx, acli, ppid, fr.Alternative, 0); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not attend %s: %s\n", fr.Alternative, err)
		}
		fmt.Fprintf(os.Stderr, "forked %s at LT %d -> attending %s\n", trunkID, atMainLT, fr.Alternative)
	}

	ep, err := resolveAria(ctx, acli, target)
	if err != nil {
		die("%s", err)
	}
	prompt = expandAtRefsForEndpoint(ctx, ep, prompt)
	mustPromptFigaro(ctx, ep, target, prompt, loaded, set)
}

// promptAria sends a prompt to a named aria.
func promptAria(loaded *config.Loaded, ariaID, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ep, err := resolveAria(ctx, acli, ariaID)
	if err != nil {
		die("%s", err)
	}
	prompt = expandAtRefsForEndpoint(ctx, ep, prompt)
	mustPromptFigaro(ctx, ep, ariaID, prompt, loaded, set)
}

// resolveAria attaches to an existing named aria. Aria ids are
// system-minted, so an unknown id is an error — a new conversation
// comes from the no-id flow (`figaro`), not by naming one here.
func resolveAria(ctx context.Context, acli *angelus.Client, ariaID string) (transport.Endpoint, error) {
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
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not in tree") {
		return transport.Endpoint{}, fmt.Errorf("no such aria %q (ids are system-minted; run `figaro` to start one)", ariaID)
	}
	return transport.Endpoint{}, fmt.Errorf("attach %q: %w", ariaID, err)
}

// waitForSocket polls until the socket exists or timeout.
func waitForSocket(path string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
