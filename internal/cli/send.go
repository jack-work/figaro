package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

// sendOpts captures the parsed flag state of the send command.
type sendOpts struct {
	id        string
	target    string // positional [<trunk>]:<LT> target (alt to --id)
	stay      bool   // --attend=false / --stay: don't rebind to the new branch
	ephemeral bool
	raw       bool // --raw / -r: raw stream, no ANSI/markdown
	verbatim  bool // --verbatim / -v: dump raw wire frames as JSON
	verbose   bool // --verbose / -o (or -t alias): expand tool inputs (Ctrl-O toggles live)
	exec      bool
	dryRun    bool   // --exec only
	skipYes   bool   // --exec only
	forget    bool   // --forget / -f: submit and exit; do not stream
	json      bool   // --json / -j: emit machine-readable result on stdout ({aria_id, ...})
	listen    bool   // --listen / -l: auto-enter the transcript at startup
	loadout   string // --loadout / -L: the loadout a CREATED aria is minted on
	record    string // --record: write a wire tape of this stream (testing)
}

// extractSendFlags scans a PassRaw arg list for the send command's
// recognized flags: --id, --ephemeral/-e, --exec/-x, --dry-run/-n,
// --yes/-y. Returns the parsed opts and the residual args (which
// still include the `--` boundary and the prompt body).
//
// Bundled short flags (e.g. -ex, -ey) are expanded. Everything
// after `--` is untouched.
//
// Nothing before `--` is discarded: a token that is neither a known flag
// nor the single positional target is an error. Silently swallowing argv
// is how `--id` came to be ignored on the bare `figaro -- <prompt>` form
// for the life of the tool.
func extractSendFlags(args []string) (sendOpts, []string, error) {
	return extractPromptFlags(args, false)
}

// extractForkFlags is extractSendFlags for `figaro fork`, where the prompt
// is OPTIONAL: `fork <id>:12` is a complete gesture, so a bare positional
// is the target even with no `--` boundary in sight. Sharing the parser is
// deliberate — fork and send must not drift apart on what a flag or a
// coordinate means.
func extractForkFlags(args []string) (sendOpts, []string, error) {
	return extractPromptFlags(args, true)
}

