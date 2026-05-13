// Package cli implements the figaro command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

// Run dispatches a CLI invocation.
func Run(args []string) {
	// Internal: angelus mode.
	if os.Getenv("_FIGARO_DAEMON") == "1" || (len(args) > 0 && args[0] == "--angelus") {
		runAngelus()
		return
	}

	// --version / -V pre-empt the router so they need no config or session.
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-V":
			runVersion()
			return
		}
	}

	ctx := context.Background()
	loaded := mustLoadConfig()

	shutdown, err := figOtel.Init(ctx, stateDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer shutdown(ctx)
	}

	router := buildRouter(loaded)

	// Bare `figaro -- <prompt>` defaults to prompt verb.
	if prompt := extractPrompt(args); prompt != "" {
		if len(args) == 0 || !router.HasCommand(args[0]) {
			runPrompt(loaded, prompt)
			return
		}
	}

	code := router.Run(args)
	os.Exit(code)
}

func buildRouter(loaded *config.Loaded) *cmdkit.Router {
	r := cmdkit.NewRouter("figaro")
	r.Extra = loaded





	r.Register(&cmdkit.Command{
		Name:    "aria",
		Aliases: []string{"qua"},
		Group:   "Prompt",
		Short:   "Prompt or view history of an aria",
		Usage:   "aria [<id>] -- <prompt>  |  aria [<id>] [N] [-v] [-l] [-a]",
		Long: `Prompt a named or pid-bound aria, or render its history.

  figaro aria -- <prompt>          prompt the pid-bound aria
  figaro aria myid -- <prompt>     prompt a named aria (create if absent)
  figaro aria 20 -v                show last 20 messages with verbose info
  figaro aria -a -l                show all messages in literal (no markdown)`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runAria(ld, ctx.RawArgs)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "new",
		Group: "Prompt",
		Short: "Start a fresh aria and prompt it",
		Usage: "new -- <prompt>",
		Long:  "Creates a new aria (with server-generated id), binds it to this shell, and sends the prompt.",
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			prompt := extractPrompt(ctx.RawArgs)
			if prompt == "" {
				return fmt.Errorf("usage: figaro new -- <prompt>")
			}
			runNewPrompt(ld, prompt)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "plain",
		Aliases: []string{"l"},
		Group:   "Prompt",
		Short:   "Raw ephemeral prompt (pipe-friendly, no formatting)",
		Usage:   "plain -- <prompt>",
		Long:    "Creates an ephemeral aria, streams the response verbatim to stdout\n(no ANSI, no markdown rendering), then kills the aria. Ideal for piping.",
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			prompt := extractPrompt(ctx.RawArgs)
			if prompt == "" {
				return fmt.Errorf("usage: figaro plain -- <prompt>")
			}
			runPlainPrompt(ld, prompt)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "x",
		Aliases: []string{"exec"},
		Group:   "Prompt",
		Short:   "Generate bash from instruction and execute it",
		Usage:   "x [-n|-y] -- <instruction>",
		Long: `Ask figaro to write a bash script for the given instruction,
then execute it locally via bash -c.

Flags:
  -n, --dry-run    Print the script to stdout instead of executing
  -y, --yes        Skip the confirmation prompt`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			prompt := extractPrompt(ctx.RawArgs)
			if prompt == "" {
				return fmt.Errorf("usage: figaro x -- <instruction>")
			}
			runExecPrompt(ld, ctx.RawArgs, prompt)
			return nil
		},
	})



	r.Register(&cmdkit.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Group:   "Session",
		Short:   "List all arias (live and dormant)",
		Usage:   "list",
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runList(ld)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "attend",
		Group:   "Session",
		Short:   "Bind this shell to an existing aria",
		Usage:   "attend <id>",
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runAttendByID(ld, ctx.Args[0])
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "detach",
		Group: "Session",
		Short: "Unbind this shell from its current aria",
		Usage: "detach",
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runDetach(ld)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "kill",
		Group:   "Session",
		Short:   "Terminate and remove an aria",
		Usage:   "kill <id>",
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runKillByID(ld, ctx.Args[0])
			return nil
		},
	})



	r.Register(&cmdkit.Command{
		Name:    "state",
		Aliases: []string{"chalkboard"},
		Group:   "State",
		Short:   "Show the current chalkboard snapshot",
		Usage:   "state",
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runChalkboard(ld)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "set",
		Group:   "State",
		Short:   "Patch a chalkboard key (no LLM round-trip)",
		Usage:   "set <key> <value>",
		ArgsMin: 2,
		ArgsMax: 2,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runSetArgs(ld, ctx.Args[0], ctx.Args[1])
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "unset",
		Group:   "State",
		Short:   "Remove chalkboard key(s)",
		Usage:   "unset <key> [<key>...]",
		ArgsMin: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runUnsetArgs(ld, ctx.Args)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "rehydrate",
		Group: "State",
		Short: "Re-run credo and apply state diff",
		Usage: "rehydrate [--dry-run]",
		Flags: []cmdkit.FlagDef{
			{Long: "dry-run", Short: "n", IsBool: true, Description: "Print diff without applying"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runRehydrateWithFlag(ld, ctx.BoolFlag("dry-run"))
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "derive",
		Aliases: []string{"search"},
		Group:   "State",
		Short:   "Read a registered durable derivation",
		Usage:   "derive <alias> [--json]",
		Long:    "Reads a DurableDerivation off disk and prints it.\nWith no alias, lists available derivations.\n\nExamples:\n  figaro derive meta      # context/usage snapshot used by `list` and `status`\n  figaro derive usage     # message + token totals\n  figaro derive summary   # top-level aria meta",
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runSearch(ld, ctx.RawArgs)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "status",
		Aliases: []string{"info"},
		Group:   "Session",
		Short:   "Show a focused view of one aria",
		Usage:   "status [<id>]",
		Long:    "Prints provider, model, message count, context size and last-active\ntime for the named aria (or the one bound to this shell). Reads the\nsame data the `list` table uses; dormant arias are backfilled from\nderived/meta.json (see `figaro derive meta`).",
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runStatus(ld, ctx.RawArgs)
			return nil
		},
	})



	r.Register(&cmdkit.Command{
		Name:  "login",
		Group: "System",
		Short: "OAuth login for a provider",
		Usage: "login <provider>",
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runLoginByName(ld, ctx.Args[0])
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "models",
		Group: "System",
		Short: "List available provider models",
		Usage: "models",
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runModels(ld)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "stop",
		Aliases: []string{"rest"},
		Group:   "System",
		Short:   "Shut down the angelus daemon",
		Usage:   "stop [--force]",
		Flags: []cmdkit.FlagDef{
			{Long: "force", Short: "f", IsBool: true, Description: "SIGKILL instead of graceful shutdown"},
			{Long: "keep-pids", Short: "k", IsBool: true, Description: "Persist PID bindings before stopping"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runRestWithFlags(ctx.BoolFlag("force"), ctx.BoolFlag("keep-pids"))
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "version",
		Aliases: []string{"v"},
		Group:   "System",
		Short:   "Print build identity (revision, exe path, Go version)",
		Usage:   "version",
		Run: func(ctx *cmdkit.RunContext) error {
			runVersion()
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "completion",
		Group: "System",
		Short: "Generate shell completion script",
		Usage: "completion <bash|zsh|fish>",
		Long:  "Print a shell completion script to stdout.\nSource it in your shell profile:\n  eval \"$(figaro completion bash)\"",
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			return r.WriteCompletion(os.Stdout, cmdkit.CompletionShell(ctx.Args[0]))
		},
	})

	return r
}
