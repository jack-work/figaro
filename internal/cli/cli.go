// Package cli implements the figaro command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
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

	// AMBIGUOUS-WIDTH GLYPHS: believe the terminal, not the default table.
	//
	// U+2500 (─) and U+2502 (│) are East Asian AMBIGUOUS: one cell in most
	// terminals, TWO where ambiguous-wide is configured. Every rule figaro draws
	// is made of ─ and every thinking gutter of │, and every row is built on
	// go-runewidth's answer of ONE. On an ambiguous-wide terminal each of those
	// rows is drawn twice as wide as figaro measured it, so a full-width rule
	// lands at 200 cells in a 100-column pane and runs off the edge.
	//
	// Measured on the same captured output at width 100: zero rows over as a
	// normal terminal, 48 rows over as an ambiguous-wide one, worst +100. That
	// is invisible to every test that measures figaro against figaro, which is
	// why three rounds of sweeps came back clean while the report stood.
	//
	// One switch, consulted once, and it reaches every measurement: the CLI's
	// displayWidth, render's cells, livelog's clip and hardWrap all resolve
	// through go-runewidth's DefaultCondition.
	//
	//	FIGARO_AMBIGUOUS_WIDE=1   ─ │ … are two cells, as your terminal draws them
	//
	// scripts/term-ambiwidth.sh asks your terminal which it is.
	applyAmbiguousWidth()

	// Arm the console we WRITE to, before the first escape leaves the process.
	//
	// On Windows nothing ever enabled ENABLE_VIRTUAL_TERMINAL_PROCESSING on
	// stdout, so figaro's escapes were honoured only where something else had
	// already turned it on: measured at stdout mode 0x0003 under a bare conhost
	// (inert) against 0x0007 under Windows Terminal. Where it is off,
	// \x1b[?1049h does nothing and the pager's frames land in the PRIMARY
	// buffer as ordinary text: the transcript dumped into the user's
	// scrollback. It rides here rather than inside MakeRaw because MakeRaw is
	// about the console we READ and only runs on an interactive path, while
	// the first escapes (autowrapOff+cursorHide) are written before any raw
	// session exists and `figaro show` renders ANSI without one at all.
	//
	// Off Windows this is a no-op. A redirected stdout degrades to a no-op
	// too: `figaro list -j | jq` is the ordinary case, not an error.
	// OnceFunc because both paths fire on a normal return: the defer runs, and
	// then exitNow's hooks would run it again. (No nil check: neither platform
	// returns one: Windows hands back a no-op restore when there is no console
	// to arm, and off Windows the whole function is a no-op.)
	restoreConsole := sync.OnceFunc(term.ArmOutput())
	defer restoreConsole()
	atExit(restoreConsole)

	// Repair the console we READ, before the first prompt asks it for a line.
	// figaro clears line-editing, echo and Ctrl-C processing for every
	// interactive session, and a session killed without unwinding leaves them
	// clear for every process that touches that console afterwards: PSReadLine
	// saves and restores whatever it finds, so the damage outlives the shell
	// prompt and the next figaro's MakeRaw dutifully saves RAW as the mode to
	// return to. In that state a prompt echoes nothing and Enter sends a bare
	// \r. Unlike the output arming this takes NO restore: what it replaces is
	// wreckage, and handing it back is how the wreckage spreads. A no-op off
	// Windows and on a redirected stdin.
	term.SanitizeInput()

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
		if loaded != nil {
			if s, err := loaded.RefSigil(); err == nil {
				SetRefSigil(s)
			}
		}
		// The binding policy must be computed HERE too: this branch
		// exits before the main path's initBindingPolicy(), and the
		// live-key completers resolve the shell's aria through
		// shellPID. Left at zero, the daemon is asked which aria pid 0
		// attends, answers not-found, and completion falls back to the
		// well-known catalog: which is how every pid-bound shell got
		// a static key list with its aria's skills.* silently missing,
		// while FIGARO_ARIA shell-outs (which skip the pid) worked.
		initBindingPolicy()
		os.Exit(buildRouter(progName, loaded).Run(args))
	}

	ctx := context.Background()
	loaded := mustLoadConfig()

	// Apply config-driven sigil for form references.
	if sigil, err := loaded.RefSigil(); err != nil {
		die("%s", err)
	} else {
		SetRefSigil(sigil)
	}

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

	// Update nudge: help surfaces only (config-gated, TTY-only, cached).
	// It used to fire on every command, which interleaved with real output;
	// now it rides along with the help text (and the transcript's '?' panel).
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		runUpdateCheck(loaded)
	}

	router := buildRouter(progName, loaded)

	// Bare `figaro [send-flags] -- <prompt>` is the prompt verb: same parser,
	// same semantics as `figaro send` (see bare.go). The `--` boundary is
	// mandatory, and args[0] must not name a command.
	if isBareForm(args, router.HasCommand) {
		runBarePrompt(progName, router, loaded, args)
		return
	}

	code := router.Run(args)
	os.Exit(code)
}