// extractPromptFlags is the shared implementation. bareTarget says whether a
// positional is allowed without a `--` boundary (fork: yes; send: no, because
// there a boundary-less bare word is a prompt the user forgot to delimit).
func extractPromptFlags(args []string, bareTarget bool) (sendOpts, []string, error) {
	var opts sendOpts
	rest := make([]string, 0, len(args))

	// A bare positional is the target only in the `… -- <prompt>` form;
	// without a `--`, bare args are the prompt itself (except under
	// bareTarget, where the prompt is optional to begin with).
	hasDoubleDash := false
	for _, a := range args {
		if a == "--" {
			hasDoubleDash = true
			break
		}
	}

	// One expander, one table: the letters come from sendFlagDefs, so a
	// short cannot be documented and un-gangable at once (which is how
	// `send -fj` failed while `-f -j` worked).
	expanded := cmdkit.ExpandBundled(argsBeforeBoundary(args), sendFlagDefs)
	expanded = append(expanded, argsFromBoundary(args)...)

	i := 0
	for i < len(expanded) {
		a := expanded[i]
		if a == "--" {
			rest = append(rest, expanded[i:]...)
			return opts, rest, nil
		}
		switch {
		case a == "--id":
			if i+1 >= len(expanded) || expanded[i+1] == "--" {
				return opts, nil, fmt.Errorf("--id requires a value")
			}
			if opts.id != "" {
				return opts, nil, fmt.Errorf("--id given more than once")
			}
			opts.id = expanded[i+1]
			if _, _, _, err := parseTarget(opts.id); err != nil {
				return opts, nil, err
			}
			i += 2
			continue
		case strings.HasPrefix(a, "--id="):
			if opts.id != "" {
				return opts, nil, fmt.Errorf("--id given more than once")
			}
			opts.id = strings.TrimPrefix(a, "--id=")
			if opts.id == "" {
				return opts, nil, fmt.Errorf("--id requires a value")
			}
			if _, _, _, err := parseTarget(opts.id); err != nil {
				return opts, nil, err
			}
			i++
			continue
		case a == "--loadout", a == "-L":
			if i+1 >= len(expanded) || expanded[i+1] == "--" {
				return opts, nil, fmt.Errorf("--loadout requires a value")
			}
			if opts.loadout != "" {
				return opts, nil, fmt.Errorf("--loadout given more than once")
			}
			opts.loadout = expanded[i+1]
			i += 2
			continue
		case strings.HasPrefix(a, "--loadout="):
			if opts.loadout != "" {
				return opts, nil, fmt.Errorf("--loadout given more than once")
			}
			opts.loadout = strings.TrimPrefix(a, "--loadout=")
			if opts.loadout == "" {
				return opts, nil, fmt.Errorf("--loadout requires a value")
			}
			i++
			continue
		case a == "--record":
			if i+1 >= len(expanded) || expanded[i+1] == "--" {
				return opts, nil, fmt.Errorf("--record requires a path")
			}
			opts.record = expanded[i+1]
			i += 2
			continue
		case strings.HasPrefix(a, "--record="):
			opts.record = strings.TrimPrefix(a, "--record=")
			if opts.record == "" {
				return opts, nil, fmt.Errorf("--record requires a path")
			}
			i++
			continue
		case a == "--ephemeral", a == "-e":
			opts.ephemeral = true
			i++
			continue
		case a == "--raw", a == "-r":
			opts.raw = true
			i++
			continue
		case a == "--verbatim", a == "-v":
			opts.verbatim = true
			i++
			continue
		case a == "--verbose", a == "--expand", a == "-o", a == "--thinking", a == "-t":
			opts.verbose = true
			i++
			continue
		case a == "--listen", a == "-l":
			opts.listen = true
			i++
			continue
		case a == "--exec", a == "-x":
			opts.exec = true
			i++
			continue
		case a == "--dry-run", a == "-n":
			opts.dryRun = true
			i++
			continue
		case a == "--yes", a == "-y":
			opts.skipYes = true
			i++
			continue
		case a == "--forget", a == "-f":
			opts.forget = true
			i++
			continue
		case a == "--json", a == "-j":
			opts.json = true
			i++
			continue
		case a == "--stay", a == "--no-attend", a == "--attend=false", a == "--attend=0":
			opts.stay = true
			i++
			continue
		case a == "--attend", a == "--attend=true", a == "--attend=1":
			opts.stay = false
			i++
			continue
		}
		// First bare positional before a `--` boundary is the target
		// ([<trunk>]:<LT>).
		if (hasDoubleDash || bareTarget) && opts.target == "" && opts.id == "" && a != "" && !strings.HasPrefix(a, "-") {
			opts.target = a
			i++
			continue
		}
		// Anything else before `--` is unconsumed argv. Never drop it.
		switch {
		case strings.HasPrefix(a, "-"):
			return opts, nil, fmt.Errorf("unknown flag %q (flags go before `--`; everything after `--` is the prompt)", a)
		case !hasDoubleDash && !bareTarget:
			return opts, nil, fmt.Errorf("the prompt must follow `--` (got bare argument %q)", a)
		default:
			return opts, nil, fmt.Errorf("unexpected argument %q (the target is already %q)", a, opts.target+opts.id)
		}
	}
	return opts, rest, nil
}

