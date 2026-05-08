// Package cli implements the figaro command-line interface.
//
// The package owns subcommand dispatch, terminal rendering of streams,
// chalkboard editing helpers, and the angelus-spawn / angelus-mode
// entrypoints. The cmd/figaro binary is a thin wrapper that handles
// the hush re-exec guard and then calls Run.
package cli

import (
	"context"
	"fmt"
	"os"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

// Run dispatches a single CLI invocation. args is the process arguments
// excluding argv[0]. The function calls os.Exit on terminal errors and
// returns normally on success — callers are expected to return immediately
// after Run.
func Run(args []string) {
	// Internal flag: run as angelus supervisor.
	if len(args) > 0 && args[0] == "--angelus" {
		runAngelus()
		return
	}

	ctx := context.Background()
	loaded := mustLoadConfig()

	shutdown, err := figOtel.Init(ctx, stateDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer shutdown(ctx)
	}

	// -s / --search invokes a registered DurableDerivation by alias.
	if len(args) > 0 && (args[0] == "-s" || args[0] == "--search") {
		runSearch(loaded, args[1:])
		return
	}

	// Dispatch subcommands.
	if len(args) > 0 {
		switch args[0] {
		case "login":
			runLogin(loaded)
			return
		case "rest":
			runRest()
			return
		case "models":
			runModels(loaded)
			return
		case "list":
			runList(loaded)
			return
		case "kill":
			runKill(loaded)
			return
		case "attend":
			runAttend(loaded)
			return
		case "detach":
			runDetach(loaded)
			return
		case "label":
			runLabel(loaded)
			return
		case "context":
			runContext(loaded)
			return
		case "rehydrate":
			runRehydrate(loaded)
			return
		case "set":
			runSet(loaded)
			return
		case "unset":
			runUnset(loaded)
			return
		case "chalkboard":
			runChalkboard(loaded)
			return
		case "aria":
			runAria(loaded, args[1:])
			return
		case "qua":
			runQua(loaded, args[1:])
			return
		case "new":
			prompt := extractPrompt(args[1:])
			if prompt == "" {
				die("usage: figaro new -- <prompt>")
			}
			runNewPrompt(loaded, prompt)
			return
		case "plain":
			prompt := extractPrompt(args[1:])
			if prompt == "" {
				die("usage: figaro plain -- <prompt>")
			}
			runPlainPrompt(loaded, prompt)
			return
		case "x", "exec":
			prompt := extractPrompt(args[1:])
			if prompt == "" {
				die("usage: figaro x -- <instruction>")
			}
			runExecPrompt(loaded, args[1:], prompt)
			return
		}
	}

	// Default: prompt via existing or new figaro.
	prompt := extractPrompt(args)
	if prompt == "" {
		printUsage()
		os.Exit(1)
	}
	runPrompt(loaded, prompt)
}
