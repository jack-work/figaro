// Package cli implements the figaro command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

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

	// Compute binding policy (interactive? --no-bind? env?) once, before
	// the router dispatches. Consulted by every command that would
	// otherwise look up the pid-binding.
	initBindingPolicy()
	args = extractNoBindFlag(args)

	shutdown, err := figOtel.Init(ctx, stateDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer shutdown(ctx)
	}

	// Passive update nudge (config-gated, TTY-only, cached).
	// Never fatal; runs sync but bounded to a short timeout.
	// Skip for meta verbs where the nudge would be noise or duplicative.
	if !isMetaVerb(args) {
		runUpdateCheck(loaded)
	}

	router := buildRouter(progName, loaded)

	// Bare `figaro -- <prompt>` defaults to prompt verb.
	if prompt := extractPrompt(args); prompt != "" {
		if len(args) == 0 || !router.HasCommand(args[0]) {
			runPrompt(loaded, prompt, renderSettings{})
			return
		}
	}

	code := router.Run(args)
	os.Exit(code)
}

// figaro:
// There has to be a better way to maintain these, like in declarative configurations perhaps.
// Evaluate the necessity and the churn in the source's version history.
func buildRouter(progName string, loaded *config.Loaded) *cmdkit.Router {
	r := cmdkit.NewRouter(progName)
	r.Extra = loaded

	r.Register(&cmdkit.Command{
		Name:    "show",
		Aliases: []string{"history"},
		Group:   "Prompt",
		Short:   "Render an aria's message history",
		Usage:   "show [<id>] [-n N | --from A [--to B] | -a] [-j] [-v] [-l]",
		Long: `Render an aria's history as conversational units (the prompt and
each agent turn). The optional positional is the target aria id;
default is the pid-bound aria. Everything else is a flag. Units are
labeled by their figaro LT (the coordinate send/fork <id>:<LT> target).

  figaro show                      last 10 units of the bound aria
  figaro show eac16fef             last 10 units of aria eac16fef
  figaro show -n 20                last 20 units
  figaro show eac16fef -n 20       last 20 units of eac16fef
  figaro show --from 4             units 4..end ("after index 4")
  figaro show --from 1 --to 3      units 1..3 inclusive
  figaro show -a                   every unit
  figaro show -j                   units as raw JSON (materialized, no deltas)
  figaro show eac16fef -v          verbose IR
  figaro show -l                   raw IR, no rendering`,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (alias for the positional)"},
			{Long: "verbose", Short: "v", IsBool: true, Description: "Raw IR with patches, thinking, usage, transitions"},
			{Long: "literal", Short: "l", IsBool: true, Description: "No ANSI / markdown rendering"},
			{Long: "all", Short: "a", IsBool: true, Description: "Show every unit, not just last N"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit units as raw JSON (no delta compression)"},
			{Long: "from", Description: "Start unit index (inclusive)"},
			{Long: "to", Description: "End unit index (inclusive)"},
			{Long: "last", Short: "n", Description: "Show the last N units"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			// Positional (or --id) is the aria; everything else is a flag.
			ariaID := ctx.Flag("id")
			if ariaID == "" && len(ctx.Args) > 0 {
				ariaID = ctx.Args[0]
			}
			// renderAria has its own flag parser; reassemble the parsed flags.
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
			if ctx.BoolFlag("json") {
				args = append(args, "-j")
			}
			if v := ctx.Flag("from"); v != "" {
				args = append(args, "--from", v)
			}
			if v := ctx.Flag("to"); v != "" {
				args = append(args, "--to", v)
			}
			if v := ctx.Flag("last"); v != "" {
				args = append(args, "--last", v)
			}
			runShow(ld, ariaID, args)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "send",
		Aliases: []string{"qua"},
		Group:   "Prompt",
		Short:   "Send a prompt to an aria",
		Usage:   "send [--id <id>] [-e] [-r] [-v] [-o] [-x] [-n] [-y] [-f] [-j] -- <prompt>",
		Long: `Send a prompt to an aria. Without --id, targets the pid-bound
aria (creating one if this shell has no binding). With --id, targets
the named aria, which must already exist (aria ids are system-minted).

Persistence (--ephemeral) and formatting (--raw) are orthogonal.

Flags:
  --id <id>      Target a specific existing aria
  -e, --ephemeral
                 Spin a one-shot in-memory aria; kill it on completion.
                 Contradicts --id. Says nothing about formatting.
  -r, --raw      Stream verbatim to stdout: no ANSI, no markdown.
                 Pipe-friendly. Says nothing about persistence.
  -v, --verbatim Dump the raw wire frames as JSON (one {"method","params"}
                 per line) — the literal protocol stream, no formatting,
                 no delta application.
  -o, --verbose  Verbose: expand full tool inputs (else truncated). Thinking
                 blocks are always shown (muted). Ctrl-O (or Ctrl-T) toggles live.
  -x, --exec     Treat the prompt as a bash instruction. The reply is
                 piped to bash -c. --raw is silently ignored here
                 because the script governs its own output.
  -n, --dry-run  --exec only: print the script without running it.
  -y, --yes      --exec only: skip the confirmation prompt.
  -f, --forget   Submit the prompt and exit immediately. Do not attach
                 to the stream; do not send figaro.interrupt on Ctrl-C.
                 Use ` + "`figaro listen <id>`" + ` later to follow.
  -j, --json     Emit a single {"aria_id":..., "mode":...} JSON line on
                 stdout. With --forget: fire, then print. With <id>:<LT>:
                 fork, then print (mode="fork-send").

Keys while streaming:
  Ctrl-C         Interrupt the turn (sends figaro.interrupt).
  Ctrl-D         Disconnect this CLI; leave the turn running.
  Ctrl-T         Open the full-screen transcript pager.
  Ctrl-O         Toggle verbose tool-input expansion.

  figaro send -- <prompt>              prompt the pid-bound aria, rich
  figaro send --id myid -- <prompt>    prompt a named aria (rich)
  figaro send -r -- <prompt>           bound aria, raw stream
  figaro send -e -- <prompt>           ephemeral, rich
  figaro send -er -- <prompt>          ephemeral + raw (was: ` + "`figaro plain`" + `)
  figaro send -ex -y -- <instruction>  ephemeral exec, no confirmation
  figaro send -f --id myid -- <prompt> fire-and-forget; do not stream`,
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
		Usage:   "new [-j|--json] -- <prompt>",
		Long:    "Creates a new aria (with server-generated id), binds it to this shell, and sends the prompt.\n-j/--json emits {aria_id, mode:'new'} on stdout instead of the streaming render.",
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			prompt := extractPrompt(ctx.RawArgs)
			if prompt == "" {
				return fmt.Errorf("usage: figaro new -- <prompt>")
			}
			asJSON := hasPreDashFlag(ctx.RawArgs, "--json", "-j")
			runNewPrompt(ld, prompt, renderSettings{jsonMode: asJSON})
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
		Name:    "listen",
		Group:   "Prompt",
		Short:   "Attach to an aria's live stream without sending a prompt",
		Usage:   "listen [<id>]",
		Long: `Attach to an aria's live stream. Same view as a send mid-stream:
catches up to the committed cursor, follows live frames, and supports
Ctrl-T transcript mode — just without calling figaro.qua. Stays open
until you close it.

With no id, the pid-bound aria is used.

Keys:
  Ctrl-C   Interrupt the in-flight turn (like in send).
  Ctrl-D   Disconnect this CLI; the turn keeps running.
  Ctrl-T   Open the full-screen transcript pager.
  Ctrl-O   Toggle verbose tool-input expansion.
  q / Esc  (in pager) leave pager and return to the inline tail.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			var id string
			if len(ctx.Args) > 0 {
				id = ctx.Args[0]
			}
			runListen(ld, id)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "hup",
		Group:   "Prompt",
		Short:   "Hang up: interrupt an aria's current turn",
		Usage:   "hup [<id>]",
		Long: `Send figaro.interrupt to a trunk — the same RPC Ctrl-C inside a
send stream fires. The agent aborts whatever turn is in flight.
With no id, the pid-bound aria is used.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			var id string
			if len(ctx.Args) > 0 {
				id = ctx.Args[0]
			}
			runHup(ld, id)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Group:   "Session",
		Short:   "List arias — scoped to where you're attended (attend is `cd`)",
		Usage:   "list [<id>] [-h|--home | -g|--global] [-a|--all | -n <count>] [-j|--json]",
		Long: "Lists arias `ls`-style relative to where you're attended (attend is\nthe `cd`).\n\n" +
			"Scope:\n" +
			"  (default)     attended → your conversation's tree (● = you);\n" +
			"                detached → home (all top-level arias)\n" +
			"  <id>          that aria's subtree\n" +
			"  -h, --home    the home view (all top-level arias) without unbinding\n" +
			"  -g, --global  home plus the null + loadout anchors (the full tree)\n\n" +
			"Cap (mutually exclusive):\n" +
			"  (default)     10 most-recently-used\n" +
			"  -a, --all     no cap\n" +
			"  -n <count>    cap to <count>\n\n" +
			"  -j, --json    pro/dev: every aria incl. null + loadouts as JSON;\n" +
			"                takes no other flags",
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "home", Short: "h", IsBool: true, Description: "Home view: all top-level arias, without unbinding"},
			{Long: "global", Short: "g", IsBool: true, Description: "Full hierarchy incl. the null + loadout anchors"},
			{Long: "all", Short: "a", IsBool: true, Description: "Show all (remove the 10-most-recent cap)"},
			{Long: "limit", Short: "n", Description: "Cap to N rows (default 10)"},
			{Long: "json", Short: "j", IsBool: true, Description: "Pro/dev: all arias (incl. anchors) as JSON; no other flags"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			o := lsOpts{
				jsonOut: ctx.BoolFlag("json"),
				home:    ctx.BoolFlag("home"),
				global:  ctx.BoolFlag("global"),
				limit:   10,
			}
			if len(ctx.Args) > 0 {
				o.rootID = ctx.Args[0]
			}
			hasN := ctx.Flag("limit") != ""
			if o.jsonOut && (o.home || o.global || ctx.BoolFlag("all") || hasN || o.rootID != "") {
				die("ls --json is the global escape hatch and takes no other flags")
			}
			if ctx.BoolFlag("all") && hasN {
				die("ls: -a/--all and -n are mutually exclusive")
			}
			if o.home && o.global {
				die("ls: -h/--home and -g/--global are mutually exclusive")
			}
			if ctx.BoolFlag("all") {
				o.limit = 0
			} else if hasN {
				if n, err := strconv.Atoi(ctx.Flag("limit")); err == nil && n > 0 {
					o.limit = n
				}
			}
			runList(ld, o)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "attend",
		Aliases: []string{"at"},
		Group:   "Session",
		Short:   "Bind this shell to an existing aria (optionally at an LT)",
		Usage:   "attend <id> | <id>:<LT> | :<LT> | null",
		Long:    "Binds this shell to an aria. With :<LT> the binding carries a pending\nfork-point — the next bare prompt (`fig -- …`) forks the trunk there and\nmoves to the new branch. `:<LT>` alone re-pins the already-bound aria.\n\n`attend null` goes home: drops this shell's binding (named for the kindNull\ngenesis root). New conversations then default to the live loadout.",
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runAttend(ld, ctx.Args[0])
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "fork",
		Group: "Session",
		Short: "Branch a conversation: freeze it, mint two children",
		Usage: "fork [--id <id> | <id>[:<LT>]] [--stay]",
		Long: `Branch a conversation. The target freezes (its id becomes a
read-only index node) and two fresh children are minted: the
continuation (the original line) and an empty alternative.

  figaro fork                 branch the bound aria at its head
  figaro fork <id>            branch another aria at its head (maintenance)
  figaro fork <id>:42         interior fork — history below LT 42 is shared,
                              the original suffix becomes the continuation
  figaro fork --stay          branch but do not rebind this shell

Forking your own bound aria rebinds this shell to the continuation
(same trunk/mantra, new id) since the bound aria just froze. Forking
any other aria, or passing --stay, leaves your session untouched.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (defaults to this shell's); :<LT> for an interior fork"},
			{Long: "stay", IsBool: true, Description: "Do not rebind this shell to the continuation"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit machine-readable result on stdout (parent, continuation, alternative, ...)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runFork(ld, ctx.Flag("id"), ctx.Args, ctx.BoolFlag("stay"), ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "promote",
		Group: "Session",
		Short: "Make a trunk the canonical line through its ancestors",
		Usage: "promote [--id <id> | <id>] [levels]",
		Long: `Promote a conversation trunk: it climbs up its ancestry, absorbing
each parent trunk's run so it becomes the canonical line. Pure
relabeling — no data moves, ids are stable, your binding is untouched.

  figaro promote              promote the bound aria one level
  figaro promote <id>         promote another aria one level
  figaro promote <id> 10      climb up to 10 stump-bounded levels

Promotion stops at the loadout boundary: a top-level conversation has
nothing to promote into ("cannot promote into a loadout").`,
		ArgsMin: 0,
		ArgsMax: 2,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (defaults to this shell's)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runPromote(ld, ctx.Flag("id"), ctx.Args)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "kill",
		Group:   "Session",
		Short:   "Terminate and remove a trunk",
		Usage:   "kill [--id <trunk> | <trunk>] [--recursive]",
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target trunk id"},
			{Long: "recursive", Short: "r", IsBool: true, Description: "Also remove the trunk's live branches"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runKill(ld, ctx.Flag("id"), ctx.Args, ctx.BoolFlag("recursive"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "state",
		Aliases: []string{"chalkboard"},
		Group:   "State",
		Short:   "Show the current chalkboard snapshot",
		Usage:   "state [<id> | --id <id>] [-j]",
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit the snapshot as a JSON object"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			id := ctx.Flag("id")
			if id == "" && len(ctx.Args) > 0 {
				id = ctx.Args[0]
			}
			runChalkboard(ld, id, ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
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
		Name:    "loadout",
		Group:   "State",
		Short:   "Apply a named loadout additively to an aria",
		Usage:   "loadout [--id <id>] <name> | loadout --list",
		Long:    "Loads ~/.config/figaro/loadouts/<name>.toml and applies it as an\nadditive chalkboard patch: keys whose values match the current\nsnapshot are skipped, and no keys are ever removed.\n\nExamples:\n  figaro loadout focus            # apply 'focus' loadout to the bound aria\n  figaro loadout --id myid focus  # apply to a specific aria\n  figaro loadout --list           # show available loadouts",
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
			{Long: "list", IsBool: true, Description: "List available loadouts and exit"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			if ctx.BoolFlag("list") {
				runLoadoutList(ld)
				return nil
			}
			if len(ctx.Args) == 0 {
				die("usage: figaro loadout [--id <id>] <name>")
			}
			runLoadout(ld, ctx.Flag("id"), ctx.Args[0])
			return nil
		},
		CompleteArgs: completeLoadouts,
	})

	r.Register(&cmdkit.Command{
		Name:    "status",
		Aliases: []string{"info"},
		Group:   "Session",
		Short:   "Show a focused view of one aria",
		Usage:   "status [<id> | --id <id>] [-m] [-j]",
		Long:    "Prints provider, model, message count, context size and last-active\ntime for the named aria (or the one bound to this shell). Reads the\nsame data the `list` table uses; dormant arias are backfilled from the\nmeta derivation.\n\n  -m/--more   also surface the derived/extra detail (mantra, cwd,\n              loadout version, fork origin, created)\n  -j/--json   emit the full status as JSON (combine: -mj)",
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id"},
			{Long: "more", Short: "m", IsBool: true, Description: "Surface derived/extra detail"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit the full status as JSON"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runStatus(ld, ctx.Flag("id"), ctx.Args, ctx.BoolFlag("more"), ctx.BoolFlag("json"))
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
		Name:  "update",
		Group: "System",
		Short: "Check for a newer figaro release",
		Usage: "update [--check] [--json] [--apply]",
		Long: `Ask the Go module proxy for the latest tagged figaro release,
compare it against this binary, and tell you how to upgrade — respecting
whichever install channel you used (` + "`go install`" + `, Nix, dev shell).

By default this is *advisory* — figaro never rewrites its own binary
unless you pass --apply and are on the go-install channel.

  figaro update                current vs. latest, cached
  figaro update --check        force a fresh network check
  figaro update --json         machine-readable output
  figaro update --apply        (go-install only) shell out to
                               ` + "`go install …@vX.Y.Z`" + ` for you

The passive one-line startup nudge is controlled by
` + "`check_updates`" + ` in ~/.config/figaro/config.toml (default true);
the cache TTL is ` + "`update_check_ttl_hours`" + ` (default 24).
This command itself is always available regardless of those settings.`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			return runUpdate(ld, ctx.RawArgs)
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