// sendFlagDefs is the single source of truth for the prompt verbs' flags:
// bundle expansion reads it, and it is what keeps the hand-rolled PassRaw
// scan and the router's parser speaking the same language.
var sendFlagDefs = []cmdkit.FlagDef{
	{Long: "id", Description: "Target aria id"},
	{Long: "ephemeral", Short: "e", IsBool: true, Description: "One-shot in-memory aria"},
	{Long: "raw", Short: "r", IsBool: true, Description: "Raw stream: no ANSI, no markdown"},
	{Long: "verbatim", Short: "v", IsBool: true, Description: "Dump the wire frames as JSON"},
	{Long: "verbose", Short: "o", IsBool: true, Description: "Expand full tool inputs"},
	{Long: "thinking", Short: "t", IsBool: true, Description: "Alias of --verbose"},
	{Long: "listen", Short: "l", IsBool: true, Description: "Open the transcript at startup"},
	{Long: "record", Description: "Record the aria wire to a tape file (testing)"},
	{Long: "exec", Short: "x", IsBool: true, Description: "Treat the prompt as a bash instruction"},
	{Long: "dry-run", Short: "n", IsBool: true, Description: "--exec only: print the script"},
	{Long: "yes", Short: "y", IsBool: true, Description: "--exec only: skip confirmation"},
	{Long: "forget", Short: "f", IsBool: true, Description: "Submit and exit; do not stream"},
	{Long: "json", Short: "j", IsBool: true, Description: "Submit, print one JSON object, exit"},
	{Long: "loadout", Short: "L", Description: "Loadout for an aria this call CREATES (-e, or an unattended shell)"},
}

// argsBeforeBoundary / argsFromBoundary split argv at the first bare `--`.
// Everything after it is the prompt and must never be touched.
func argsBeforeBoundary(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[:i]
		}
	}
	return args
}

func argsFromBoundary(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i:]
		}
	}
	return nil
}

// parseTarget splits a target spec into a trunk id and an optional :<turn>.
// "" -> bound trunk, no turn. ":6" -> bound trunk at turn 6. "t1:6" -> trunk
// t1 at turn 6. "t1" -> trunk t1, no turn.
//
// The suffix is a TURN ID, not an LT. LT is the model's coordinate — it counts
// the steps the model experienced — and it is the wrong thing to hand a human,
// because most LTs sit mid-tool where a fork would strand a tool_invoke without
// its result. A turn is the exchange: your question and everything the agent
// did about it. `figaro show` prints the turn id; that is the number you type.
//
// Shared by `send` and `fork` so the two cannot drift apart.
func parseTarget(spec string) (trunk string, turn uint64, hasTurn bool, err error) {
	if spec == "" {
		return "", 0, false, nil
	}
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		trunk = spec[:i]
		n, perr := strconv.ParseUint(spec[i+1:], 10, 64)
		if perr != nil || n == 0 {
			return "", 0, false, fmt.Errorf("bad :<turn> in %q (want [<trunk>]:<n>, turns start at 1)", spec)
		}
		turn, hasTurn = n, true
	} else {
		trunk = spec
	}
	if trunk != "" {
		if verr := validateSendID(trunk); verr != nil {
			return "", 0, false, verr
		}
	}
	return trunk, turn, hasTurn, nil
}

// validateSendID wraps rpc.ValidateAriaID with a friendlier error
// prefix. Pulled out so extractSendFlags reads cleanly.
func validateSendID(id string) error {
	if err := rpc.ValidateAriaID(id); err != nil {
		return fmt.Errorf("--id %q: %w", id, err)
	}
	return nil
}

// runSend is the unified send dispatcher. Branches:
//
//	--ephemeral + --id    -> error (contradictory)
//	--exec                -> bash wrapper; --raw is silently ignored
//	                         (the script governs its own output)
//	--ephemeral           -> one-shot in-memory aria, killed after
//	--raw                 -> raw stream, no ANSI/markdown
//	(no flags)            -> bound/named aria, interactive stream
//
// Persistence (--ephemeral) and formatting (--raw) are orthogonal.
func runSend(loaded *config.Loaded, rawArgs []string) {
	runSendAs(loaded, "send", rawArgs)
}

