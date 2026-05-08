package cli

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"time"

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

// runQua handles the explicit `figaro qua` subcommand.
//
//	figaro qua -- <prompt>           # same as bare `figaro -- <prompt>`
//	figaro qua <id> -- <prompt>      # one-shot send to an arbitrary aria
//
// In the targeted form, the shell's PPID binding is left untouched —
// this is a send, not an attend.
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
	runQuaTarget(loaded, targetID, prompt)
}

// runQuaTarget sends a one-shot prompt to a specific aria by id,
// without touching the shell's PPID binding.
func runQuaTarget(loaded *config.Loaded, figaroID, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	listCtx, listCancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := acli.List(listCtx)
	listCancel()
	if err != nil {
		die("list: %s", err)
	}
	found := false
	for _, f := range resp.Figaros {
		if f.ID == figaroID {
			found = true
			break
		}
	}
	if !found {
		die("no figaro with id %q", figaroID)
	}

	ep := transport.UnixEndpoint(filepath.Join(angelusRuntimeDir(), "figaros", figaroID+".sock"))
	mustPromptFigaro(ctx, ep, figaroID, prompt, loaded)
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
