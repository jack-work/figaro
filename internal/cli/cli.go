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

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/mark"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/term"
)

// Run dispatches a CLI invocation; progName is argv[0]'s basename.
func Run(progName string, args []string) {
	// die() unwinds rather than calling os.Exit, so a verb can run in-process;
	// this is where an abort becomes an exit.
	defer RecoverExit()

	if progName == "" {
		progName = "figaro"
	}

	mark.Init("cli")
	atExit(profileSession())

	if !lockstepReplayArgs(args) {
		applyAmbiguousWidth()
	}

	// Arm the console before the first escape leaves the process.
	restoreConsole := sync.OnceFunc(term.ArmOutput())
	defer restoreConsole()
	atExit(restoreConsole)

	// Repair the console we read, with no restore: a raw mode left by a killed
	// session is what MakeRaw would otherwise save as the mode to return to.
	// No-op off Windows.
	term.SanitizeInput()

	if len(args) > 0 {
		switch args[0] {
		case "--version", "-V":
			runVersion(false)
			return
		case "box":
			exitAt(runBoxStandalone(progName, args))
			return
		}
	}

	// __complete skips otel init and tolerates config errors: completion must
	// be cheap.
	if len(args) > 0 && args[0] == "__complete" {
		loaded, _ := config.Load(config.DefaultConfigDir())
		if loaded != nil {
			_ = applyIDStyle(loaded)
			boot.DeclareProviders(loaded)
			if s, err := loaded.RefSigil(); err == nil {
				SetRefSigil(s)
			}
		}
		// The binding policy is needed here too: live-key completers resolve
		// the shell's aria through shellPID.
		initBindingPolicy()
		exitAt(buildRouter(progName, loaded).Run(args))
	}

	ctx := context.Background()
	loaded := MustLoadConfig()

	if sigil, err := loaded.RefSigil(); err != nil {
		die("%s", err)
	} else {
		SetRefSigil(sigil)
	}

	if err := applyIDStyle(loaded); err != nil {
		fmt.Fprintf(stderrw, "figaro: config.toml %s\n", err)
	}
	loadKeysFile(loaded)

	initBindingPolicy()
	args = extractNoBindFlag(args)
	adoptHandedAttendance()

	telemetryDir := boot.StateDir()
	if loaded, lerr := config.Load(config.DefaultConfigDir()); lerr == nil {
		figOtel.SetConfiguredLevel(loaded.TelemetryLevel())
		figOtel.SetOTLPEndpoint(loaded.TelemetryOTLPEndpoint())
		telemetryDir = loaded.TelemetryDir(boot.StateDir())
	}
	figOtel.SetServiceVersion(boot.ServiceVersion())
	shutdown, err := figOtel.Init(ctx, telemetryDir)
	if err != nil {
		fmt.Fprintf(stderrw, "warning: otel init: %s\n", err)
	} else {
		defer shutdown(ctx)
	}

	if len(args) == 0 && bareOpensPager() {
		runListen(loaded, "", "", "")
		return
	}

	// The update nudge rides only help surfaces.
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		runUpdateCheck(loaded)
	}

	router := buildRouter(progName, loaded)

	if args, err = dotGetArgs(args); err != nil {
		dieUsage("%s", err)
	}

	// Bare `figaro [send-flags] -- <prompt>` is the prompt verb; args[0] must
	// not name a command.
	if isBareForm(args, router.HasCommand) {
		runBarePrompt(progName, router, loaded, args)
		return
	}

	code := router.Run(args)
	exitAt(code)
}

