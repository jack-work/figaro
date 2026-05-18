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

// Run dispatches a CLI invocation. progName is the basename of argv[0]
// (e.g. "figaro" or "fig"); it threads through to the router so help,
// errors, and shell completion reflect the name the user actually typed.
func Run(progName string, args []string) {
	if progName == "" {
		progName = "figaro"
	}

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

	// __complete is the hidden dispatcher for shell autocompletion.
	// Skip otel init and tolerate config errors: completion must be
	// cheap and never appear broken.
	if len(args) > 0 && args[0] == "__complete" {
		loaded, _ := config.Load(config.DefaultConfigDir())
		os.Exit(buildRouter(progName, loaded).Run(args))
	}

	ctx := context.Background()
	loaded := mustLoadConfig()

	shutdown, err := figOtel.Init(ctx, stateDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer shutdown(ctx)
	}

	router := buildRouter(progName, loaded)

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

func buildRouter(progName string, loaded *config.Loaded) *cmdkit.Router {
	r := cmdkit.NewRouter(progName)
	r.Extra = loaded

	r.Register(&cmdkit.Command{
		Name:    "show",
		Aliases: []string{"history"},
		Group:   "Prompt",
		Short:   "Render an aria's message history",
		Usage:   "show [--id <id>] [N] [-v] [-l] [-a]",
		Long: `Render the message history of an aria. Defaults to the last 10
messages of the pid-bound aria; --id scopes to a different aria.

  figaro show                      last 10 of the bound aria
  figaro show 20                   last 20 of the bound aria
  figaro show --id myid 50 -v      last 50 of myid, verbose
  figaro show --id myid -a -l      all messages of myid, literal output`,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
			{Long: "verbose", Short: "v", IsBool: true, Description: "Include patches, thinking, usage"},
			{Long: "literal", Short: "l", IsBool: true, Description: "No ANSI / markdown rendering"},
			{Long: "all", Short: "a", IsBool: true, Description: "Show every message, not just last N"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			// renderAria parses -v/-l/-a/N from argv. Reassemble what
			// the router parsed back into its expected shape.
			var args []string
			if ctx.BoolFlag("verbose") {
				args = append(args, "-v")
			}
			if ctx.BoolFlag("literal") {
				args = append(args, "-l")
			}
			if ctx.BoolFlag("all") {
				args = append(args, "-a")
			}
			args = append(args, ctx.Args...)
			runShow(ld, ctx.Flag("id"), args)
			return nil
		},
		CompleteArgs: completeAriaIDsAfterFlag(nil),
	})

	r.Register(&cmdkit.Command{
		Name:    "send",
		Aliases: []string{"qua"},
		Group:   "Prompt",
		Short:   "Send a prompt to an aria",
		Usage:   "send [--id <id>] [-e|--ephemeral] [-r|--raw] [-x|--exec] [-n] [-y] -- <prompt>",
		Long: `Send a prompt to an aria. Without --id, targets the pid-bound
aria (creating one if this shell has no binding). With --id, targets
the named aria, creating it if it does not yet exist.

Persistence (--ephemeral) and formatting (--raw) are orthogonal.

Flags:
  --id <id>      Target a specific aria (create if missing)
  -e, --ephemeral
                 Spin a one-shot in-memory aria; kill it on completion.
                 Contradicts --id. Says nothing about formatting.
  -r, --raw      Stream verbatim to stdout: no ANSI, no markdown.
                 Pipe-friendly. Says nothing about persistence.
  -x, --exec     Treat the prompt as a bash instruction. The reply is
                 piped to bash -c. --raw is silently ignored here
                 because the script governs its own output.
  -n, --dry-run  --exec only: print the script without running it.
  -y, --yes      --exec only: skip the confirmation prompt.

  figaro send -- <prompt>              prompt the pid-bound aria, rich
  figaro send --id myid -- <prompt>    prompt a named aria (rich)
  figaro send -r -- <prompt>           bound aria, raw stream
  figaro send -e -- <prompt>           ephemeral, rich
  figaro send -er -- <prompt>          ephemeral + raw (was: ` + "`figaro plain`" + `)
  figaro send -ex -y -- <instruction>  ephemeral exec, no confirmation`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runSend(ld, ctx.RawArgs)
			return nil
		},
		CompleteArgs: completePromptOrIDFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "new",
		Group:   "Prompt",
		Short:   "Start a fresh aria and prompt it",
		Usage:   "new -- <prompt>",
		Long:    "Creates a new aria (with server-generated id), binds it to this shell, and sends the prompt.",
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
		CompleteArgs: completePromptOrIDFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "plain",
		Aliases: []string{"l"},
		Group:   "Prompt",
		Short:   "(deprecated) Raw prompt — use `send -er` / `send -r --id <id>`",
		Usage:   "plain [--id <id>] -- <prompt>",
		Long:    "Deprecated. Without --id, equivalent to `figaro send -er`\n(ephemeral + raw). With --id, equivalent to `figaro send -r --id <id>`.\nWill be removed in a future release.",
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			fmt.Fprintln(os.Stderr, "figaro plain: deprecated; use `figaro send -er` (ephemeral+raw) or `figaro send -r --id <id>` (named, raw) instead.")
			runPlainPrompt(ld, ctx.RawArgs)
			return nil
		},
		CompleteArgs: completePromptOrIDFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "x",
		Aliases: []string{"exec"},
		Group:   "Prompt",
		Short:   "(deprecated) Bash exec — use `send -x` / `send -ex`",
		Usage:   "x [--id <id>] [-n|-y] -- <instruction>",
		Long: `Deprecated. Equivalent to ` + "`figaro send --exec`" + `; bare ` + "`figaro x`" + ` is
` + "`figaro send -ex`" + `. Will be removed in a future release.`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			fmt.Fprintln(os.Stderr, "figaro x: deprecated; use `figaro send -x` (or `-ex` for ephemeral exec) instead.")
			runExecPrompt(ld, ctx.RawArgs)
			return nil
		},
		CompleteArgs: completePromptOrIDFlag,
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
		CompleteArgs: completeAriaIDsPositionalOrFlag,
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
		Usage:   "kill [--id <id> | <id>]",
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runKill(ld, ctx.Flag("id"), ctx.Args)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "state",
		Aliases: []string{"chalkboard"},
		Group:   "State",
		Short:   "Show the current chalkboard snapshot",
		Usage:   "state [--id <id>]",
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runChalkboard(ld, ctx.Flag("id"))
			return nil
		},
		CompleteArgs: completeAriaIDsAfterFlag(nil),
	})

	r.Register(&cmdkit.Command{
		Name:    "set",
		Group:   "State",
		Short:   "Patch a chalkboard key (no LLM round-trip)",
		Usage:   "set [--id <id>] <key> <value>",
		ArgsMin: 2,
		ArgsMax: 2,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runSetArgs(ld, ctx.Flag("id"), ctx.Args[0], ctx.Args[1])
			return nil
		},
		CompleteArgs: completeAriaIDsAfterFlag(completeChalkboardKeys),
	})

	r.Register(&cmdkit.Command{
		Name:    "unset",
		Group:   "State",
		Short:   "Remove chalkboard key(s)",
		Usage:   "unset [--id <id>] <key> [<key>...]",
		ArgsMin: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runUnsetArgs(ld, ctx.Flag("id"), ctx.Args)
			return nil
		},
		CompleteArgs: completeAriaIDsAfterFlag(completeChalkboardKeys),
	})

	r.Register(&cmdkit.Command{
		Name:  "rehydrate",
		Group: "State",
		Short: "Re-run credo and apply state diff",
		Usage: "rehydrate [--id <id>] [--dry-run]",
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
			{Long: "dry-run", Short: "n", IsBool: true, Description: "Print diff without applying"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runRehydrateWithFlag(ld, ctx.Flag("id"), ctx.BoolFlag("dry-run"))
			return nil
		},
		CompleteArgs: completeAriaIDsAfterFlag(nil),
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
		Usage:   "status [--id <id> | <id>]",
		Long:    "Prints provider, model, message count, context size and last-active\ntime for the named aria (or the one bound to this shell). Reads the\nsame data the `list` table uses; dormant arias are backfilled from\nderived/meta.json (see `figaro derive meta`).",
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runStatus(ld, ctx.Flag("id"), ctx.Args)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "login",
		Group:   "System",
		Short:   "OAuth login for a provider",
		Usage:   "login <provider>",
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
		Short: "Generate or install a shell completion script",
		Usage: "completion <bash|zsh|fish>  |  completion install [<shell>]",
		Long: `Print a completion script to stdout, or install it where the shell will
auto-load it on the next tab.

  figaro completion bash               # print bash script to stdout
  figaro completion install            # auto-detect $SHELL, write to autoload path
  figaro completion install fish       # explicit shell`,
		ArgsMin: 1,
		ArgsMax: 2,
		Run: func(ctx *cmdkit.RunContext) error {
			first := ctx.Args[0]
			if first == "install" {
				shell := ""
				if len(ctx.Args) > 1 {
					shell = ctx.Args[1]
				}
				return runCompletionInstall(r, shell)
			}
			if len(ctx.Args) > 1 {
				return fmt.Errorf("usage: completion <shell> | completion install [<shell>]")
			}
			return r.WriteCompletion(os.Stdout, cmdkit.CompletionShell(first))
		},
	})

	// Bare-prompt completion: when the user invokes `figaro -- <body>`
	// (or an alias such as `q ` expanding to it), the cursor in <body>
	// should pull from the prompt-context pool, not the verb list.
	r.SetBarePromptComplete(completePromptContext)

	return r
}
