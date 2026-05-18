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
	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded)
}

// runNewPrompt creates a fresh figaro and prompts it.
func runNewPrompt(loaded *config.Loaded, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	acli.Unbind(ctx, ppid)

	figaroID, figaroEP := mustCreateAndBind(ctx, acli, loaded, ppid)
	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded)
}

// promptAria sends a prompt to a named aria.
func promptAria(loaded *config.Loaded, ariaID, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ep, err := resolveOrCreate(ctx, acli, ariaID)
	if err != nil {
		die("%s", err)
	}
	prompt = expandAtRefsForEndpoint(ctx, ep, prompt)
	mustPromptFigaro(ctx, ep, ariaID, prompt, loaded)
}

// resolveOrCreate attaches or creates a named aria.
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

	// Not found -> create; other errors -> propagate.
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