// runSendAs is runSend with the verb that appears in diagnostics. The bare
// `figaro [flags] -- <prompt>` form dispatches here too — one parser, one
// set of semantics — and labels its errors with the program name instead.
func runSendAs(loaded *config.Loaded, verb string, rawArgs []string) {
	opts, rest, err := extractSendFlags(rawArgs)
	if err != nil {
		dieUsage("%s: %s", verb, err)
	}
	prompt := extractPrompt(rest)
	if prompt == "" {
		// A boundary with nothing after it is an INVITATION, not a mistake:
		// open the editor and send what gets written. That is what lets a `q`
		// alias expanding to `figaro --` be typed with no arguments at all.
		//
		// Only when a boundary was actually given. `figaro send` with no `--`
		// at all is still a usage error, because the boundary is what says "a
		// prompt belongs here" and dropping that would make every typo'd flag
		// open an editor.
		if hasDashBoundary(rest) {
			text, cerr := composePrompt("Write a prompt. Markdown is fine.")
			if cerr != nil {
				if _, cancelled := cerr.(composeCancelled); cancelled {
					return // nothing written; not an error
				}
				die("%s: %s", verb, cerr)
			}
			prompt = text
		} else {
			flags := "[--id <id>] [-e|--ephemeral] [-r|--raw] [-v|--verbatim] [-x|--exec] [-n] [-y] -- <prompt>"
			if verb == "send" {
				dieUsage("usage: figaro send %s", flags)
			}
			dieUsage("usage: %s %s", verb, flags)
		}
	}

	spec := opts.id
	if spec == "" {
		spec = opts.target
	}
	trunkID, turn, hasTurn, perr := parseTarget(spec)
	if perr != nil {
		dieUsage("%s: %s", verb, perr)
	}

	if opts.ephemeral && (opts.id != "" || opts.target != "") {
		dieUsage("%s: --ephemeral and a target are contradictory", verb)
	}
	if err := validateSendOpts(opts, hasTurn); err != nil {
		dieUsage("%s: %s", verb, err)
	}
	if opts.json {
		// The turn is submitted and not attached to — exactly --forget, which
		// already knows how to emit the object.
		opts.forget = true
	}

	set := renderSettings{verbose: opts.verbose, listen: opts.listen, record: opts.record}

	// `send <trunk>:<turn>` — fork at that turn, then send. The message lands
	// on whichever trunk we end up attended to: the new alternative by default
	// (rebind), or the original with --attend=false/--stay.
	if hasTurn {
		runSendForkAt(loaded, trunkID, turn, opts.stay, opts.json, prompt, set)
		return
	}
	// No turn: a positional target is just the aria to send to.
	if opts.id == "" {
		opts.id = trunkID
	} else {
		opts.id = trunkID // strip any :<turn> that parseTarget already consumed
	}

	switch {
	case opts.forget:
		runSendForget(loaded, opts, prompt)
	case opts.verbatim:
		runSendVerbatim(loaded, opts, prompt)
	case opts.exec:
		runSendExec(loaded, opts, prompt)
	case opts.ephemeral && opts.raw:
		runSendEphemeralRaw(loaded, opts, prompt)
	case opts.ephemeral:
		runSendEphemeralRich(loaded, opts, prompt, set)
	case opts.raw:
		runSendRaw(loaded, opts.id, opts.loadout, prompt)
	default:
		// Today's interactive send: pid-bound or --id named.
		if opts.id == "" {
			runPrompt(loaded, opts.loadout, prompt, set)
			return
		}
		promptAria(loaded, opts.id, prompt, set)
	}
}