// buildRouter is the whole command surface, declared once. Every command is a
// cmdkit.Command value here; the router owns dispatch, help, arg counts and
// completion, so a Command declares what it accepts and Run does the work.
//
// Two shapes, one field apart:
//
//   - Parsed (default). Flags is the table. The router parses argv, expands
//     short bundles (a value-taking short ends the bundle: -erOsonn5 is
//     -e -r -O sonn5), refuses unknown flags, enforces ArgsMin/ArgsMax.
//     `state`, `set`, `attend`, `gc`, `kill`.
//   - PassRaw. Run gets the untouched tail, for grammars the router cannot
//     express: everything after `--` is a prompt and must not be inspected,
//     and a positional may be <id>:<turn>. `send`, `new`, `fork`: sharing one
//     parser (extractPromptFlags, send.go) that reads the same table
//     (sendFlagDefs) the router would have. One table, so a flag cannot be
//     documented in help and unparsed in practice.
//
// -O traces both. On the prompt verbs extractPromptFlags parses it into
// sendOpts.outfit and parks it in promptOutfit, so buildPromptForm puts
// it on the same RPC as the message: no verb assembles its own prompt, so
// none can carry the flag and forget the fold. On `state outfit` it is the
// verb's first positional, and reaches the same ParseSpec and the same fold.
//
// Run bodies: manage.go (list/fork/kill/promote), prompt.go and send.go
// (prompting), outfit.go (state outfit), form.go (state/set/unset),
// portable.go (export/import), firstrun.go (the wizard create falls into).
// Completions are CompleteArgs callbacks in complete_*.go.
//
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
		Usage:   "show [<id>] [-n N | --from A [--to B] | --before T | -a] [--max-bytes N] [-j] [-o] [-v] [-l]",
		Long: `Render an aria's history as turns. A turn is one exchange: your
question and every node the agent produced about it. The optional
positional is the target aria id; default is the pid-bound aria.
Turns are labeled by their turn id: the coordinate send/fork
<id>:<turn> takes.

  figaro show                      last 10 turns of the bound aria
  figaro show eac16fef             last 10 turns of aria eac16fef
  figaro show -n 20                last 20 turns (paginates backwards from the end)
  figaro show eac16fef -n 20       last 20 turns of eac16fef
  figaro show --from 4             turns 4..end
  figaro show --from 1 --to 3      turns 1..3 inclusive
  figaro show --before 12 -n 5     5 turns before turn 12 (paginate backwards)
  figaro show -a                   every turn
  figaro show -j                   turns as raw JSON (the wire IR verbatim)
  figaro show -o                   with each block's address and timestamp
  figaro show eac16fef -v          verbose IR, labeled by LT
  figaro show -l                   raw IR, no rendering
  figaro show --max-bytes 200000   cap what is painted; the OLDEST turns go first

show reads from the TAIL backwards, one page at a time, and stops as soon as
your selection is covered. Length is not a limit: the last N turns of a 40,000
turn aria cost the same read as the last N of a short one, so redirecting show
to a file captures the tail of any aria. Diagnostics go to stderr, so the file
holds the conversation and nothing else.

--max-bytes is the one cap, and it is opt-in. When the selection is larger
than the budget, the OLDEST turns of it are dropped, never the newest, and a
line on stderr says how many.

LT is the model's coordinate: it counts the steps the model experienced,
and most LTs sit mid-tool. It stays visible under -v/-l for debugging the
fig IR, but it is not an address: turns are.`,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (alias for the positional)"},
			{Long: "details", Short: "o", IsBool: true, Description: "Block addresses and timestamps, as Ctrl-O shows in the pager"},
			{Long: "verbose", Short: "v", IsBool: true, Description: "Raw IR with patches, thinking, usage, transitions"},
			{Long: "literal", Short: "l", IsBool: true, Description: "No ANSI / markdown rendering"},
			{Long: "all", Short: "a", IsBool: true, Description: "Show every turn, not just last N"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit turns as raw JSON (the wire IR verbatim)"},
			{Long: "from", Description: "Start turn id (inclusive)"},
			{Long: "to", Description: "End turn id (inclusive)"},
			{Long: "before", Description: "Show N turns before this turn id (paginate backwards)"},
			{Long: "max-bytes", Description: "Cap the painted size; the oldest turns of the selection are dropped first"},
			{Long: "last", Short: "n", Description: "Show the last N turns (paginate backwards from the end)"},
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
			if ctx.BoolFlag("details") {
				args = append(args, "-o")
			}
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
			if v := ctx.Flag("before"); v != "" {
				args = append(args, "--before", v)
			}
			if v := ctx.Flag("last"); v != "" {
				args = append(args, "--last", v)
			}
			// A flag declared in the registry and not reassembled here is
			// SILENTLY IGNORED: cmdkit consumes it, renderAria never sees it,
			// and the command succeeds having done nothing you asked. That is
			// how --max-bytes did nothing on its first run.
			if v := ctx.Flag("max-bytes"); v != "" {
				args = append(args, "--max-bytes", v)
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
		Usage:   "send [--id <id>] [-O <spec>] [-e] [-r] [-v] [-o] [-l] [-x] [-n] [-y] [-f] [-j] -- <prompt>",
		Long: `Send a prompt to an aria. Without --id, targets the pid-bound
aria (creating one if this shell has no binding): or, inside an aria's
own bash tool, the aria itself (FIGARO_ARIA). With --id, targets
the named aria, which must already exist (aria ids are system-minted).

Persistence (--ephemeral) and formatting (--raw) are orthogonal.

Flags:
  --id <id>      Target a specific existing aria
  -e, --ephemeral
                 Spin a one-shot in-memory aria; kill it on completion.
                 Contradicts --id. Says nothing about formatting.
  -O, --outfit <spec>
                 Dress the aria in an outfit. On an aria THIS CALL creates it
                 is the birth outfit; on one that already exists it is folded
                 onto the form in the SAME call as the prompt, so the
                 turn you are sending is answered wearing it. Additive: keys
                 already holding the value are skipped, nothing is removed.
                 A spec is names, k=v pairs and JSON literals, comma-joined
                 and folded left to right (-O sonn5,ttl=1h). Repeating -O
                 appends. Defaults to config.toml's default_outfit.
                 See ` + "`figaro help outfits`" + ` for the syntax.
  -r, --raw      Stream verbatim to stdout: no ANSI, no markdown.
                 Pipe-friendly. Says nothing about persistence.
  -v, --verbatim Dump the raw wire frames as JSON (one {"method","params"}
                 per line): the literal protocol stream, no formatting,
                 no delta application.
  -o, --verbose  Verbose: expand full tool inputs (else truncated). Thinking
                 blocks are always shown (muted). Ctrl-O toggles live.
  -l, --listen   Open the transcript pager at startup. The transcript always
                 stays open until Ctrl-D/Ctrl-C/q; Ctrl-L or Ctrl-T open it
                 mid-stream.
  -x, --exec     Treat the prompt as a bash instruction. The reply is
                 piped to bash -c. --raw is silently ignored here
                 because the script governs its own output.
  -n, --dry-run  --exec only: print the script without running it.
  -y, --yes      --exec only: skip the confirmation prompt.
  -f, --forget   Submit the prompt and exit immediately. Do not attach
                 to the stream; do not send figaro.interrupt on Ctrl-C.
                 Use ` + "`figaro listen <id>`" + ` later to follow.
  -j, --json     Emit a single {"aria_id":..., "mode":...} JSON line on
                 stdout. With --forget: fire, then print. With <id>:<turn>:
                 fork, then print (mode="fork-send").

Timing is the whole rule: one command, whether or not the aria is busy.
A prompt that arrives while a turn is running joins it as a steering aside;
a prompt that arrives when nothing is running opens a turn of its own. The
classification is made where the queue is drained, and nowhere else.

Keys while streaming:
  Ctrl-C         Interrupt the turn (sends figaro.interrupt).
  Ctrl-D         Disconnect this CLI; leave the turn running.
  Ctrl-T         Open the full-screen transcript pager.
  Ctrl-O         Toggle verbose tool-input expansion.

  figaro send -- <prompt>              prompt the pid-bound aria, rich
  figaro send --id myid -- <prompt>    prompt a named aria (rich)
  figaro send -r -- <prompt>           bound aria, raw stream
  figaro send -e -- <prompt>           ephemeral, rich
  figaro send -er -- <prompt>          ephemeral + raw
  figaro send -ex -y -- <instruction>  ephemeral exec, no confirmation
  figaro send -O sonn5 -er -- <p>      ephemeral aria on a named outfit, raw
  figaro send -O focus --id x -- <p>   fold focus onto x, then ask
  figaro send -f --id myid -- <prompt> fire-and-forget; do not stream
  figaro send -- <nudge>               sent mid-turn, this steers that turn

The bare form drops the verb: ` + "`figaro [flags] -- <prompt>`" + ` parses exactly
like ` + "`figaro send [flags] -- <prompt>`" + `: same flags, same semantics. The
` + "`--`" + ` is mandatory there (so a mistyped subcommand stays an error), and a
positional target needs the explicit verb or --id.`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runSend(ld, ctx.RawArgs)
			return nil
		},
		CompleteArgs: completeNewPrompt,
	})

	r.Register(&cmdkit.Command{
		Name:  "new",
		Group: "Prompt",
		Short: "Start a fresh aria and prompt it",
		Usage: "new [-j|--json] [-O <spec>] [-- <prompt>]",
		Long: `Creates a new aria (server-generated id), binds it to this shell, and: when
a prompt follows ` + "`--`" + `: sends it.

  figaro new -- <prompt>            fresh aria on the default outfit, prompted
  figaro new -O <spec> -- <p>       fresh aria on a named outfit, prompted
  figaro new -O <spec>              fresh aria on a named outfit, attended, no turn
  figaro new -S ttl=1h -- <p>       fresh aria with a birth patch (-O names, -S data)
  figaro new                        fresh aria on the default outfit, attended
  figaro new -C <@form>             fresh aria, cast into that role at birth
  figaro new -CO reviewer           fresh aria, and a role minted from an outfit
  figaro new -CS name=x             fresh aria, and a role minted from keys

-C/--cast mints the pair. With a role named, -O/-S dress the ARIA; with no
role named they mint the ROLE, because that is the thing that does not exist
yet. A figaro that is minted but not cast is reported as exactly that, with
its id, rather than exiting silently.

new always mints. To go home instead, ` + "`figaro attend null`" + ` drops this
shell's binding: which is what bare ` + "`new`" + ` used to do, a second spelling
of another verb sitting on the obvious meaning of this one.

The outfit is the aria's BIRTH outfit here: the stump it is spawned under,
which is what ` + "`figaro ls`" + ` shows in the OUTFIT column. It defaults to
config.toml's default_outfit. See ` + "`figaro help outfits`" + ` for the spec syntax.

new shares send's flag parser, so -O, -j and the short bundles behave
identically; the flags that only make sense when sending to something that
already exists (--id, -e, -x) are refused rather than ignored.

-j/--json emits {aria_id, mode:'new'} on stdout.`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			// Same parser as send and fork: -O composes the same way, shorts
			// bundle the same way, and a rejection reads the same.
			// The role positional is lifted out BEFORE the prompt parser
			// sees it: `new` refuses bare arguments (a prompt must follow
			// `--`), and that refusal is right for everything except the one
			// argument this verb now takes. The `@` sigil is what makes the
			// lift lexical rather than a guess.
			role, raw, rerr := liftRoleArg(ctx.RawArgs)
			if rerr != nil {
				return fmt.Errorf("new: %s", rerr)
			}
			opts, rest, perr := extractPromptFlags(raw, false)
			if perr != nil {
				return fmt.Errorf("new: %s", perr)
			}
			if err := validateNewOpts(opts); err != nil {
				return fmt.Errorf("new: %s", err)
			}
			if role != "" && !opts.cast {
				return fmt.Errorf("new: %s is a role; `new -C %s` casts into it", role, role)
			}
			prompt := extractPrompt(rest)
			set := renderSettings{jsonMode: opts.json}
			if opts.cast {
				runNewCast(ld, role, opts.outfit, prompt, set)
				return nil
			}
			if prompt == "" {
				// No prompt: mint under the spec (empty = default_outfit) and
				// attend it, no turn. A prompt needs the `--` boundary.
				runNewFromOutfit(ld, opts.outfit, set)
				return nil
			}
			runNewPrompt(ld, prompt, opts.outfit, set)
			return nil
		},
		CompleteArgs: completeNewPrompt,
	})

	r.Register(&cmdkit.Command{
		Name:  "listen",
		Group: "Prompt",
		Short: "Attach to an aria's live stream without sending a prompt",
		Usage: "listen [<id>]",
		Long: `Attach to an aria's live stream. Same view as a send mid-stream:
catches up to the committed cursor, follows live frames, and supports
Ctrl-T transcript mode: just without calling figaro.qua. Stays open
until you close it.

With no id, the pid-bound aria is used.

Keys:
  Ctrl-C   Interrupt the in-flight turn (like in send).
  Ctrl-D   Disconnect this CLI; the turn keeps running.
  Ctrl-T   Open the full-screen transcript pager.
  Ctrl-O   Toggle verbose tool-input expansion.
  q / Esc  (in pager) leave pager and return to the inline tail.

TESTING: --record <file> writes a wire tape: every JSON-RPC message
this CLI exchanged with the agent, with the time it crossed. Replay it
with ` + "`figaro replay <file>`" + ` to reproduce the exact stream, and
the exact rendering, with no daemon and no provider. A tape carries the
aria's content; it is written only when you ask for it.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "record", Description: "Record the aria wire to a tape file (testing)"},
			{Long: "note", Description: "Note stored in the tape header: what you are hunting"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			var id string
			if len(ctx.Args) > 0 {
				id = ctx.Args[0]
			}
			runListen(ld, id, ctx.Flag("record"), ctx.Flag("note"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "replay",
		Group: "Prompt",
		Short: "Replay a recorded aria wire tape through the real renderer",
		Usage: "replay <tape> [--speed <x>] [--summary]",
		Long: `Play back a tape taken with ` + "`figaro listen --record`" + `.

The tape is the whole world: no angelus, no agent, no provider, no aria
store, no tokens. A local socket speaks the recorded JSON-RPC frames on
their recorded schedule and the ordinary listen renderer: same pager,
same pacer, same catch-up: draws them. What you see is what was seen.

  figaro replay bug.tape              real time
  figaro replay bug.tape --speed 4    four times faster
  figaro replay bug.tape --summary    what is on the tape, without playing it`,
		ArgsMin: 1,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "speed", Description: "Playback rate; 1 is real time (default 1)"},
			{Long: "summary", IsBool: true, Description: "Describe the tape and exit"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			speed := 1.0
			if v := ctx.Flag("speed"); v != "" {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return fmt.Errorf("--speed %q: not a number", v)
				}
				speed = f
			}
			runReplay(ld, ctx.Args[0], speed, ctx.BoolFlag("summary"))
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "hup",
		Group: "Prompt",
		Short: "Hang up: stop the turn, KEEP queued messages (-d discards them)",
		Usage: "hup [<id>] [-d|--drop-queued-messages] [-j|--json]",
		Long: `Hang up on the turn in flight: the same RPC Ctrl-C fires inside a
send stream. Anything queued behind it is KEPT.

The waiting messages coalesce into ONE combined message, which the aria
answers next: three notes typed during a long turn are one question, not
three turns to sit through. A queued form set or fork is a barrier
and is never crossed.

  figaro hup          stop the turn, keep the queue
  figaro hup -d       stop the turn and DROP the queue
  figaro hup -j       either of the above as one JSON object

Both forms RETURN the queued messages: listed on stdout, or as JSON
with -j: so dropping them is not the same as losing them:

  figaro hup -dj > lost.json

` + "`figaro cut`" + ` is the shorthand for ` + "`figaro hup -d`" + `. With no id, the
pid-bound aria is used.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "drop-queued-messages", Short: "d", IsBool: true, Description: "Discard the queued messages (they are still returned)"},
			{Long: "json", Short: "j", IsBool: true, Description: "Print one JSON object (aria, cleared, queue) and exit"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			var id string
			if len(ctx.Args) > 0 {
				id = ctx.Args[0]
			}
			disposition := rpc.QueueKeep
			if ctx.BoolFlag("drop-queued-messages") {
				disposition = rpc.QueueClear
			}
			runHangup(ld, id, disposition, ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "cut",
		Group: "Prompt",
		Short: "Shorthand for `hup -d`: stop the turn, DISCARD queued messages (returned)",
		Usage: "cut [<id>] [-j|--json]",
		Long: `Cut the line: stop the turn in flight AND drop everything queued
behind it.

The discarded messages are handed back rather than lost: verbatim, one
entry per message as you typed it, with the form input each
carried: so they can be persisted:

  figaro cut          stop the turn, discard the queue (listed on stdout)
  figaro cut -j > lost.json
                      the same, as one JSON object you can keep

Unlike ` + "`figaro hup`" + `, nothing survives to be answered. Clearing does
not need a turn to be running, a queue is worth dropping between turns
too. A queued form set or fork is not a question and is left
alone. With no id, the pid-bound aria is used.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "json", Short: "j", IsBool: true, Description: "Print one JSON object (aria, cleared, the drained queue) and exit"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			var id string
			if len(ctx.Args) > 0 {
				id = ctx.Args[0]
			}
			runHangup(ld, id, rpc.QueueClear, ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "queue",
		Group: "Prompt",
		Short: "Read, edit and delete the messages an aria has not answered yet",
		Usage: "queue [ls] | queue rm <id>... | queue rm --all | queue edit <id> -- <text>   [--id <aria>] [-j]",
		Long: `The queue is what the aria has accepted but not yet answered.

  figaro queue                    list it: id, state, age, text
  figaro queue rm 3 5             drop those messages
  figaro queue rm --all           drop all of them
  figaro queue edit 3 -- new text rewrite one

To ADD to the queue, send: a queued message is just a prompt that
arrived while the aria was busy.

Ids come from the listing and are only meaningful in the generation they
were read from: they restart whenever the agent is rebuilt: so every
mutation re-reads the queue first and names that generation. If the
agent restarted in between, the request is refused as stale rather than
resolved against a different message.

A refusal is an ANSWER, not a crash: the agent will decline to delete a
message it has already committed to the conversation, and says which of
"committing", "committed", "merged" (an interrupt folded it into another
queued message: the survivor's id is named), "stale" or "unknown"
applies. Exit is 0 when every id was applied, 1 when any was refused.

The aria is --id <aria>, or the one this shell is attended to. The
positional slot belongs to the sub-verb.`,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Address a specific aria (default: the attended one)"},
			{Long: "all", IsBool: true, Description: "queue rm: drop every queued message"},
			{Long: "json", Short: "j", IsBool: true, Description: "Print one JSON object and exit"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			ariaID := ctx.Flag("id")
			asJSON := ctx.BoolFlag("json")
			all := ctx.BoolFlag("all")

			verb := "ls"
			if len(ctx.Args) > 0 {
				verb = ctx.Args[0]
			}
			switch verb {
			case "ls", "list":
				if len(ctx.Args) > 1 {
					dieUsage("queue ls takes no arguments (address an aria with --id)")
				}
				if all {
					dieUsage("queue ls: --all belongs to `queue rm`")
				}
				runQueueList(ld, ariaID, asJSON)
			case "rm", "delete":
				ids := parseQueueIDs(ctx.Args[1:])
				switch {
				case all && len(ids) > 0:
					dieUsage("queue rm: --all and explicit ids are mutually exclusive")
				case !all && len(ids) == 0:
					dieUsage("queue rm: name the ids to drop, or pass --all")
				}
				runQueueRemove(ld, ariaID, ids, all, asJSON)
			case "edit", "update":
				if len(ctx.Args) < 3 {
					dieUsage("queue edit: usage is `figaro queue edit <id> -- <text>`")
				}
				ids := parseQueueIDs(ctx.Args[1:2])
				text := queueEditText(ctx.Args[2:])
				if strings.TrimSpace(text) == "" {
					dieUsage("queue edit: the replacement text is empty (use `queue rm %d` to drop it)", ids[0])
				}
				runQueueEdit(ld, ariaID, ids[0], text, asJSON)
			default:
				dieUsage("queue: unknown sub-verb %q (want ls, rm or edit)", verb)
			}
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Group:   "Session",
		Short:   "List arias: scoped to where you're attended (attend is `cd`)",
		Usage:   "list [<id>] [-H|--home | -g|--global] [-a|--all | -n <count>] [-j|--json]",
		Long: "Lists arias `ls`-style relative to where you're attended (attend is\nthe `cd`).\n\n" +
			"Scope:\n" +
			"  (default)     attended → your conversation's tree (● = you);\n" +
			"                detached → home (all top-level arias)\n" +
			"  <id>          that aria's subtree\n" +
			"  -H, --home    the home view (all top-level arias) without unbinding\n" +
			"                (-h is reserved for help, on every verb)\n" +
			"  -g, --global  home plus the null + outfit anchors (the full tree)\n\n" +
			"Cap (mutually exclusive):\n" +
			"  (default)     10 rows, trees ordered by their most recent member\n" +
			"  -a, --all     no cap\n" +
			"  -n <count>    cap to <count>\n\n" +
			"  -j, --json    pro/dev: every aria incl. null + outfits as JSON;\n" +
			"                takes no other flags",
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "home", Short: "H", IsBool: true, Description: "Home view: all top-level arias, without unbinding (-h is help)"},
			{Long: "global", Short: "g", IsBool: true, Description: "Full hierarchy incl. the null + outfit anchors"},
			{Long: "all", Short: "a", IsBool: true, Description: "Show all (remove the 10-row cap)"},
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
		Short:   "Bind this shell to an existing aria (optionally at a turn)",
		Usage:   "attend <id> | <id>:<turn> | <id>.<lt> | :<turn> | null",
		Long:    "Binds this shell to an aria. With :<turn> the binding carries a pending\nfork-point: the next bare prompt (`fig -- …`) forks the trunk there and\nmoves to the new branch. `:<turn>` alone re-pins the already-bound aria.\n\n`attend null` goes home: drops this shell's binding (named for the kindNull\ngenesis root). New conversations then default to the live outfit.\n\nTerminal-only. Inside an aria's own bash tool, FIGARO_ARIA statically\nattends that shell to the aria that spawned it, and attend refuses: reach\nanother aria with an explicit --id instead.",
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
		Name:  "outfit",
		Group: "State",
		Short: "Outfit lifecycle: reload flags the default form for recomputation",
		Usage: "outfit reload",
		Long: `The outfit files are ONE-WAY sources of truth: figaro reads them, and
nothing ever writes form state back (deliberate: they may be tracked in
git; access controls will fence the gap later).

  figaro outfit reload    flag the default form dirty; the next fig new
                          re-reads the files, and remints the default form
                          only if the hash moved or the form was patched
                          by hand since birth. Same files, untouched form:
                          a no-op, and the shared prefix (and the
                          provider's warm cache) survives.

Folding an outfit onto a LIVE aria is a different act: see
` + "`figaro state outfit`" + `.`,
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			if ctx.Args[0] != "reload" {
				return fmt.Errorf("outfit: unknown subcommand %q (only `reload`; there is deliberately no `write`)", ctx.Args[0])
			}
			acli := mustConnectAngelus(ld)
			defer acli.Close()
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := acli.OutfitReload(c)
			if err != nil {
				die("outfit reload: %s", err)
			}
			if resp.Flagged {
				fmt.Printf("default form %s flagged; the next `fig new` recomputes from the outfit files\n", resp.FormID)
			} else {
				fmt.Println("no default form minted yet; the next `fig new` computes from the files regardless")
			}
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "study",
		Group: "Session",
		Short: "Subscribe this figaro to an unbound form (see its changes)",
		Usage: "study [<aria>] <@form-id> | study [-j]",
		Long: `Subscribes a figaro to an unbound form: committed patches to the form
reach the figaro as <system-reminder> blocks on its next turn. Durable on
the figaro's own board (system.studies); revival resubscribes; a fork
inherits the relationship, never a private copy. No arguments lists the
attended figaro's studies. The form is always the LAST positional.`,
		ArgsMax: 2,
		Flags: []cmdkit.FlagDef{
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runStudy(ctx.Extra.(*config.Loaded), ctx.Args, false, ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "drop",
		Group:   "Session",
		Short:   "Unsubscribe this figaro from a studied form",
		Usage:   "drop [<aria>] <@form-id>",
		ArgsMin: 1,
		ArgsMax: 2,
		Flags: []cmdkit.FlagDef{
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runStudy(ctx.Extra.(*config.Loaded), ctx.Args, true, ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "cast",
		Group: "Session",
		Short: "Cast a figaro into a role (point target-aria here, and study it)",
		Usage: "cast [<aria>] <@form-id> | cast [<aria>] -O <names> [-S <k=v>] [-j]",
		Long: `One casting call: ensure the figaro studies the role, then point the
role's target-aria at it: serialized through the figaro's actor loop, so
no two castings of one figaro interleave. With -O the role is MINTED by
the call, born already cast (nothing half-fails); -O occupies the form
slot, so a lone positional is then the aria. With no figaro available
(unattended, or attending a form) one is minted from the default form
first: if the casting then fails, that partial is spelled out.

ATTENDING A FORM, with no arguments at all, mints a figaro for it and
casts it: the other entrance to `+"`figaro new -C`"+`, the same operation from
the other end. There, -O/-S dress the ARIA, because the form already
exists; attending an ARIA they mint the ROLE, for the same reason.

  figaro cast                 (attending @form) mint a figaro and cast it
  figaro cast -O sonn5        (attending @form) dress the minted figaro
  figaro cast -O reviewer     (attending an aria) mint the role, cast into it

The form is always the LAST positional. Bound boards are refused: roles
are unbound forms only.`,
		ArgsMax: 2,
		Flags: []cmdkit.FlagDef{
			{Long: "outfit", Short: "O", Description: "Mint the role from these outfit NAMES (occupies the form slot)"},
			{Long: "set", Short: "S", Description: "Mint the role with these keys (occupies the form slot)"},
			{Long: "delete", Short: "D", Description: "Key paths the minted role's birth patch removes"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runCast(ctx.Extra.(*config.Loaded), ctx.Args, ctx.Flag("outfit"), ctx.Flag("set"), ctx.Flag("delete"), ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "bind",
		Group: "Session",
		Short: "Birth a figaro from an unbound form (dormant; never rebinds this shell)",
		Usage: "bind [<@form-id> | null] [-O <names>] [-S <k=v>] [-j]",
		Long: `Births a FIGARO by forking an unbound form: the form's state becomes the
figaro's inherited prefix, the form goes on living, and nothing converts.

  figaro bind                 bind from the ATTENDED form
  figaro bind @a1b2c3         bind from that form
  figaro bind null            the naked figaro: mints fine, and its first
                              TURN fails until a provider is patched in
  figaro bind @a1b2c3 -O focus  fold an outfit into the birth
  figaro bind @a1b2c3 -S n=1    fold keys into the birth

The figaro is born DORMANT: no agent, no provider constructed, and
wakes on first use. Your shell's attendance never moves: attend the
printed id yourself. See ` + "`figaro help form`" + ` for the form family.`,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "outfit", Short: "O", Description: "Outfit NAMES folded into the birth patch"},
			{Long: "set", Short: "S", Description: "Birth keys: k=v or a JSON literal"},
			{Long: "delete", Short: "D", Description: "Key paths the birth patch removes"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			target := ""
			if len(ctx.Args) > 0 {
				target = ctx.Args[0]
			}
			runBind(ld, target, ctx.Flag("outfit"), ctx.Flag("set"), ctx.Flag("delete"), ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "fork",
		Group: "Session",
		Short: "Branch a conversation: keep this id, mint an alternative",
		Usage: "fork [--id <id> | <id>[:<turn>]] [--stay] [-r|-v|-o|-l|-x|-n|-y|-f|-j] [-- <prompt>]",
		Long: `Branch a conversation. A HEAD fork keeps the target's id on the
continuation line and mints ONE new aria, the alternative. The target is
NOT frozen and does NOT become read-only: it stays live at the same id,
so anything already addressing it keeps working. Only an INTERIOR fork
(<id>:12) freezes a node, and it freezes the fork POINT, not the target.

  figaro fork                 branch the bound aria at its head
  figaro fork <id>            branch another aria at its head (maintenance)
  figaro fork <id>:12         interior fork: history before turn 12 is shared,
                              the original suffix becomes the continuation
  figaro fork <id>.842        the same, at an LT instead of a turn
  figaro fork --stay          branch but do not rebind this shell

TWO COORDINATES. :N is a TURN -- one exchange, the number show prints,
and what you normally want. .N is an LT -- one step of the model's
experience, the number show -v prints. Prefer the colon: most LTs sit
mid-tool, where a fork strands a tool_invoke without its result. Reach for
the dot when you already hold an LT, or need a point no turn boundary names.
Both work for send, fork and attend; naming both at once is an error.

Forking your own bound aria at its head leaves this shell bound to the
same id: the continuation keeps the target's id, trunk and mantra, and
the aria is never frozen. Forking any other aria, or passing --stay,
also leaves your session untouched. SELF-FORKING IS SAFE -- it does not
cost you your id, your inbox, or any message in flight.

With a prompt: ` + "`figaro fork [flags] -- <prompt>`" + `: it also sends, the way
` + "`figaro new -- <prompt>`" + ` does. The prompt always lands on the ALTERNATIVE
(the fresh empty branch); the continuation is never written to. Parsing is
` + "`send`" + `'s, so the send flags mean here exactly what they mean there:

  figaro fork -- <prompt>              branch the bound aria, prompt the branch
  figaro fork <id>:12 -- <prompt>      "what if turn 12 had gone this way"
  figaro fork --stay <id> -- <p>       fan out a branch; do not move this shell
  figaro fork -r -- <prompt>           branch, stream raw to stdout
  figaro fork -fj -- <prompt>          branch, fire-and-forget, print the ids
  figaro fork -O review -- <prompt>    branch, dress the branch, then ask
  figaro fork -S ttl=1h                branch and patch it; say nothing yet

  -r/--raw, -v/--verbatim, -o/--verbose, -l/--listen, -x/--exec (+ -n, -y),
  -f/--forget, as in ` + "`figaro send`" + `; prompt-only (an error without one).
  -j/--json: one line on stdout: {aria_id, parent, continuation,
                 alternative, turn, rescoped, mode}. mode is "fork" with no
                 prompt, "fork-send" with one, and aria_id is then the branch.
  --stay: governs the SHELL only, never where the prompt lands. (This
                 differs from ` + "`send <id>:<turn> --stay`" + `, which parks the branch
                 and sends to the original trunk; under fork the branch is
                 always the thing you just made, so it is always the thing
                 that gets prompted.)
  -O/--outfit: dresses the ALTERNATIVE, in the same call that mints it: the
                 fold lands on the new branch's form before anything is
                 said to it, so the first turn is answered wearing it. Legal
                 with or without a prompt. See ` + "`figaro help outfits`" + `.
  -e/--ephemeral is rejected: a fork mints a persistent branch.`,
		PassRaw: true,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (defaults to this shell's); :<turn> for an interior fork"},
			{Long: "stay", IsBool: true, Description: "Do not rebind this shell to the new branch/continuation"},
			{Long: "outfit", Short: "O", Description: "Dress the new branch (see `figaro help outfits`)"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit machine-readable result on stdout (parent, continuation, alternative, ...)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runForkCmd(ld, ctx.RawArgs)
			return nil
		},
		CompleteArgs: completeForkPrompt,
	})

	r.Register(&cmdkit.Command{
		Name:  "normalize",
		Group: "Session",
		Short: "Run deferred topology work now",
		Usage: "normalize [--segments]",
		Long: `Make every aria independent of ancestors it is no longer presented
under: each absorbs the history it currently reads through them.

Everything else in the trunk surface is instant because this can be
postponed. Deletes repair only what they must, at delete time. Run this
when you would rather pay the cost now, in one blocking pass.

  figaro normalize             absorb history for every promoted aria
  figaro normalize --segments  also repack partially filled segments

Nothing changes about what any aria reads; only where the bytes live.
Needs the trunk capability -- a trunkless figaro is normalized already.`,
		ArgsMin: 0,
		ArgsMax: 0,
		Flags: []cmdkit.FlagDef{
			{Long: "segments", IsBool: true, Description: "Also repack partially filled segments"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runNormalize(ld, ctx.BoolFlag("segments"))
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "export",
		Group: "Session",
		Short: "Write an aria to a portable file",
		Usage: "export [<id>] [-o <file>]",
		Long: `Write an aria to a file that another store can import: its outfit,
its form, and every message, with no store-local identity in it.

  figaro export                     the bound aria, to stdout
  figaro export 14bf8211 -o keep.json

It carries CONTENT, not identity. Node ids, fork bases and LTs belong to the
store an aria is in, not to the aria: so they are left behind and the
destination mints its own. That is what makes an import unable to collide.

The provider translation caches do not travel either. They are a derivable
wire cache; the price is one cache-miss on the first turn after an import.`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			runExport(ctx.Extra.(*config.Loaded), ctx.RawArgs)
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "import",
		Group: "Session",
		Short: "Restore an exported aria into this store",
		Usage: "import <file>",
		Long: `Restore an aria written by ` + "`figaro export`" + ` as a NEW conversation.

  figaro import keep.json
  figaro export --id X | ssh other-box figaro import -

The outfit is resolved by content (an identical one is reused, not
duplicated), a fresh conversation is spawned under it, and the messages are
appended through the ordinary path. Every identity is minted by THIS store, so
an import can never collide with what is already here.

The exported id is offered, not demanded: it is taken when free and replaced
when not, and the tool says which happened.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			runImport(ctx.Extra.(*config.Loaded), ctx.Args)
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "promote",
		Group: "Session",
		Short: "Raise an aria in the tree ls draws",
		Usage: "promote [--id <id> | <id>] [levels]",
		Long: `Raise an aria in the tree ` + "`figaro ls`" + ` draws: it takes its parent's
place, and the parent comes to sit under it.

This is presentation only. Nothing moves on disk, no history changes, and
the aria still reads exactly the turns it read before: so a promote is
instant no matter how long the conversation is, and cannot fail halfway.

  figaro promote              promote the bound aria one level
  figaro promote <id>         promote another aria one level
  figaro promote <id> 10      climb up to 10 levels

Promotion stops at the outfit boundary: a top-level conversation has
nothing to promote into ("cannot promote into an outfit").

Needs the trunk capability (` + "`trunks = true`" + `, the default). Without it,
aria nesting follows fork history alone and there is nothing to promote.`,
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
		Aliases: []string{"form"},
		Group:   "State",
		Short:   "Show the form, or shape it",
		Usage:   "state [show] [<id>] | state set <k> <v> | state delete <paths> | state new [-O <names>] [-S <k=v>] | state outfit <names>",
		Long: `The aria's state, and the verbs that shape it.

  figaro state                     print the form
  figaro state show --id <id> -j   another aria's, as JSON
  figaro state set mantra hello    patch ONE path (key then value)
  figaro state set a=1,b="two"     patch several, in the -S grammar
  figaro state delete a.b,mantra   remove key paths, comma-separated
  figaro form new -S name=x        mint an UNBOUND form (@id); -O and/or -S,
                                   never nothing: a form is born of its patch
  figaro form fork @id -S k=v      duplicate a form's state into a fresh @id
  figaro form ls                   list unbound forms (scoped by attendance)
  figaro form rm @id               remove an unbound form
  figaro state outfit focus        fold an outfit onto this aria, now
  figaro state outfit a,b          fold both, b winning
  figaro state outfit --tree a     draw a's layer closure, apply nothing
  figaro form listen               watch it live, as a JSON tree
  figaro form help outfits         a help topic, from here

THE TWO AXES. Outfits and patches are separate and never mixed:

  -O/--outfit   outfit NAMES only, comma-separated
  -S/--set      form keys: ` + "`k=v`" + ` or a JSON literal, comma-separated
  -D/--delete   form key paths to remove, comma-separated

They compose wherever dressing is accepted, and the order is fixed:
outfits fold first, then --set, then --delete. Outfit names are resolved
once, at the daemon's API boundary; what lands on a board is always data.

An unbound form is durable, versioned, forkable state with no figaro
attached: patch it with set/delete, watch it with form listen, make a
figaro from it by forking (bind). A form carrying target-aria is a ROLE.
` + "`state`" + ` and ` + "`form`" + ` are one command; both spellings work everywhere.

` + "`state outfit`" + ` is an ADDITIVE fold: keys already holding the outfit's
value are skipped and nothing is ever removed, so re-applying is free and the
aria sees a <system-reminder> for exactly what changed. It is the same fold
` + "`-O`" + ` performs on send/new/fork; the verb form exists because dressing
state is an action, not a modifier on one.

See ` + "`figaro help outfits`" + ` for the outfit syntax.`,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output (new/fork/ls emit machine-readable ids)"},
			{Long: "outfit", Short: "O", Subwords: []string{"new", "fork"}, Description: "new/fork: outfit NAMES for the birth patch (required for new)"},
			{Long: "set", Short: "S", Subwords: []string{"new", "fork"}, Description: "new/fork: birth keys, k=v or a JSON literal"},
			{Long: "delete", Short: "D", Subwords: []string{"new", "fork"}, Description: "new/fork: key paths the birth patch removes"},
			{Long: "list", IsBool: true, Subwords: []string{"outfit"}, Description: "outfit: list available outfits and exit"},
			{Long: "tree", IsBool: true, Subwords: []string{"outfit"}, Description: "outfit: print the layer closure and exit; applies nothing"},
			{Long: "refresh", IsBool: true, Subwords: []string{"outfit"}, Description: "outfit: re-read outfits and config from disk"},
			{Long: "recursive", IsBool: true, Subwords: []string{"rm"}, Description: "rm: also remove the form's live branches"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			args := ctx.Args
			sub := ""
			if len(args) > 0 {
				sub = args[0]
			}
			switch sub {
			case "outfit":
				return runStateOutfit(ld, ctx, args[1:])
			case "listen":
				id := ctx.Flag("id")
				if id == "" && len(args) > 1 {
					id = args[1]
				}
				runFormListen(ld, id)
				return nil
			case "new":
				runFormNew(ld, ctx.Flag("outfit"), ctx.Flag("set"), ctx.Flag("delete"), args[1:], ctx.BoolFlag("json"))
				return nil
			case "fork":
				parent := ""
				rest := args[1:]
				if len(rest) > 0 {
					parent, rest = rest[0], rest[1:]
				}
				runFormFork(ld, parent, ctx.Flag("outfit"), ctx.Flag("set"), ctx.Flag("delete"), rest, ctx.BoolFlag("json"))
				return nil
			case "ls":
				runFormLs(ld, ctx.BoolFlag("json"))
				return nil
			case "rm":
				runKill(ld, "", args[1:], ctx.BoolFlag("recursive"))
				return nil
			case "set":
				return runFormSet(ld, ctx.Flag("id"), args[1:])
			case "delete", "unset":
				return runFormDelete(ld, ctx.Flag("id"), args[1:])
			case "help":
				return runFormHelp(ctx, args[1:])
			case "show":
				id := ctx.Flag("id")
				if id == "" && len(args) > 1 {
					id = args[1]
				}
				runForm(ld, id)
				return nil
			}
			id := ctx.Flag("id")
			if id == "" && len(args) > 0 {
				id = args[0]
			}
			runForm(ld, id)
			return nil
		},
		CompleteArgs: completeStateArgs,
	})

	r.Register(&cmdkit.Command{
		Name:    "set",
		Group:   "State",
		Short:   "Patch a form key (no LLM round-trip)",
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
		CompleteArgs: completeAriaIDsAfterFlag(completeFormKeys),
	})

	r.Register(&cmdkit.Command{
		Name:    "unset",
		Group:   "State",
		Short:   "Remove form key(s)",
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
		CompleteArgs: completeAriaIDsAfterFlag(completeFormKeys),
	})

	r.Register(&cmdkit.Command{
		Name:   "outfits",
		Group:  "State",
		Hidden: true,
		Short:  "The outfit syntax (a help topic)",
		Usage:  "help outfits",
		Long: `An OUTFIT is a named patch for an aria's form: model, credo,
skills, and anything else you keep together. Dressing has TWO AXES, and they
are never mixed:

  -O/--outfit   outfit NAMES, comma-separated, folded LEFT TO RIGHT
  -S/--set      form keys: ` + "`k=v`" + ` or a whole JSON literal, comma-separated
  -D/--delete   form key paths to remove, comma-separated

  -O sonn5                     one outfit, from outfits/sonn5.toml
  -O sonn5,focus               both, focus winning
  -S ttl=1h                    one key, one value
  -S mantra="cool thing"       quoted values keep their spaces
  -S n=3   -S on=true          a value that parses as JSON keeps its type
  -S '{"ttl":"1h","n":3}'      the literal the sugar stands for
  -D system.tags,mantra        remove two keys
  -O sonn5 -S ttl=1h           compose: the outfit, then your key on top

ORDER IS FIXED where they compose: outfits fold first, then --set, then
--delete. A key you wrote always beats the outfit that also sets it.

What travels is names on one side and DATA on the other. The daemon resolves
the names once, at its API boundary, and what lands on a board is always
materialized keys: so the same ` + "`-O`" + ` means the same thing at birth and on a
live aria, and a patch never carries a directive:

  figaro new -O <o> -- <p>        BIRTH: folded ON TOP of default_outfit
  figaro send -O <o> -- <p>       applied to the form in the same call as
  figaro fork -O <o> [-- <p>]     the prompt (on fork, to the new branch)
  figaro state outfit <o>         applied, with no prompt
  figaro state set k v            the other axis, one path
  figaro state delete <paths>     removals
  figaro state outfit --tree <o>  draw the layer closure, apply nothing
  figaro state outfit --list      what the server has on disk
  figaro state outfit --refresh   re-read outfits and config from disk

Rules worth knowing:

  - Setting a key to the value it already holds changes nothing and announces
    nothing, so re-applying an outfit is free.
  - A name that does not exist is an error everywhere except one place: the
    reserved name ` + "`default`" + `, whose absence is what triggers first-run setup.
  - A name is a file basename: no whitespace, no ` + "`=`" + ` (that is --set), no ` + "`/`" + `
    or ` + "`\\`" + ` (which would climb out of the outfits directory), no brackets or
    quotes, no leading ` + "`-`" + `. Layer names inside a file obey the same rule.
  - Structure must balance. An unmatched brace or quote is an error.
  - QUOTE a literal. Unquoted, ` + "`{mantra:test}`" + ` is not JSON (use the sugar,
    ` + "`mantra=test`" + `) and ` + "`{a:1,b:2}`" + ` is brace-expanded by the shell into two
    words with the braces gone, so it never reaches figaro at all.
  - Repeating a flag appends: ` + "`-O a -O b`" + ` is ` + "`-O a,b`" + `, and the same for -S/-D.
  - ` + "`layers`" + ` is respected in exactly ONE place: an outfit FILE, where it
    names the outfits that file is composed from. Written into a patch it is
    ordinary data and is stored as typed.

Outfits live in the SERVER's config: ~/.config/figaro/outfits/<name>.toml, with
default_outfit in config.toml naming what ` + "`default`" + ` stands for. The first-run
flow writes both through the daemon, so a client never has to know the path.`,
		Run: func(ctx *cmdkit.RunContext) error {
			if cmd, ok := r.Command("outfits"); ok {
				r.PrintCommandHelp(cmd)
			}
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "gc",
		Group: "System",
		Short: "Collect outfit stumps nothing is using",
		Usage: "gc [--dry-run] [-j|--json]",
		Long: "An outfit stump is content-addressed (<outfit>@<hash>), so one exists\n" +
			"per outfit VERSION: editing an outfit mints a new stump the next time an\n" +
			"aria is born under it. Killing an aria collects its stump when it was the\n" +
			"last child, so `gc` is the sweep for versions that predate that.\n\n" +
			"Collecting loses nothing: the stump is content-addressed, so the next aria\n" +
			"wanting that outfit re-mints the same id. Only stumps with no arias under\n" +
			"them are taken.\n\n" +
			"  figaro gc --dry-run   show what would go\n" +
			"  figaro gc             take it",
		Flags: []cmdkit.FlagDef{
			{Long: "dry-run", IsBool: true, Description: "Report what would be collected; remove nothing"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit the report as JSON"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runGC(ld, ctx.BoolFlag("dry-run"), ctx.BoolFlag("json"))
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "status",
		Aliases: []string{"info"},
		Group:   "Session",
		Short:   "Show a focused view of one aria",
		Usage:   "status [<id> | --id <id>] [-m] [-j]",
		Long:    "Prints mantra, provider, model, message count, context-window usage,\nand cumulative token cost for the named aria (or the one bound to this\nshell). Reads the same data the `list` table uses; dormant arias are\nbackfilled from the meta derivation.\n\n  -m/--more   also surface cwd, outfit version, fork origin, created\n  -j/--json   emit the full status as JSON (combine: -mj)",
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
		Name:  "doctor",
		Group: "System",
		Short: "Store maintenance: gc removes dead channels; schema reports channel versions; mem reports the daemon's footprint; skills lists what the binary ships",
		Usage: "doctor <gc [--dry-run] | schema | term | mem [-j] | skills [-j]>",
		Flags: []cmdkit.FlagDef{
			{Long: "dry-run", Short: "n", IsBool: true, Description: "Report what would be removed without touching the store"},
			{Long: "json", Short: "j", IsBool: true, Description: "Machine-readable output (mem)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			if len(ctx.Args) != 1 {
				return fmt.Errorf("usage: doctor <gc [--dry-run] | schema | term | mem [-j] | skills [-j]>")
			}
			switch ctx.Args[0] {
			case "gc":
				return runDoctorGC(ctx.BoolFlag("dry-run"))
			case "schema":
				return runDoctorSchema()
			case "term":
				return runDoctorTerm()
			case "mem":
				return runDoctorMem(ctx.BoolFlag("json"))
			case "skills":
				return runDoctorSkills(ctx.BoolFlag("json"))
			}
			return fmt.Errorf("usage: doctor <gc [--dry-run] | schema | term | mem [-j] | skills [-j]>")
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "vault",
		Aliases: []string{"hush"},
		Group:   "System",
		Short:   "Inspect and repair figaro's embedded secrets vault",
		Usage:   "vault <status | forget | unlock | lock>",
		Long: `figaro runs its own hush: its own identity, agent, and keyring entry
(service "figaro"). The hush binary on PATH addresses a different
instance, so these are the levers for this one.

  status   mode, identity, agent, and whether the saved passphrase works
  forget   clear the saved passphrase; the next command prompts
  unlock   prompt, verify, save, and start the agent
  lock     stop the agent, dropping the decrypted identity`,
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			switch ctx.Args[0] {
			case "status":
				return runVaultStatus()
			case "forget":
				return runVaultForget()
			case "unlock":
				return runVaultUnlock()
			case "lock":
				return runVaultLock()
			}
			return fmt.Errorf("usage: vault <status | forget | unlock | lock>")
		},
		CompleteArgs: func(ctx *cmdkit.CompleteContext) []string {
			return []string{"status", "forget", "unlock", "lock"}
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "update",
		Group: "System",
		Short: "Check for a newer figaro release",
		Usage: "update [--check] [--json] [--apply]",
		Long: `Ask the Go module proxy for the latest tagged figaro release,
compare it against this binary, and tell you how to upgrade: respecting
whichever install channel you used (` + "`go install`" + `, Nix, dev shell).

By default this is *advisory*: figaro never rewrites its own binary
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
		Usage: "completion <bash|zsh|fish|powershell>  |  completion install [<shell>]",
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

	// The bare form is not a registered command: Run dispatches it before
	// the router sees argv: so it needs its own usage line.
	r.Synopsis = []string{progName + " [send flags] -- <prompt>   (same flags as `" + progName + " send`)"}
	// Consumed by extractNoBindFlag before dispatch, so no Command declares
	// them and nothing else would print them.
	r.GlobalFlags = []cmdkit.FlagDef{
		{Long: "no-bind", Short: "A", IsBool: true,
			Description: "Absolute mode: ignore this shell's attend binding (alias --absolute; FIGARO_NO_BIND=1)"},
		{Long: "bind", IsBool: true,
			Description: "Use the binding even where it is off by default (--no-bind, FIGARO_NO_BIND)"},
	}

	return r
}