func buildRouter(progName string, loaded *config.Loaded) *cmdkit.Router {
	r := cmdkit.NewRouter(progName)
	r.Extra = loaded
	r.AlsoCalled = []string{"fig"}

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
			{Long: "details", Short: "o", IsBool: true, Description: "Block addresses and timestamps, as M-m shows in the pager"},
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
			ariaID := ctx.Flag("id")
			if ariaID == "" && len(ctx.Args) > 0 {
				ariaID = ctx.Args[0]
			}
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
			// silently ignored.
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
		Usage:   "send [--id <id>] [-O <spec>] [-S <k=v>] [-D <paths>] [-r] [-v] [-o] [-l] [-x] [-n] [-y] [-f] [-j] [--image <file>]... [--stdin | -- <prompt>]",
		Long: `Send a prompt to an aria. Without --id, targets the pid-bound
aria (creating one if this shell has no binding): or, inside an aria's
own bash tool, the aria itself (FIGARO_ARIA). With --id, targets
the named aria, which must already exist (aria ids are system-minted).

Flags:
  --id <id>      Target a specific existing aria
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
                 blocks are always shown (muted). M-m toggles live.
  -l, --listen   Open the transcript pager at startup. The transcript always
                 stays open until Ctrl-D/Ctrl-C/q; Ctrl-L or Ctrl-T open it
                 mid-stream.
  -x, --exec     Treat the prompt as a bash instruction. The reply is
                 piped to bash -c. --raw is silently ignored here
                 because the script governs its own output.
  -n, --dry-run  --exec only: print the script without running it.
  -y, --yes      --exec only: skip the confirmation prompt.
  --stdin        Read the prompt from stdin, never from argv, so a caller
                 keeps message text out of the process table. A prompt
                 after ` + "`--`" + ` with --stdin is refused.
  --image <file> Attach a png, jpeg, gif or webp to the prompt, ahead of
                 its text; repeat for up to 4. Each file is capped at 10 MB,
                 and the daemon checks and shrinks it before a model sees it.
                 A picture that already fits is sent as is: EXIF and GPS
                 metadata are not removed. Needs a prompt; refused with
                 --exec and --verbatim.
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
  M-m            Toggle verbose tool-input expansion.

  figaro send -- <prompt>              prompt the pid-bound aria, rich
  figaro send --id myid -- <prompt>    prompt a named aria (rich)
  figaro send -r -- <prompt>           bound aria, raw stream
  figaro send -x -y -- <instruction>   exec, no confirmation
  figaro send -O sonn5 -r -- <p>       named outfit, raw
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
		CompleteArgs:  completeSend,
		CompleteFlags: promptFlagDefs,
	})

	r.Register(&cmdkit.Command{
		Name:  "new",
		Group: "Prompt",
		Short: "Start a fresh aria and prompt it",
		Usage: "new [-j|--json] [-O <spec>] [-C [<@form>]] [--image <file>]... [--stdin | -- <prompt>]",
		Long: `Creates a new aria (server-generated id), binds it to this shell, and: when
a prompt follows ` + "`--`" + `: sends it.

  figaro new -- <prompt>            fresh aria on the default outfit, prompted
  figaro new -O <spec> -- <p>       fresh aria on a named outfit, prompted
  figaro new -O <spec>              fresh aria on a named outfit, attended, no turn
  figaro new -S ttl=1h -- <p>       fresh aria with a birth patch (-O names, -S data)
  figaro new                        fresh aria on the default outfit, attended
  :new [-O <spec>] [-S k=v]         in the pager: the same, shown in place
  figaro new -C <@form>             fresh aria, cast into that role at birth
  figaro new --stdin < prompt.txt   the prompt from stdin, never from argv
  figaro new --image r.jpg --stdin  a picture with the prompt from stdin
  figaro new -CO reviewer           fresh aria, and a role minted from an outfit
  figaro new -CS name=x             fresh aria, and a role minted from keys

-C/--cast mints the pair. With a role named, -O/-S dress the ARIA; with no
role named they mint the ROLE, because that is the thing that does not exist
yet. A figaro that is minted but not cast is reported as exactly that, with
its id, rather than exiting silently.

new always mints. To go home instead, ` + "`figaro attend null`" + ` drops this
shell's binding: which is what bare ` + "`new`" + ` used to do, a second spelling
of another verb sitting on the obvious meaning of this one.

The outfit is the aria's BIRTH outfit here: the outfit version it is spawned under,
which is what ` + "`figaro ls`" + ` shows in the OUTFIT column. It defaults to
config.toml's default_outfit. See ` + "`figaro help outfits`" + ` for the spec syntax.

new shares send's flag parser, so -O, -j and the short bundles behave
identically; the flags that only make sense when sending to something that
already exists (--id, -x) are refused rather than ignored.

-j/--json emits {aria_id, mode:'new'} on stdout.`,
		PassRaw: true,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			// The role positional is lifted before the prompt parser
			// sees it: `new` refuses bare arguments.
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
			prompt, perr := promptFromStdin(opts, rest, os.Stdin)
			if perr != nil {
				return fmt.Errorf("new: %s", perr)
			}
			if len(opts.images) > 0 && prompt == "" {
				return fmt.Errorf("new: %s", errImageNeedsPrompt)
			}
			if opts.outfit.images, perr = readPromptImages(opts.images); perr != nil {
				return fmt.Errorf("new: %s", perr)
			}
			set := pagerSettings(ld)
			set.jsonMode = opts.json
			if opts.cast {
				runNewCast(ld, role, opts.outfit, prompt, set, opts.stay)
				return nil
			}
			if prompt == "" {
				runNewFromOutfit(ld, opts.outfit, set)
				return nil
			}
			runNewPrompt(ld, prompt, opts.outfit, set)
			return nil
		},
		CompleteArgs:  completeNewPrompt,
		CompleteFlags: promptFlagDefs,
	})

	r.Register(&cmdkit.Command{
		Name:  "listen",
		Group: "Prompt",
		Short: "Open an aria in the pager without sending a prompt",
		Usage: "listen [<id>]",
		Long: `Attach to an aria's live stream without calling figaro.qua. Listen
opens straight into the full-screen transcript pager, caught up to the
committed cursor and following live frames. It has no inline view: the
pager is the whole session, and it stays open until you end it.

With no id, the aria this shell attends is used. With none attended,
listen mints one from the default outfit, attends it, and opens that.
Bare ` + "`figaro`" + `, with no arguments at a terminal, is the same as
` + "`figaro listen`" + `; off a terminal it prints the usage.

Keys:
  q        With a pit open, close it; with none open, end listen. The
           turn keeps running.
  Ctrl-D   End listen; the turn keeps running.
  Ctrl-C   End listen, interrupting the turn in flight.
  Esc      Cancel what is open (a selection, a pit, a search, visual
           mode). It does not end listen.
  Ctrl-T   Nothing: the pager is already open.
  M-m      Toggle verbose tool-input expansion.
  h        The pager's key help.

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
		Usage: "replay <tape> [--speed <x>] [--summary] [--lockstep <sock> [--fps <n>] [--anim-fps <n>]]",
		Long: `Play back a tape taken with ` + "`figaro listen --record`" + `.

The tape is the whole world: no angelus, no agent, no provider, no aria
store, no tokens. A local socket speaks the recorded JSON-RPC frames on
their recorded schedule and the ordinary listen renderer: same pager,
same pacer, same catch-up: draws them. What you see is what was seen.

  figaro replay bug.tape              real time
  figaro replay bug.tape --speed 4    four times faster
  figaro replay bug.tape --summary    what is on the tape, without playing it

With --lockstep a driver steps the replay on a virtual clock instead.
Replay listens on <sock> and waits up to 10s for one driver. Each line
"step" (the next step) or "step N" (through step N, counting from 0)
renders steps at tape time n/fps, nothing read from the wall clock:
every tape frame and timer due by then, settled, drawn once. Each step
writes OSC 7717;step=<n> BEL after its bytes and answers
"done <n> <bytes>", bytes being the running total written, markers
included. "rate <n>" sets the animation rate (--anim-fps, default 12).
The renderer runs on its own pty sized from the tape header; keys and
SIGWINCH do not reach it. "resize <cols> <rows>" resizes that pty
between steps, with no tape time passing, and redraws as a terminal
resize would: OSC 7717;resize=<cols>x<rows> BEL follows the redraw and
the answer is "resized <cols> <rows> <bytes>". At least 20x5, at most
four times the tape's size. Closing the socket ends the replay.`,
		ArgsMin: 1,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "speed", Description: "Playback rate; 1 is real time (default 1)"},
			{Long: "summary", IsBool: true, Description: "Describe the tape and exit"},
			{Long: "lockstep", Description: "Step on a virtual clock, driven over this unix socket"},
			{Long: "fps", Description: "Lockstep steps per tape second (default 60)"},
			{Long: "anim-fps", Description: "Lockstep animation rate, spinner included (default 12)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			opt := replayOpts{speed: 1, summary: ctx.BoolFlag("summary"), lockstep: ctx.Flag("lockstep"),
				fps: lockstepFPS, animFPS: lockstepAnimFPS}
			if v := ctx.Flag("speed"); v != "" {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return fmt.Errorf("--speed %q: not a number", v)
				}
				opt.speed = f
			}
			for _, n := range []struct {
				flag string
				into *int
			}{{"fps", &opt.fps}, {"anim-fps", &opt.animFPS}} {
				v := ctx.Flag(n.flag)
				if v == "" {
					continue
				}
				if opt.lockstep == "" {
					return fmt.Errorf("--%s steps a --lockstep replay", n.flag)
				}
				i, err := strconv.Atoi(v)
				if err != nil || i <= 0 {
					return fmt.Errorf("--%s %q: want a positive whole number", n.flag, v)
				}
				*n.into = i
			}
			runReplay(ld, ctx.Args[0], opt)
			return nil
		},
		CompleteArgs: completeFileArg,
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
  figaro queue --watch            follow it live, as a form

To ADD to the queue, send: a queued message is just a prompt that
arrived while the aria was busy.

The queue is also a INTRINSIC FORM -- a non-persistent builtin form bound to
the aria, addressed as <aria>/queue -- so it can be watched with the
ordinary form verbs and pushes its changes rather than being polled:

  figaro form show <aria>/queue     the same thing, as a live tree
  figaro form show <aria>/runtime   what the aria is DOING right now