// validateSendOpts holds every "these flags contradict" rule for the prompt
// verbs as a PURE function: it decides, never exits, never opens a socket.
// That matters — inline in runSendAs, the only way to test a rejection was
// to call the dispatcher, and a dispatcher past its guard reaches
// mustConnectAngelus, which in a test binary is a fork bomb.
//
// --json is a MODE (submit, one object, exit), so anything that renders,
// streams or takes the terminal contradicts it. Saying so is the point:
// dropping -j quietly made `send -j` a no-op for the life of the flag.
func validateSendOpts(opts sendOpts, hasTurn bool) error {
	if (opts.dryRun || opts.skipYes) && !opts.exec {
		return fmt.Errorf("-n / -y only meaningful with --exec")
	}
	if opts.forget && (opts.exec || opts.verbatim) {
		return fmt.Errorf("--forget contradicts --exec/--verbatim")
	}
	if opts.forget && opts.ephemeral {
		return fmt.Errorf("--forget contradicts --ephemeral (the aria would be killed before the turn ran)")
	}
	if hasTurn && (opts.ephemeral || opts.exec || opts.verbatim) {
		return fmt.Errorf("<trunk>:<turn> is not compatible with --ephemeral/--exec/--verbatim")
	}
	if opts.loadout != "" && !opts.ephemeral && (opts.id != "" || opts.target != "" || hasTurn) {
		return fmt.Errorf("--loadout applies to an aria this call creates; a target names one that already exists (use -e, or drop the target)")
	}
	if opts.json {
		if bad := jsonIncompatible(opts); bad != "" {
			return fmt.Errorf("--json contradicts %s (--json submits and exits; there is no stream to shape)", bad)
		}
		if opts.ephemeral {
			return fmt.Errorf("--json contradicts --ephemeral (the aria would be killed before the turn ran)")
		}
	}
	return nil
}

// jsonIncompatible names the first flag that cannot survive --json's
// contract. --raw and --verbatim want the stream itself; --exec hands stdout
// to a script; --listen and --verbose shape a render that will not happen.
func jsonIncompatible(opts sendOpts) string {
	switch {
	case opts.raw:
		return "--raw"
	case opts.verbatim:
		return "--verbatim"
	case opts.exec:
		return "--exec"
	case opts.listen:
		return "--listen"
	case opts.verbose:
		return "--verbose"
	}
	return ""
}

// runSendEphemeralRaw spins an ephemeral aria, streams raw output
// to stdout, kills it. Today's `figaro plain` with no --id.
func runSendEphemeralRaw(loaded *config.Loaded, opts sendOpts, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, opts.loadout, nil) })
	if err != nil {
		die("create figaro: %s", err)
	}
	figaroID := createResp.FigaroID
	figaroEP := transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer killCancel()
		_ = acli.Kill(killCtx, figaroID, false)
	}()
	if err := waitForSocket(figaroEP.Address, 3*time.Second); err != nil {
		die("send: %s", err)
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	exitCode := plainPrompt(ctx, figaroEP, prompt, os.Stdout)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runSendEphemeralRich spins an ephemeral aria, interactive (rich)
// stream, kills it. Useful for one-off conversations the user wants
// to see formatted but not persist.
func runSendEphemeralRich(loaded *config.Loaded, opts sendOpts, prompt string, set renderSettings) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, opts.loadout, nil) })
	if err != nil {
		die("create figaro: %s", err)
	}
	figaroID := createResp.FigaroID
	figaroEP := transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer killCancel()
		_ = acli.Kill(killCtx, figaroID, false)
	}()
	if err := waitForSocket(figaroEP.Address, 3*time.Second); err != nil {
		die("send: %s", err)
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	mustPromptFigaro(ctx, figaroEP, figaroID, prompt, loaded, set)
}

// runSendRaw streams raw output from a persistent aria (bound, named, or
// minted here when this shell has none). The aria is left alive; only the
// formatting is raw.
func runSendRaw(loaded *config.Loaded, ariaID, loadout, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, figaroEP, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, true, loadout)
	if err != nil {
		die("%s", err)
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	exitCode := plainPrompt(ctx, figaroEP, prompt, os.Stdout)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runSendVerbatim dumps the raw wire frames (one JSON object per line:
// {"method","params"}) with no formatting — the literal protocol stream.
// Ephemeral when -e, else the bound/named aria (left alive).
func runSendVerbatim(loaded *config.Loaded, opts sendOpts, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	var figaroEP transport.Endpoint
	if opts.ephemeral {
		createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, opts.loadout, nil) })
		if err != nil {
			die("create figaro: %s", err)
		}
		figaroEP = transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
		defer func() {
			killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer killCancel()
			_ = acli.Kill(killCtx, createResp.FigaroID, false)
		}()
		if err := waitForSocket(figaroEP.Address, 3*time.Second); err != nil {
			die("send: %s", err)
		}
	} else {
		_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, opts.id, true, opts.loadout)
		if err != nil {
			die("%s", err)
		}
		figaroEP = ep
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	if exitCode := verbatimPrompt(ctx, figaroEP, prompt, os.Stdout); exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runSendExec implements the --exec branch. Ephemeral when no --id,
// otherwise scoped to the named aria (auto-created if missing).
func runSendExec(loaded *config.Loaded, opts sendOpts, instruction string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	var figaroEP transport.Endpoint
	if opts.ephemeral || opts.id == "" {
		createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, opts.loadout, nil) })
		if err != nil {
			die("create figaro: %s", err)
		}
		figaroEP = transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
		defer func() {
			killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer killCancel()
			_ = acli.Kill(killCtx, createResp.FigaroID, false)
		}()
		if err := waitForSocket(figaroEP.Address, 3*time.Second); err != nil {
			die("send: %s", err)
		}
	} else {
		_, ep, err := resolveTargetEndpoint(ctx, loaded, acli, opts.id, true, opts.loadout)
		if err != nil {
			die("%s", err)
		}
		figaroEP = ep
	}

	instruction = expandAtRefsForEndpoint(ctx, figaroEP, instruction)
	prompt := "You will write a bash script. Output ONLY raw bash, " +
		"no markdown fences, no prose, no commentary, no explanations. " +
		"The script will be executed verbatim via `bash -c`. " +
		"Instruction: " + instruction

	var buf bytes.Buffer
	exitCode := plainPrompt(ctx, figaroEP, prompt, &buf)
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	script := stripBashFences(buf.String())
	if strings.TrimSpace(script) == "" {
		die("figaro send --exec: empty script from agent")
	}

	if opts.dryRun {
		fmt.Print(script)
		if !strings.HasSuffix(script, "\n") {
			fmt.Println()
		}
		return
	}

	if !opts.skipYes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "--- figaro send --exec: about to execute ---")
		fmt.Fprintln(os.Stderr, script)
		fmt.Fprintln(os.Stderr, "--- press enter to run, ctrl-c to abort ---")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}

	sh := exec.Command("bash", "-c", script)
	sh.Stdin = os.Stdin
	sh.Stdout = os.Stdout
	sh.Stderr = os.Stderr
	if err := sh.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		die("figaro send --exec: bash: %s", err)
	}
}

// runSendForget submits a prompt and exits — fire-and-forget. The daemon
// keeps the turn alive; the CLI does not attach to the stream and never
// sends figaro.interrupt. Useful from scripts, or when you want a prompt
// to run and check on it later via `figaro show` / `figaro listen`.
func runSendForget(loaded *config.Loaded, opts sendOpts, prompt string) {
	// 30s, not 10: this call may now MINT the aria before submitting, and a
	// cold daemon plus a first-run loadout render does not fit in ten.
	//
	// TODO(perf): put this back to 10s once the `new`/`fork` latency work
	// lands. The extra 20s buys exactly one thing — the create — and that
	// cost is the thing being fixed there. A timeout widened for a slow path
	// outlives the slowness unless someone writes down when to close it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ariaID, figaroEP, err := resolveTargetEndpoint(ctx, loaded, acli, opts.id, true, opts.loadout)
	if err != nil {
		die("%s", err)
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)

	fcli, derr := figaro.DialClient(figaroEP, func(string, json.RawMessage) {})
	if derr != nil {
		die("connect figaro: %s", derr)
	}
	defer fcli.Close()

	if _, _, qerr := fcli.Qua(ctx, prompt, buildPromptChalkboard()); qerr != nil {
		die("prompt: %s", qerr)
	}
	if opts.json {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(struct {
			AriaID string `json:"aria_id"`
			Mode   string `json:"mode"`
		}{AriaID: ariaID, Mode: "forget"})
		return
	}
	fmt.Fprintf(os.Stderr, "forgot %s — use `figaro listen %s` to follow\n", ariaID, ariaID)
}