<aria> alone means <aria>/state, the aria's own bound form.

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
			{Long: "watch", Short: "w", IsBool: true, Description: "Follow the queue live (the <aria>/queue intrinsic)"},
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
			if ctx.BoolFlag("watch") {
				if verb != "ls" && verb != "list" {
					dieUsage("queue --watch follows the queue; it does not %s", verb)
				}
				// The queue is a form, so watching it is `form show`.
				runListenIntrinsic(ld, ariaID, "", "", true, intrinsicQueue, resolveFigaroTargetEndpoint)
				return nil
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
		CompleteArgs: completeWords([][2]string{{"ls", "list what is waiting"}, {"rm", "drop messages by id"}, {"edit", "rewrite one message"}}, nil),
	})

	listCmd := &cmdkit.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Group:   "Session",
		Short:   "List arias: scoped to where you're attended (attend is `cd`)",
		Usage:   "list [<id>|^N|/] [-H|--home | -g|--global] [-a|--all | -n <count>] [-j|--json]",
		Long: "Lists arias `ls`-style relative to where you're attended (attend is\nthe `cd`).\n\n" +
			"Scope:\n" +
			"  (default)     attended → you and everything forked below you (● = you);\n" +
			"                detached → home (all top-level arias)\n" +
			"  <id>          that aria's subtree\n" +
			"  ^, ^2, ^^     climb N layers: the ancestor's subtree, you still in it;\n" +
			"                past the root it clamps to the top-level aria and says so\n" +
			"  /             the home view, as a positional\n" +
			"  -H, --home    the home view (all top-level arias) without unbinding\n" +
			"                (-h is reserved for help, on every verb); `lsh` is this\n" +
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
			runList(ctx.Extra.(*config.Loaded), listOptsFrom(ctx, false))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	}
	r.Register(listCmd)

	lshCmd := *listCmd
	lshCmd.Name = "lsh"
	lshCmd.Aliases = nil
	lshCmd.Short = "List arias from home: `ls -H`, spelled shorter"
	lshCmd.Usage = "lsh [<id>] [-g|--global] [-a|--all | -n <count>] [-j|--json]"
	lshCmd.Long = "`ls -H`: the home view, every top-level aria, without unbinding this\nshell. Every other `ls` flag applies; see `figaro help list`."
	lshCmd.Run = func(ctx *cmdkit.RunContext) error {
		runList(ctx.Extra.(*config.Loaded), listOptsFrom(ctx, true))
		return nil
	}
	r.Register(&lshCmd)

	r.Register(&cmdkit.Command{
		Name:  "recent",
		Group: "Session",
		Short: "Every figaro, most recently active first, one row each",
		Usage: "recent [-n <count>] [-j|--json]",
		Long: "Every figaro you may read on this node, newest activity first, with no\n" +
			"tree: the list M-l and :recent show in the pager. A row leads with the\n" +
			"first word of the figaro's handle (its whole handle when another row\n" +
			"shares the word), then its mantra, else its handle, then how long ago\n" +
			"it was last active.\n\n" +
			"  -n <count>    the newest <count> rows\n" +
			"  -j, --json    the rows as JSON: name, id, handle, title, state, cwd,\n" +
			"                last_active (unix ms)",
		ArgsMax: 0,
		Flags: []cmdkit.FlagDef{
			{Long: "limit", Short: "n", Description: "The newest N rows"},
			{Long: "json", Short: "j", IsBool: true, Description: "The rows as JSON"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			limit := 0
			if v := ctx.Flag("limit"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					dieUsage("recent: -n takes a positive count, got %q", v)
				}
				limit = n
			}
			runRecent(ctx.Extra.(*config.Loaded), limit, ctx.BoolFlag("json"))
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "attend",
		Aliases: []string{"at"},
		Group:   "Session",
		Short:   "Bind this shell to an existing aria (optionally at a turn)",
		Usage:   "attend <id> | <id>:<turn> | <id>.<lt> | :<turn> | :head | null",
		Long:    "Binds this shell to an aria. With :<turn> the binding carries a pending\nfork-point: the next bare prompt (`fig -- …`) forks the trunk there and\nmoves to the new branch. `:<turn>` alone re-pins the already-bound aria, and `:head`\nalone drops its pending fork-point.\n\n`attend <node>/<id>` binds an aria on another troupe node: it is resolved\nthrough the door of the troupes/*.toml profile that answers for that node,\nand plain figaro, listen and `fig -- …` then reach it there. A fork-point\non another node is refused.\n\n`attend null` goes home: drops this shell's binding (named for the kindNull\ngenesis root). New conversations then default to the live outfit.\n\nTerminal-only. Inside an aria's own bash tool, FIGARO_ARIA statically\nattends that shell to the aria that spawned it, and attend refuses: reach\nanother aria with an explicit --id instead.",
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runAttend(ld, ctx.Args[0])
			return nil
		},
		CompleteArgs: completeAttend,
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
				fmt.Fprintf(stdout, "default form %s flagged; the next `fig new` recomputes from the outfit files\n", ariaName(resp.FormID))
			} else {
				fmt.Fprintln(stdout, "no default form minted yet; the next `fig new` computes from the files regardless")
			}
			return nil
		},
		CompleteArgs: completeWords([][2]string{{"reload", "flag the default form for recomputation"}}, nil),
	})

	r.Register(&cmdkit.Command{
		Name:  "study",
		Group: "Session",
		Short: "Subscribe this figaro to an unbound form (see its changes)",
		Usage: "study [<aria>] <@form-id> | study [-j]",
		Long: `Subscribes a figaro to an unbound form: committed patches to the form
reach the figaro as <system-reminder> blocks on its next turn. Durable on
the figaro's own bound form (system.studies); revival resubscribes; a fork
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
		CompleteArgs: completeFormVerb,
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
		CompleteArgs: completeFormVerb,
	})

	r.Register(&cmdkit.Command{
		Name:  "cast",
		Group: "Session",
		Short: "Cast a figaro into a role (point target-aria here, and study it)",
		Usage: "cast [<aria>] <@form-id> | cast [<aria>] -O <names> [-S <k=v>] [-j] | cast (attending a form)",
		Long: `One casting call: ensure the figaro studies the role, then point the
role's target-aria at it: serialized through the figaro's actor loop, so
no two castings of one figaro interleave. With -O the role is MINTED by
the call, born already cast (nothing half-fails); -O occupies the form
slot, so a lone positional is then the aria. With no figaro available
(unattended, or attending a form) one is minted from the default form
first: if the casting then fails, that partial is spelled out.

ATTENDING A FORM, with no arguments at all, mints a figaro for it and
casts it: the other entrance to ` + "`figaro new -C`" + `, the same operation from
the other end. There, -O/-S dress the ARIA, because the form already
exists; attending an ARIA they mint the ROLE, for the same reason.

  figaro cast                 (attending @form) mint a figaro and cast it
  figaro cast -O sonn5        (attending @form) dress the minted figaro
  figaro cast -O reviewer     (attending an aria) mint the role, cast into it

The form is always the LAST positional. Bound forms are refused: roles
are unbound forms only.`,
		ArgsMax: 2,
		Flags: []cmdkit.FlagDef{
			{Long: "outfit", Short: "O", Description: "Mint the role from these outfit NAMES (occupies the form slot)"},
			{Long: "set", Short: "S", Repeats: true, Description: "Mint the role with these keys (occupies the form slot); repeats"},
			{Long: "delete", Short: "D", Repeats: true, Description: "Key paths the minted role's birth patch removes; repeats"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
			{Long: "stay", IsBool: true, Description: "Do not attend the role this call mints"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runCast(ctx.Extra.(*config.Loaded), ctx.Args, ctx.Flag("outfit"), ctx.Flag("set"), ctx.Flag("delete"), ctx.BoolFlag("json"), ctx.BoolFlag("stay"))
			return nil
		},
		CompleteArgs: completeFormVerb,
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
			{Long: "set", Short: "S", Repeats: true, Description: "Birth keys: k=v or a JSON literal; repeats"},
			{Long: "delete", Short: "D", Repeats: true, Description: "Key paths the birth patch removes; repeats"},
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
		CompleteArgs: completeFormVerb,
	})

	r.Register(&cmdkit.Command{
		Name:  "fork",
		Group: "Session",
		Short: "Branch a conversation: keep this id, mint an alternative",
		Usage: "fork [--id <id> | <id>[:<turn>[.<node>]]] [--stay] [-r|-v|-o|-l|-x|-n|-y|-f|-j] [-- <prompt>]",
		Long: `Branch a conversation. A HEAD fork keeps the target's id on the
continuation line and mints ONE new aria, the alternative. The target is
NOT frozen and does NOT become read-only: it stays live at the same id,
so anything already addressing it keeps working. Only an INTERIOR fork
(<id>:12) freezes a node, and it freezes the fork POINT, not the target.

  figaro fork                 branch the bound aria at its head
  figaro fork <id>            branch another aria at its head (maintenance)
  figaro fork <id>:head       the same, spelled out
  figaro fork <id>:12         interior fork: history before turn 12 is shared,
                              the original suffix becomes the continuation
  figaro fork <id>:12.10      inside turn 12: node 10 and everything after it
                              is replaced (the number the pager draws under M-m)
  figaro fork <id>:12.-1      turn 12's question: the same as <id>:12
  figaro fork <id>.842        the same, at an LT instead of a turn
  figaro fork --stay          branch but do not rebind this shell

THREE COORDINATES. :N is a TURN -- one exchange, the number show prints,
and what you normally want. :N.K is a NODE inside that turn -- the address
the pager shows under M-m, so what you can point at you can branch at. .N is
an LT -- one step of the model's experience, the number show -v prints.
Prefer the colon: most LTs sit mid-tool, where a fork strands a tool_invoke
without its result. Reach for the dot when you already hold an LT, or need a
point no turn boundary names. All three work for send, fork and attend;
naming more than one at once is an error. :head is the head spelled out;
naming no coordinate means :head.

A NODE IS FINER THAN A FORK POINT, and the difference is reported rather
than hidden. An assistant message that says a paragraph and then calls a
tool is ONE message and TWO nodes; a fork cuts whole messages, so forking
at that tool retreats to before the paragraph and says so on stderr. A node
that begins its own message is exact and says nothing.

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
`,
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
		CompleteArgs:  completeForkPrompt,
		CompleteFlags: promptFlagDefs,
	})

	r.Register(&cmdkit.Command{
		Name:    "normalize",
		Hidden:  true,
		ArgsMin: 0,
		ArgsMax: 0,
		Flags: []cmdkit.FlagDef{
			{Long: "dry-run", Short: "n", IsBool: true, Description: "Report what would change; touch nothing"},
			{Long: "json", Short: "j", IsBool: true, Description: "Emit the report as JSON"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			fmt.Fprintln(stderrw, "figaro normalize moved to `figaro doctor normalize`")
			runNormalize(ctx.Extra.(*config.Loaded), ctx.BoolFlag("dry-run"), ctx.BoolFlag("json"))
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
		CompleteArgs: completeExport,
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
		CompleteArgs: completeFileArg,
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
		Name:  "demote",
		Group: "Session",
		Short: "Sink an aria to a leaf so deleting it takes nothing else",
		Usage: "demote [--id <id> | <id>] [--under <child>] [-j]",
		Long: `Sink an aria to the bottom of the tree ` + "`figaro ls`" + ` draws. One of its
children takes its place, its other children move under that child at the
depth they had, and the aria hangs beneath it with nothing under it. A kill
or a reap of the demoted aria then takes it alone.

  figaro demote <id>                  the child branched earliest takes its place
  figaro demote <id> --under <child>  that child does, if it is one

Presentation only, like promote: no history moves. An aria with no
children is left where it is. ` + "`figaro succeed`" + ` demotes the aria it replaces.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Aria to demote (defaults to this shell's)"},
			{Long: "under", Description: "The child that takes its place"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runDemote(ld, ctx.Flag("id"), ctx.Args, ctx.Flag("under"), ctx.BoolFlag("json"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:    "kill",
		Group:   "Session",
		Short:   "Stop a trunk and reap it; purged after a grace",
		Usage:   "kill [--id <trunk> | <trunk>] [-r] [--when <duration> | --now]",
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target trunk id"},
			{Long: "recursive", Short: "r", IsBool: true, Description: "Also take the trunk's live branches"},
			{Long: "when", Description: "Grace before the purge (90m, 6h, 2d, 1w; 0 deletes now); default [memory] kill_grace_hours, 3 days"},
			{Long: "now", IsBool: true, Description: "Delete at once instead of reaping"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runKill(ld, ctx.Flag("id"), ctx.Args, ctx.BoolFlag("recursive"), ctx.BoolFlag("now"), ctx.Flag("when"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "succeed",
		Group: "Session",
		Short: "Hand a role to a successor and wind the old aria down",
		Usage: "succeed <@role> [--turn <n>|head | --successor <aria>] [--title <t>] [-O <outfits>] [-S <k=v>] [-D <keys>] [--ttl <d>] [--abdication retire|linger] [--stay] [-j] -- <prompt>",
		Long: `Replace the aria a role points at. The role must already target an aria
that exists; that aria is the predecessor.

  figaro succeed @warden -- take over from here
  figaro succeed @warden --turn head -- carry on from the last turn
  figaro succeed @warden --turn 5 --title "deploy warden II" -- go
  figaro succeed @warden --successor <aria> -- you hold the seat now
  figaro succeed @warden --ttl 0 -- go          kill the predecessor at once
  figaro succeed @warden -O default -- go       the successor dressed afresh

In order, stopping at the first step that fails:

  1. fork the predecessor, its mantra set to --title. --turn says what the
     successor keeps:
       (none)    turn 1 only; the prompt becomes its turn 2
       --turn N  turns 1..N-1; the prompt becomes its turn N
       --turn head
                 every turn; the prompt follows the last. On an aria
                 with N turns, --turn N+1 is the same
     The fork carries the model its turn was recorded with; -O/-S/-D dress
     it in the same call, as with fork, so it can come up on today's.
     --successor names an existing aria instead and skips the fork.
  2. demote the predecessor beneath the successor (figaro demote), so its
     branches survive its end. A successor not drawn under it is left where
     it is, and the branch forked earliest takes its place.
  3. send the prompt to the successor. A successor mid-turn is steered.
  4. cast the successor into the role.
  5. the predecessor drops the role, its mantra gains "SUCCEEDED: ", and
     --abdication decides the rest:
       retire  (default) it refuses prompts from now on
       linger  it still takes prompts, for a handover with overlap
     Either way it is reaped --ttl from now (default 10m) unless something
     holds it: a bound shell, another role, a recent branch. --ttl 0 kills
     it instead. A reaped predecessor stays in ` + "`figaro reaped`" + `.

Nothing is rolled back. A step that fails leaves the ones before it in
place, and the error lists the commands that finish the job by hand.

In the pager, :succeed takes the same flags. When the aria on screen is the
predecessor, the pager moves to the successor afterwards; --stay keeps it.`,
		ArgsMin: 0,
		Flags: []cmdkit.FlagDef{
			{Long: "turn", Description: "Fork the predecessor at this turn, or head to keep every turn (default 2)"},
			{Long: "successor", Description: "An existing aria to take the role, instead of a fork"},
			{Long: "title", Description: "The successor's mantra"},
			{Long: "ttl", Description: "Reap the predecessor this long from now; 0 kills it (default 10m)"},
			{Long: "abdication", Description: "retire (refuse prompts) or linger (keep taking them)"},
			{Long: "stay", IsBool: true, Description: "In the pager, keep showing the predecessor"},
			{Long: "outfit", Short: "O", Description: "Outfit NAMES folded into the successor's birth patch"},
			{Long: "set", Short: "S", Repeats: true, Description: "Successor birth keys: k=v or a JSON literal; repeats"},
			{Long: "delete", Short: "D", Repeats: true, Description: "Key paths the successor's birth patch removes; repeats"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON report"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			plan, err := succeedPlanFrom(ctx)
			if err != nil {
				return err
			}
			return runSucceed(plan, ctx.BoolFlag("json"))
		},
		CompleteArgs: completeSucceed,
	})

	r.Register(&cmdkit.Command{
		Name:  "retire",
		Group: "Session",
		Short: "Stop an aria taking prompts, and dress it on the way out",
		Usage: "retire [--id <aria> | <aria>] [--ttl <d>] [-O <outfits>] [-S <keys>] [-D <paths>] [--undo] [-j]",
		Long: `A retired aria refuses every prompt; reading it (show, attend, ls,
fork) still works, and a fork of it is not retired. The outfits and keys
land in the same write as the retirement, so a tombstone can carry its own
end:

  figaro retire <id> --ttl 1h        reaped an hour from now, unless held
  figaro retire <id> -S system.ttl=30d
                                     system.ttl as written: counted from
                                     the aria's creation, not from now

A retired aria is not kept alive by the default keepalive, only by a
system.keepalive of its own. Its sweep still waits for a live agent, a
bound shell, a role pointing into it, or a recently used branch under it.
--undo lets it take prompts again.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Aria to retire"},
			{Long: "outfit", Short: "O", Description: "Outfit NAMES folded in with the retirement"},
			{Long: "set", Short: "S", Repeats: true, Description: "Keys set with the retirement: k=v or a JSON literal; repeats"},
			{Long: "delete", Short: "D", Repeats: true, Description: "Key paths removed with the retirement; repeats"},
			{Long: "ttl", Description: "Reap the aria this long from now (sets system.ttl from its age)"},
			{Long: "undo", IsBool: true, Description: "Let the aria take prompts again"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			return runRetire(ld, ctx.Flag("id"), ctx.Args, ctx.Flag("outfit"), ctx.Flag("set"),
				ctx.Flag("delete"), ctx.Flag("ttl"), ctx.BoolFlag("undo"), ctx.BoolFlag("json"))
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "reaped",
		Group: "Session",
		Short: "Arias kill, gc and the ttl sweep reaped: list, restore or purge them",
		Usage: "reaped [ls | restore <id> [--keep] | purge <id>] [-j]",
		Long: `When an aria's lifetime is spent and nothing holds it, the sweep does not
delete it at once: it reaps it. A reaped aria and everything under it are
hidden from ls, refuse prompts, and wait [memory] reaped_grace_hours (seven
days by default) before the sweep purges them for good. The index is the
catalog @reaped (figaro form show @reaped).

  figaro reaped                 what is reaped, and when each purge is due
  figaro reaped restore <id>    bring the group back; the keepalive holds
                                it for a day, --keep also unsets system.ttl
  figaro reaped purge <id>      delete the group now`,
		ArgsMax: 2,
		Flags: []cmdkit.FlagDef{
			{Long: "keep", IsBool: true, Description: "restore: also unset system.ttl on the root"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			return runReaped(ld, ctx.Args, ctx.BoolFlag("keep"), ctx.BoolFlag("json"))
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "move",
		Group: "Session",
		Short: "Move an aria and its whole fork tree to another node",
		Usage: "move [--id <aria> | <aria>] --to <node> [-j]",
		Long: `Moves the aria's fork tree (every aria sharing its history) to a peer
node under the same ids. The old node keeps a forwarding address, so the
ids keep working from anywhere. Refused while any aria of the tree has a
turn in flight or queued. Reaching another node needs a bearer.

Status: in development; cross-node moves are not settled yet.`,
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Aria to move (defaults to this shell's)"},
			{Long: "to", Description: "Node id, or a unique prefix of one"},
			{Long: "json", Short: "j", IsBool: true, Description: "Print the result as one JSON object"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			runMove(ld, ctx.Flag("id"), ctx.Args, ctx.Flag("to"), ctx.BoolFlag("json"))
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
once, at the daemon's API boundary; what lands on a form is always data.

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
			{Long: "set", Short: "S", Repeats: true, Subwords: []string{"new", "fork"}, Description: "new/fork: birth keys, k=v or a JSON literal; repeats"},
			{Long: "delete", Short: "D", Repeats: true, Subwords: []string{"new", "fork"}, Description: "new/fork: key paths the birth patch removes; repeats"},
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
				runKill(ld, "", args[1:], ctx.BoolFlag("recursive"), true, "")
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
		CompleteArgs: completeSetArgs,
	})

	r.Register(&cmdkit.Command{
		Name:  "get",
		Group: "State",
		Short: "Print form values by path",
		Usage: "get [--id <id>] [-j] <path> [<path>...]",
		Long: `Print form values, by the same paths set and unset take.

  figaro get system.cwd             a string prints bare
  figaro get system.model mantra    one line per path, in order
  figaro get comments.p24 -j        JSON, a string quoted
  figaro .system.cwd                the same as get system.cwd
  figaro show .mantra               likewise

A string prints without quotes; a number, bool or null prints as JSON; an
object or array prints as indented JSON. -j prints JSON for every value, and
with several paths one object keyed by path. A path that is not on the form
is an error naming it (exit 1); the paths that are print first.

A leading '.' stands for get, before any verb or as show's argument. '.N'
(digits only) is refused: it reads as an LT coordinate.`,
		ArgsMin: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target figaro or form id (overrides pid binding)"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON for every value; several paths make one object"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			return runGet(ld, ctx.Flag("id"), ctx.Args, ctx.BoolFlag("json"))
		},
		CompleteArgs: completeAriaIDsAfterFlag(completeFormKeys),
	})

	r.Register(&cmdkit.Command{
		Name:  "pwd",
		Group: "State",
		Short: "Print the directory a figaro's tools run in",
		Usage: "pwd [--id <id>] [-j]",
		Long: `Print system.cwd: the directory a figaro's tools resolve relative paths
against, and where the pager resolves a relative path it opens in your editor.
When system.cwd is unset, the tools run in the daemon's own working directory,
and pwd prints that. Change it with figaro cd.`,
		ArgsMax: 0,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target figaro id (overrides pid binding)"},
			{Long: "json", Short: "j", IsBool: true, Description: "The directory as a JSON string"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			return runPwd(ld, ctx.Flag("id"), ctx.BoolFlag("json"))
		},
		CompleteArgs: completeAriaIDsAfterFlag(nil),
	})

	r.Register(&cmdkit.Command{
		Name:    "toggle",
		Group:   "State",
		Short:   "Flip a true/false form key; absent reads the default outfit's, else false",
		Usage:   "toggle [--id <id>] <key>",
		ArgsMin: 1,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			return runToggle(ld, ctx.Flag("id"), ctx.Args[0])
		},
		CompleteArgs: completeSetArgs,
	})

	r.Register(&cmdkit.Command{
		Name:    "cd",
		Group:   "State",
		Short:   "Change an aria's working directory",
		Usage:   "cd [--id <id>] [<path>]",
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id (overrides pid binding)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			path := ""
			if len(ctx.Args) > 0 {
				path = ctx.Args[0]
			}
			return runCd(ld, ctx.Flag("id"), path)
		},
		CompleteArgs: completeCdDirs,
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
the names once, at its API boundary, and what lands on a form is always
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

Outfits live in the SERVER's config: <name>.toml anywhere under
~/.config/figaro/outfits/ (subdirectories organize; the name is the file's), with
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
		Short: "Choose figaros nobody uses and reap them, after a plan you confirm",
		Usage: "gc [--idle <d>] [--empty] [--older <d>] [--outfit <name>] [--under <id>] [--match <query>] [--box <name>] [--yes | --dry-run] [-j] | gc --undo <run>",
		Long: "gc plans first. It prints what it would reap, what it keeps and why,\n" +
			"and the bytes freed at purge; on a terminal it then asks. Piped, it\n" +
			"prints the plan and changes nothing unless --yes is given.\n\n" +
			"The flags choose figaros and AND together:\n" +
			"  --idle 30d        unused for at least that long\n" +
			"  --empty           never had a message\n" +
			"  --older 1d        born at least that long ago\n" +
			"  --outfit <name>   born under that outfit, any version\n" +
			"  --under <id>      presented under that aria or form\n" +
			"  --match <query>   fzf syntax over mantra, handle and cwd\n" +
			"  --box <name>      in that box; without it the owner's plan skips boxes\n\n" +
			"Nothing is chosen while it has a live agent, a bound shell or attending\n" +
			"pane, a role pointing at it, a spooled prompt, a background session, a\n" +
			"use within its keepalive, or system.keep = true; nor while anything its\n" +
			"kill would take does. The plan lists those as kept.\n\n" +
			"Applying reaps, never deletes: each figaro and its branches go to\n" +
			"`figaro reaped` for [memory] kill_grace_hours (3 days), stamped with\n" +
			"the run. `figaro gc --undo <run>` restores the whole run. A plan lasts\n" +
			"10 minutes, and apply checks every figaro again: one used or held\n" +
			"since the plan is dropped from the run and named.\n\n" +
			"With no flag, gc collects outfit versions no aria was born under.\n" +
			"Old form history is the daemon's hourly job under [store] gc\n" +
			"(\"off\" by default, \"dry-run\" or \"on\"); gc does not touch it.\n\n" +
			"  figaro gc --idle 30d             plan, then ask\n" +
			"  figaro gc --empty --older 1d -y  apply without asking\n" +
			"  figaro gc --undo <run>           restore what one run reaped",
		Flags: []cmdkit.FlagDef{
			{Long: "idle", Description: "Choose figaros unused for at least this long (30d, 12h, 2w)"},
			{Long: "empty", IsBool: true, Description: "Choose figaros that never had a message"},
			{Long: "older", Description: "Choose figaros born at least this long ago"},
			{Long: "outfit", Description: "Choose figaros born under this outfit"},
			{Long: "under", Description: "Choose figaros presented under this aria or form"},
			{Long: "match", Description: "Choose figaros whose mantra, handle or cwd match (fzf syntax)"},
			{Long: "box", Description: "Choose figaros in this box"},
			{Long: "yes", Short: "y", IsBool: true, Description: "Apply the plan without asking"},
			{Long: "dry-run", IsBool: true, Description: "Print the plan only"},
			{Long: "undo", Description: "Restore every figaro one run reaped"},
			{Long: "json", Short: "j", IsBool: true, Description: "Print the plan (or with --yes, the result) as JSON"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			o, err := gcOptsOf(ctx)
			if err != nil {
				dieUsage("gc: %s", err)
			}
			runGCCommand(ctx.Extra.(*config.Loaded), o)
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
		Name:    "name",
		Group:   "Session",
		Short:   "Print an aria's name the way this CLI shows it",
		Usage:   "name [<id> | --id <id>] [-m]",
		Long:    "Prints the name [cli] ids chooses for the named aria (or the one bound to\nthis shell, or FIGARO_ARIA inside a tool): its word handle, or its short\nhex. For a script or a reply footer that should name an aria the way\nfigaro does.\n\n  -m/--more   add the other spelling in parentheses",
		ArgsMin: 0,
		ArgsMax: 1,
		Flags: []cmdkit.FlagDef{
			{Long: "id", Description: "Target aria id"},
			{Long: "more", Short: "m", IsBool: true, Description: "Add the other spelling"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runName(ctx.Extra.(*config.Loaded), ctx.Flag("id"), ctx.Args, ctx.BoolFlag("more"))
			return nil
		},
		CompleteArgs: completeAriaIDsPositionalOrFlag,
	})

	r.Register(&cmdkit.Command{
		Name:  "provider",
		Group: "System",
		Short: "Sign in to, sign out of, and list model providers",
		Usage: "provider <login | logout | ls> [<name>] [-j]",
		Long: `Model credentials, one provider at a time.

  login <name>    run the provider's sign-in: OAuth or device code where it
                  has one, else ask for an API key and seal it in hush
  logout <name>   remove what figaro holds for it: the hush credential and
                  a sealed api_key. A key in the environment, a key file,
                  a service credential or a plaintext api_key you wrote is
                  left alone and named; logout exits 1 while one remains
  ls [<name>]     every source of each provider's credential, in the order
                  figaro tries them; the first is the one in use

logout removes figaro's copy only: a token the provider issued stays valid
there until you revoke it with the provider.`,
		Flags: []cmdkit.FlagDef{
			{Long: "json", Short: "j", IsBool: true, Description: "JSON for ls and logout"},
		},
		ArgsMin: 1,
		ArgsMax: 2,
		Run: func(ctx *cmdkit.RunContext) error {
			return runProviderVerb(ctx.Extra.(*config.Loaded), ctx.Args, ctx.BoolFlag("json"))
		},
		CompleteArgs: completeWords([][2]string{
			{"login", "sign in to a provider"},
			{"logout", "remove figaro's credential for a provider"},
			{"ls", "where each provider's credential comes from"},
		}, completeProviderAfterVerb),
	})

	registerTroupe(r)
	r.Register(boxVerb)

	r.Register(&cmdkit.Command{
		Name:  "models",
		Group: "System",
		Short: "List the models every provider offers, from the daemon's catalog",
		Usage: "models [--refresh] [-j]",
		Long: `Reads the catalog @models: instant, no provider is asked. The daemon
lists every configured provider in the background ([models] poll_minutes,
six hours by default), once at start when its snapshot is stale, and after
figaro provider login. Each provider shows when it was last listed, and the
error of its last attempt if that failed.

  figaro models             the catalog as the daemon holds it
  figaro models --refresh   list every provider now and wait for the answers
  figaro models -j          every entry, hidden ones included, as JSON`,
		Flags: []cmdkit.FlagDef{
			{Long: "refresh", IsBool: true, Description: "ask the daemon to list every provider now, and wait"},
			{Long: "json", Short: "j", IsBool: true, Description: "JSON output"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			ld := ctx.Extra.(*config.Loaded)
			return runModels(ld, ctx.BoolFlag("refresh"), ctx.BoolFlag("json"))
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "serve",
		Group: "System",
		Short: "Relay model requests to configured providers over loopback HTTP",
		Usage: "serve [-l <addr>] [-p <provider,...>]",
		Long:  "Runs a stateless, unauthenticated model router on a loopback address. It\ntouches no aria, store or daemon: each request is forwarded to a provider\nwith figaro's credential for it, and the response streams back unchanged.\n\n  /u/<provider>/<path>                    base URL + path, any method\n  /v1/responses, /v1/chat/completions,    model \"<provider>/<model>\" picks\n  /v1/messages, /v1/systemone,            the provider; the prefix is removed\n  /v1/decisions\n  /v1/models                              every provider's models, prefixed\n\nDecision models go the same way: /v1/systemone (System One: jev,\ndecision-1) and /v1/decisions (OpenAI: luna), model \"<provider>/<model>\".\n\nRequests whose Host is not a loopback name, or that carry a non-loopback\nOrigin, are refused.",
		Flags: []cmdkit.FlagDef{
			{Long: "listen", Short: "l", Description: "loopback address to listen on (default 127.0.0.1:8788)"},
			{Long: "provider", Short: "p", Description: "comma-separated providers to serve (default: every one with a credential)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			return runServe(ctx.Extra.(*config.Loaded), ctx.Flag("listen"), ctx.Flag("provider"))
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "stop",
		Aliases: []string{"rest"},
		Group:   "System",
		Short:   "Shut down the angelus daemon and its hush agent",
		Usage:   "stop [-f] [-k] [--forget-bindings] [--hup]",
		Long:    "Stops the angelus, then retires the embedded hush agent that daemon\nspawned: it is figaro's own binary re-exec'd, and nothing else would ever\nretire it. An EXTERNAL hush agent is never touched.\n\nOne agent can have several daemons (the share-hush, snapshot and sandbox\ndev shells share a hush surface across isolated runtime dirs). It is\nretired only when no other live daemon is still using it; otherwise stop\nnames the one that is holding it.\n\nWith no daemon answering, the agent is still retired: one outlives a\ndaemon that was killed rather than stopped, which is where orphans come\nfrom.\n\nEvery shell's binding outlives the daemon: the next angelus binds each\nshell that is still running to the aria it attended, however this one\nended (stop, upgrade, a unit restart, a crash). --forget-bindings is the\none way to start the next angelus with no shell attended.\n\nA turn the stop cuts off resumes on its own when the next angelus starts,\nwith a note telling the aria it was interrupted, and every queued message\nis delivered then. --hup is the brake: it hangs up every running turn\nfirst, as `figaro hup` would, so none of them resumes. Queued messages are\nkept either way; `figaro cut <aria>` drops one aria's queue.\n\nThe angelus respawns on the next command, so this is how you pick up a\nrebuilt binary.",
		Flags: []cmdkit.FlagDef{
			{Long: "force", Short: "f", IsBool: true, Description: "SIGKILL instead of graceful shutdown"},
			{Long: "keep-agent", Short: "k", IsBool: true, Description: "A restart is coming: keep the unlocked hush agent"},
			{Long: "forget-bindings", IsBool: true, Description: "The next angelus starts with no shell attended"},
			{Long: "hup", IsBool: true, Description: "Hang up every running turn first, so none resumes after the restart"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runStop(stopOptions{
				force:          ctx.BoolFlag("force"),
				keepAgent:      ctx.BoolFlag("keep-agent"),
				forgetBindings: ctx.BoolFlag("forget-bindings"),
				hup:            ctx.BoolFlag("hup"),
			})
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:   "upgrade",
		Group:  "System",
		Hidden: true,
		Usage:  "upgrade --to <figaro> [-n] [--if-changed] [--wait <seconds>] [--force] [--tmux-socket <name>]",
		Short:  "Restart the angelus on a build already installed, and the tmux panes that cannot reconnect",
		Flags: []cmdkit.FlagDef{
			{Long: "to", Description: "The installed figaro binary to move to"},
			{Long: "dry-run", Short: "n", IsBool: true, Description: "Print the plan and change nothing"},
			{Long: "if-changed", IsBool: true, Description: "Do nothing when the running build is already this one"},
			{Long: "wait", Description: "Seconds to wait for running turns (default 120)"},
			{Long: "force", IsBool: true, Description: "Interrupt turns still running after --wait"},
			{Long: "tmux-socket", Description: "The tmux server's socket name (tmux -L)"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			wait := 120 * time.Second
			if s := ctx.Flag("wait"); s != "" {
				n, err := strconv.Atoi(s)
				if err != nil || n < 0 {
					die("--wait takes a number of seconds, got %q", s)
				}
				wait = time.Duration(n) * time.Second
			}
			runUpgrade(upgradeOptions{
				dryRun:     ctx.BoolFlag("dry-run"),
				ifChanged:  ctx.BoolFlag("if-changed"),
				force:      ctx.BoolFlag("force"),
				to:         ctx.Flag("to"),
				wait:       wait,
				tmuxSocket: ctx.Flag("tmux-socket"),
			})
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:    "version",
		Aliases: []string{"v"},
		Group:   "System",
		Short:   "Print build identity (revision, exe path, Go version) and the reachable daemon's node id",
		Usage:   "version [-j]",
		Flags: []cmdkit.FlagDef{
			{Long: "json", Short: "j", IsBool: true, Description: "One line of JSON"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			runVersion(ctx.BoolFlag("json"))
			return nil
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "keys",
		Group: "System",
		Short: "Show and edit the pager's keybindings (~/.config/figaro/keys.toml)",
		Usage: "keys [layers | actions | init | bind <keys> <action> [<layer>] | unbind <keys> [<layer>] | reset [<keys> [<layer>]] | path]",
		Long: `The live key map, and the file behind it.

A key is written the way the help panel prints it: a letter, ^D, M-m, Tab,
Esc, Enter, Up, PgDn, Space. ^X claims both encodings a terminal may send.

Bindings live in LAYERS, highest first; a layer covers some modes, and a
binding claims its key only in the modes where its action means something,
so "down" on j is a line in the transcript, the cursor in visual mode and
the selection in a list. A PREFIX layer is read by the key after the one
that arms it (g, f, c, Space).

  figaro keys                          every layer, its modes, its bindings
  figaro keys actions                  every verb, and where it means something
  figaro keys init                     start a keys.toml from the shipped recipes
  figaro keys bind J half-down         in the user layer, above all the others
  figaro keys bind w status leader     Space w opens the status pit
  figaro keys unbind d motions         d does nothing where motions apply
  figaro keys reset                    back to the defaults
  figaro doctor keys                   what the file asks that figaro refused

keys.toml is read when the process starts, and is the client's own file:
the daemon neither reads nor writes it.`,
		ArgsMax: 3,
		Run: func(ctx *cmdkit.RunContext) error {
			return runKeys(ctx.Extra.(*config.Loaded), ctx.Args)
		},
	})

	r.Register(&cmdkit.Command{
		Name:  "doctor",
		Group: "System",
		Short: "Store and provider health: gc removes dead channels; schema reports channel versions; mem reports the daemon's footprint; pprof arms the profiler on the running daemon; provider shows recent provider round-trips (including in-flight) and the decision routes methods have stopped asking; librettos recounts derived-form observers; normalize makes every aria own its history and drops the overrides its history now agrees with; skills lists what the binary ships; ttl lists the nodes carrying a lifetime and when it is spent; snapshot copies the most recently active arias, with their history and every form, into a new store; quarantine lists the segments a corrupt frame keeps closed and releases one by cutting it at the bad frame, after copying it aside (owner only); unit says whether the daemon on the socket is the systemd unit's",
		Usage: "doctor <gc [--dry-run] | schema | term | mem [-j] [--gc] | pprof <on | off> | provider [--id <aria>] [-c N] [-j] | librettos [--dry-run] | normalize [--dry-run] [-j] | skills [-j] | keys | ttl [-j] | unit [-j] | snapshot <dir> [--from <store>] [--arias N] [--max-mb M] [-j] | quarantine [ls | release <figaro> --at <offset> [--channel <name>] [--yes]]>",
		Flags: []cmdkit.FlagDef{
			{Long: "dry-run", Short: "n", IsBool: true, Description: "Report what would be removed without touching the store"},
			{Long: "json", Short: "j", IsBool: true, Description: "Machine-readable output (mem, provider, normalize)"},
			{Long: "id", Description: "Restrict to one aria (provider)"},
			// -n is already dry-run on this command.
			{Long: "count", Short: "c", Description: "How many rows to show (provider); default 20"},
			{Long: "gc", IsBool: true, Description: "mem: collect first, then report -- the only honest live-set reading (stops the world)"},
			{Long: "from", Description: "snapshot: the store to copy from (default: this one)"},
			{Long: "arias", Description: "snapshot: how many of the most recently active arias (default 50, 0 = all)"},
			{Long: "max-mb", Description: "snapshot: the size cap in MB (default 500, 0 = none)"},
			{Long: "at", Description: "quarantine release: the corrupt frame's byte offset, as ls prints it"},
			{Long: "channel", Description: "quarantine release: the channel, when several hold a bad frame at that offset"},
			{Long: "yes", Short: "y", IsBool: true, Description: "quarantine release: release without asking"},
		},
		Run: func(ctx *cmdkit.RunContext) error {
			if len(ctx.Args) == 2 && ctx.Args[0] == "pprof" {
				return runDoctorPprof(ctx.Args[1])
			}
			if len(ctx.Args) >= 1 && ctx.Args[0] == "quarantine" {
				return runDoctorQuarantine(ctx.Args[1:], ctx.Flag("at"), ctx.Flag("channel"), ctx.BoolFlag("json"), ctx.BoolFlag("yes"))
			}
			if len(ctx.Args) == 2 && ctx.Args[0] == "snapshot" {
				return runDoctorSnapshot(ctx.Args[1], ctx.Flag("from"), ctx.Flag("arias"), ctx.Flag("max-mb"), ctx.BoolFlag("json"))
			}
			if len(ctx.Args) != 1 {
				return fmt.Errorf("usage: doctor <gc [--dry-run] | schema | term | mem [-j] [--gc] | pprof <on | off> | provider [--id <aria>] [-c N] [-j] | librettos [--dry-run] | normalize [--dry-run] [-j] | toolcalls [--dry-run] | skills [-j] | keys | ttl [-j] | unit [-j] | snapshot <dir> [--from <store>] [--arias N] [--max-mb M] [-j] | quarantine [ls | release <figaro> --at <offset> [--channel <name>] [--yes]]>")
			}
			switch ctx.Args[0] {
			case "gc":
				return runDoctorGC(ctx.BoolFlag("dry-run"))
			case "schema":
				return runDoctorSchema()
			case "term":
				return runDoctorTerm()
			case "mem":
				return runDoctorMem(ctx.BoolFlag("json"), ctx.BoolFlag("gc"))
			case "provider":
				return runDoctorProvider(ctx.Flag("id"), ctx.Flag("count"), ctx.BoolFlag("json"))
			case "librettos":
				return runDoctorLibrettos(ctx.BoolFlag("dry-run"))
			case "normalize":
				runNormalize(ctx.Extra.(*config.Loaded), ctx.BoolFlag("dry-run"), ctx.BoolFlag("json"))
				return nil
			case "toolcalls":
				return runDoctorToolCalls(ctx.BoolFlag("dry-run"))
			case "keys":
				return runDoctorKeys(ctx.Extra.(*config.Loaded))
			case "skills":
				return runDoctorSkills(loaded, ctx.BoolFlag("json"))
			case "ttl":
				return runDoctorTTL(loaded, ctx.BoolFlag("json"))
			case "unit":
				return runDoctorUnit(ctx.BoolFlag("json"))
			}
			return fmt.Errorf("usage: doctor <gc [--dry-run] | schema | term | mem [-j] [--gc] | pprof <on | off> | provider [--id <aria>] [-c N] [-j] | librettos [--dry-run] | normalize [--dry-run] [-j] | toolcalls [--dry-run] | skills [-j] | keys | ttl [-j] | unit [-j] | snapshot <dir> [--from <store>] [--arias N] [--max-mb M] [-j] | quarantine [ls | release <figaro> --at <offset> [--channel <name>] [--yes]]>")
		},
		CompleteArgs: completeWords([][2]string{
			{"gc", "remove dead channels"}, {"schema", "channel versions"}, {"term", "terminal capabilities"},
			{"mem", "the daemon's footprint"}, {"pprof", "arm or disarm the live profiler"}, {"provider", "recent provider round-trips"},
			{"librettos", "recount derived-form observers"}, {"normalize", "detach promoted arias, drop overrides history agrees with"},
			{"skills", "what the binary ships"}, {"ttl", "nodes carrying a lifetime"},
			{"unit", "whether the daemon on the socket is the systemd unit's"},
			{"snapshot", "copy recent arias into a new store"},
			{"quarantine", "segments a corrupt frame keeps closed, and releasing one"},
		}, nil),
	})

	r.Register(&cmdkit.Command{
		Name:    "vault",
		Aliases: []string{"hush"},
		Group:   "System",
		Short:   "Inspect and repair figaro's embedded secrets vault",
		Usage:   "vault <status | init | reset | forget | unlock | lock> [--file | --passphrase] [--yes]",
		Long: `figaro runs its own hush: its own identity, agent, and keyring entry
(service "figaro"). The hush binary on PATH addresses a different
instance, so these are the levers for this one.

  status   mode, identity, agent, and whether the saved passphrase works
  init     create the identity: --file writes a random passphrase to a
           0600 file and never asks you anything, --passphrase asks
  reset    throw the identity away, make a new one, and walk back
           through the provider logins it was holding
  forget   clear the saved passphrase; the next command prompts
  unlock   prompt, verify, save, and start the agent
  lock     stop the agent, dropping the decrypted identity

With neither --file nor --passphrase, init and reset pick the file when
no OS keyring answers, since a passphrase with nowhere to be remembered
is a question asked on every command.

What reset costs you: the provider tokens this identity encrypted, and
nothing else. Arias, forms and outfits are not encrypted with it. The
old identity and token files are renamed, not deleted.`,
		Flags: []cmdkit.FlagDef{
			{Long: "file", IsBool: true, Description: "Unlock from a 0600 passphrase file: no prompt, ever"},
			{Long: "passphrase", IsBool: true, Description: "Unlock from a passphrase you type"},
			{Long: "yes", Short: "y", IsBool: true, Description: "Skip the reset confirmation"},
		},
		ArgsMin: 1,
		ArgsMax: 1,
		Run: func(ctx *cmdkit.RunContext) error {
			kind, err := vaultUnlockKindFromFlags(ctx.BoolFlag("file"), ctx.BoolFlag("passphrase"))
			if err != nil {
				return err
			}
			switch ctx.Args[0] {
			case "status":
				return runVaultStatus()
			case "init":
				return runVaultInit(kind)
			case "reset":
				return runVaultReset(ctx.Extra.(*config.Loaded), kind, ctx.BoolFlag("yes"))
			case "forget":
				return runVaultForget()
			case "unlock":
				return runVaultUnlock()
			case "lock":
				return runVaultLock()
			}
			return fmt.Errorf("usage: vault <status | init | reset | forget | unlock | lock>")
		},
		CompleteArgs: func(ctx *cmdkit.CompleteContext) []string {
			return []string{"status", "init", "reset", "forget", "unlock", "lock"}
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
		CompleteArgs: completeWords([][2]string{
			{"bash", "print the bash script"}, {"zsh", "print the zsh script"}, {"fish", "print the fish script"},
			{"powershell", "print the powershell script"}, {"install", "write it where the shell autoloads it"},
		}, nil),
	})

	r.SetBarePromptComplete(completePromptContext)

	registerDecide(r)

	// The bare form is not a registered command, so it needs its own usage line.
	r.Synopsis = []string{
		progName + " [send flags] -- <prompt>   (same flags as `" + progName + " send`)",
		progName + "                            at a terminal: the pager, as `" + progName + " listen`",
	}
	// Consumed by extractNoBindFlag before dispatch, so no Command declares them.
	r.GlobalFlags = []cmdkit.FlagDef{
		{Long: "no-bind", Short: "A", IsBool: true,
			Description: "Absolute mode: ignore this shell's attend binding (alias --absolute; FIGARO_NO_BIND=1)"},
		{Long: "bind", IsBool: true,
			Description: "Use the binding even where it is off by default (--no-bind, FIGARO_NO_BIND)"},
	}

	return r
}
