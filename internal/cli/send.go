package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/term"
)

type sendOpts struct {
	id       string
	target   string
	stay     bool
	raw      bool
	verbatim bool
	verbose  bool
	exec     bool
	dryRun   bool
	skipYes  bool
	forget   bool
	json     bool
	listen   bool
	outfit   dressing
	// -O is outfit names only; -S carries k=v and JSON literals; -D key paths.
	outfitText string
	setText    string
	delText    string
	record     string
	cast       bool
}

// extractSendFlags returns the parsed opts and the residual args, `--` boundary
// and prompt included.
func extractSendFlags(args []string) (sendOpts, []string, error) {
	return extractPromptFlags(args, false)
}

// extractForkFlags: the prompt is optional, so a bare positional is the target
// without a `--`.
func extractForkFlags(args []string) (sendOpts, []string, error) {
	return extractPromptFlags(args, true)
}

// bareTarget allows a positional without a `--` boundary.
func extractPromptFlags(args []string, bareTarget bool) (sendOpts, []string, error) {
	var opts sendOpts
	rest := make([]string, 0, len(args))

	hasDoubleDash := false
	for _, a := range args {
		if a == "--" {
			hasDoubleDash = true
			break
		}
	}

	// The letters come from sendFlagDefs, so a short cannot be documented and
	// un-gangable at once.
	expanded := cmdkit.ExpandBundled(argsBeforeBoundary(args), sendFlagDefs)
	expanded = append(expanded, argsFromBoundary(args)...)

	i := 0
	for i < len(expanded) {
		a := expanded[i]
		if a == "--" {
			rest = append(rest, expanded[i:]...)
			return opts, rest, opts.armOutfit()
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
			if _, _, err := parseTarget(opts.id); err != nil {
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
			if _, _, err := parseTarget(opts.id); err != nil {
				return opts, nil, err
			}
			i++
			continue
		case a == "--outfit", a == "-O":
			if i+1 >= len(expanded) || expanded[i+1] == "--" {
				return opts, nil, fmt.Errorf("--outfit requires a value")
			}
			if err := opts.addOutfit(expanded[i+1]); err != nil {
				return opts, nil, err
			}
			i += 2
			continue
		case strings.HasPrefix(a, "--outfit="):
			if err := opts.addOutfit(strings.TrimPrefix(a, "--outfit=")); err != nil {
				return opts, nil, err
			}
			i++
			continue
		case a == "--set", a == "-S":
			if i+1 >= len(expanded) {
				return opts, nil, fmt.Errorf("--set requires a value")
			}
			opts.setText = joinTerms(opts.setText, expanded[i+1])
			i += 2
			continue
		case strings.HasPrefix(a, "--set="):
			opts.setText = joinTerms(opts.setText, strings.TrimPrefix(a, "--set="))
			i++
			continue
		case a == "--delete", a == "-D":
			if i+1 >= len(expanded) {
				return opts, nil, fmt.Errorf("--delete requires a value")
			}
			opts.delText = joinTerms(opts.delText, expanded[i+1])
			i += 2
			continue
		case strings.HasPrefix(a, "--delete="):
			opts.delText = joinTerms(opts.delText, strings.TrimPrefix(a, "--delete="))
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
		case a == "--cast", a == "-C":
			opts.cast = true
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
		if (hasDoubleDash || bareTarget) && opts.target == "" && opts.id == "" && a != "" && !strings.HasPrefix(a, "-") {
			opts.target = a
			i++
			continue
		}
		// Unconsumed argv is never dropped.
		switch {
		case strings.HasPrefix(a, "-"):
			return opts, nil, fmt.Errorf("unknown flag %q (flags go before `--`; everything after `--` is the prompt)", a)
		case !hasDoubleDash && !bareTarget:
			return opts, nil, fmt.Errorf("the prompt must follow `--` (got bare argument %q)", a)
		default:
			return opts, nil, fmt.Errorf("unexpected argument %q (the target is already %q)", a, opts.target+opts.id)
		}
	}
	return opts, rest, opts.armOutfit()
}

// Repeats compose: `-O a -O b` means `-O a,b`.
func (o *sendOpts) addOutfit(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("--outfit requires a value")
	}
	o.outfitText = joinTerms(o.outfitText, text)
	return nil
}

func joinTerms(have, more string) string {
	if strings.TrimSpace(have) == "" {
		return more
	}
	return have + "," + more
}

func (o *sendOpts) armOutfit() error {
	d, err := parseDress(o.outfitText, o.setText, o.delText)
	if err != nil {
		return err
	}
	o.outfit = d
	return nil
}

// sendFlagDefs is what keeps the PassRaw scan and the router's parser in agreement.
var sendFlagDefs = []cmdkit.FlagDef{
	{Long: "id", Description: "Target aria id"},
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
	{Long: "outfit", Short: "O", Description: "Outfit NAMES to dress the aria in (comma-separated)"},
	{Long: "set", Short: "S", Description: "Form keys: k=v or a JSON literal, comma-separated"},
	{Long: "delete", Short: "D", Description: "Form key paths to remove, comma-separated"},
	{Long: "cast", Short: "C", IsBool: true, Description: "new: cast the fresh aria into a role (named @form, or minted from -O/-S)"},
}

// Everything after the first bare `--` is the prompt and is never touched.
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

// forkPoint is where a fork lands; zero value is the head. `:19` is a turn,
// `:19.10` a node of that turn (a rendering coordinate, resolved by
// resolveForkPoint), `.326` an LT.
type forkPoint struct {
	turn uint64
	lt   uint64

	// node 0 and -1 are both real addresses, so hasNode carries presence.
	node    int
	hasNode bool
}

func (f forkPoint) isHead() bool { return f.turn == 0 && f.lt == 0 && !f.hasNode }

func (f forkPoint) String() string {
	switch {
	case f.turn > 0 && f.hasNode && f.node == inquiryNode:
		return fmt.Sprintf("turn %d's question", f.turn)
	case f.turn > 0 && f.hasNode:
		return fmt.Sprintf("turn %d node %d", f.turn, f.node)
	case f.turn > 0:
		return fmt.Sprintf("turn %d", f.turn)
	case f.lt > 0:
		return fmt.Sprintf("LT %d", f.lt)
	default:
		return "the head"
	}
}

func parseTarget(spec string) (trunk string, at forkPoint, err error) {
	if spec == "" {
		return "", forkPoint{}, nil
	}
	trunk = spec
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		trunk = spec[:i]
		at, err = parseTurnCoord(spec, spec[i+1:])
		if err != nil {
			return "", forkPoint{}, err
		}
	} else if i := strings.LastIndex(spec, "."); i >= 0 {
		n, perr := strconv.ParseUint(spec[i+1:], 10, 64)
		if perr != nil || n == 0 {
			return "", forkPoint{}, fmt.Errorf("bad .<lt> in %q (want [<trunk>].<n>, LTs start at 1)", spec)
		}
		trunk, at.lt = spec[:i], n
	}
	if trunk != "" {
		if verr := validateSendID(trunk); verr != nil {
			return "", forkPoint{}, verr
		}
	}
	return trunk, at, nil
}

func parseTurnCoord(spec, coord string) (forkPoint, error) {
	turnText, nodeText, hasNode := strings.Cut(coord, ".")
	turn, err := strconv.ParseUint(turnText, 10, 64)
	if err != nil || turn == 0 {
		return forkPoint{}, fmt.Errorf("bad :<turn> in %q (want [<trunk>]:<n>, turns start at 1)", spec)
	}
	if !hasNode {
		return forkPoint{turn: turn}, nil
	}
	// -1 is the turn's opening question; nothing below -1 addresses anything.
	node, err := strconv.Atoi(nodeText)
	if err != nil || node < inquiryNode {
		return forkPoint{}, fmt.Errorf("bad :<turn>.<node> in %q (want [<trunk>]:<n>.<k>, nodes start at 0; -1 is the question)", spec)
	}
	return forkPoint{turn: turn, node: node, hasNode: true}, nil
}

func validateSendID(id string) error {
	if err := rpc.ValidateAriaRef(id); err != nil {
		return fmt.Errorf("--id %q: %w", id, err)
	}
	return nil
}

// runSend: --exec runs the bash wrapper (--raw ignored); --raw streams
// unformatted; otherwise interactive.
func runSend(loaded *config.Loaded, rawArgs []string) {
	runSendAs(loaded, "send", rawArgs)
}

// The bare `figaro [flags] -- <prompt>` form dispatches here too, labelled with verb.
func runSendAs(loaded *config.Loaded, verb string, rawArgs []string) {
	opts, rest, err := extractSendFlags(rawArgs)
	if err != nil {
		dieUsage("%s: %s", verb, err)
	}
	prompt := extractPrompt(rest)
	if prompt == "" {
		// A boundary with nothing after it opens the editor.
		if hasDashBoundary(rest) {
			text, cerr := composePrompt("Write a prompt. Markdown is fine.")
			if cerr != nil {
				if _, cancelled := cerr.(composeCancelled); cancelled {
					return
				}
				die("%s: %s", verb, cerr)
			}
			prompt = text
		} else {
			flags := "[--id <id>] [-r|--raw] [-v|--verbatim] [-x|--exec] [-n] [-y] -- <prompt>"
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
	trunkID, at, perr := parseTarget(spec)
	if perr != nil {
		dieUsage("%s: %s", verb, perr)
	}

	if err := validateSendOpts(opts, !at.isHead()); err != nil {
		dieUsage("%s: %s", verb, err)
	}
	if opts.json {
		opts.forget = true
	}

	set := pagerSettings(loaded)
	set.verbose, set.listen, set.record = opts.verbose, opts.listen, opts.record

	// `send <trunk>:<turn>` forks at that turn, then sends to whichever trunk
	// is attended afterwards.
	if !at.isHead() {
		runSendForkAt(loaded, trunkID, at, opts.stay, opts.json, prompt, set, opts.outfit)
		return
	}
	if opts.id == "" {
		opts.id = trunkID
	} else {
		opts.id = trunkID
	}

	switch {
	case opts.forget:
		runSendForget(loaded, opts, prompt)
	case opts.verbatim:
		runSendVerbatim(loaded, opts, prompt)
	case opts.exec:
		runSendExec(loaded, opts, prompt)
	case opts.raw:
		runSendRaw(loaded, opts.id, opts.outfit, prompt)
	default:
		if opts.id == "" {
			runPrompt(loaded, opts.outfit, prompt, set)
			return
		}
		promptAria(loaded, opts.id, prompt, set, opts.outfit)
	}
}

// validateSendOpts is pure: a dispatcher past its guard reaches
// mustConnectAngelus, which in a test binary is a fork bomb.
func validateSendOpts(opts sendOpts, hasTurn bool) error {
	if (opts.dryRun || opts.skipYes) && !opts.exec {
		return fmt.Errorf("-n / -y only meaningful with --exec")
	}
	if opts.forget && (opts.exec || opts.verbatim) {
		return fmt.Errorf("--forget contradicts --exec/--verbatim")
	}
	if hasTurn && (opts.exec || opts.verbatim) {
		return fmt.Errorf("<trunk>:<turn> is not compatible with --exec/--verbatim")
	}
	if opts.json {
		if bad := jsonIncompatible(opts); bad != "" {
			return fmt.Errorf("--json contradicts %s (--json submits and exits; there is no stream to shape)", bad)
		}
	}
	return nil
}

func validateNewOpts(opts sendOpts) error {
	switch {
	case opts.id != "" || opts.target != "":
		return fmt.Errorf("new always creates an aria; drop the target (or use `send --id`)")
	case opts.exec || opts.dryRun || opts.skipYes:
		return fmt.Errorf("--exec is `send -x`")
	}
	return validateSendOpts(opts, false)
}

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

func runSendRaw(loaded *config.Loaded, ariaID string, d dressing, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, figaroEP, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, ariaID, true, d)
	if err != nil {
		die("%s", err)
	}

	exitCode := plainPrompt(ctx, figaroEP, prompt, os.Stdout, d)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runSendVerbatim dumps the wire frames, one JSON object per line.
func runSendVerbatim(loaded *config.Loaded, opts sendOpts, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	_, figaroEP, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, opts.id, true, opts.outfit)
	if err != nil {
		die("%s", err)
	}

	if exitCode := verbatimPrompt(ctx, figaroEP, prompt, os.Stdout, opts.outfit); exitCode != 0 {
		os.Exit(exitCode)
	}
}

func runSendExec(loaded *config.Loaded, opts sendOpts, instruction string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	var figaroEP sdk.Addr
	if opts.id == "" {
		createResp, err := createWithFirstRun(ctx, loaded, opts.outfit, func() (*rpc.CreateResponse, error) {
			return acli.Create(ctx, opts.outfit.names, opts.outfit.patch)
		})
		if err != nil {
			dieWithClosure(err, "create figaro: %s", err)
		}
		figaroEP = ariaAddr(createResp.FigaroID)
		defer func() {
			killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer killCancel()
			_ = acli.Kill(killCtx, createResp.FigaroID, false)
		}()
	} else {
		_, ep, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, opts.id, true, opts.outfit)
		if err != nil {
			die("%s", err)
		}
		figaroEP = ep
	}

	prompt := "You will write a bash script. Output ONLY raw bash, " +
		"no markdown fences, no prose, no commentary, no explanations. " +
		"The script will be executed verbatim via `bash -c`. " +
		"Instruction: " + instruction

	var buf bytes.Buffer
	exitCode := plainPrompt(ctx, figaroEP, prompt, &buf, opts.outfit)
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	script := stripBashFences(buf.String())
	if strings.TrimSpace(script) == "" {
		die("figaro send --exec: empty script from agent")
	}

	if opts.dryRun {
		fmt.Fprint(stdout, script)
		if !strings.HasSuffix(script, "\n") {
			fmt.Fprintln(stdout)
		}
		return
	}

	if !opts.skipYes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(stderrw, "--- figaro send --exec: about to execute ---")
		fmt.Fprintln(stderrw, script)
		fmt.Fprintln(stderrw, "--- press enter to run, ctrl-c to abort ---")
		_, _ = term.ReadLine("")
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

// runSendForget submits and exits; the daemon keeps the turn alive.
func runSendForget(loaded *config.Loaded, opts sendOpts, prompt string) {
	// 30s: this call may mint the aria first, and a cold daemon plus a
	// first-run outfit render does not fit in ten.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ariaID, figaroEP, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, opts.id, true, opts.outfit)
	if err != nil {
		die("%s", err)
	}

	fcli, derr := sdk.DialAria(figaroEP, func(string, json.RawMessage) {})
	if derr != nil {
		die("connect figaro: %s", derr)
	}
	defer fcli.Close()

	if _, _, qerr := fcli.Qua(ctx, prompt, buildPromptForm(opts.outfit)); qerr != nil {
		dieWithClosure(qerr, "prompt: %s", qerr)
	}
	if opts.json {
		enc := json.NewEncoder(stdout)
		_ = enc.Encode(struct {
			AriaID string `json:"aria_id"`
			Mode   string `json:"mode"`
		}{AriaID: ariaID, Mode: "forget"})
		return
	}
	fmt.Fprintf(stderrw, "forgot %s: use `figaro listen %s` to follow\n", ariaID, ariaID)
}

// promptFlagDefs exists for completion only: send, new and fork are PassRaw and
// parse their own argv.
var promptFlagDefs = []cmdkit.FlagDef{
	{Long: "id", Description: "Target a specific existing aria"},
	{Long: "outfit", Short: "O", Description: "Dress the aria in an outfit"},
	{Long: "set", Short: "S", Description: "Patch the board in the same call: k=v"},
	{Long: "delete", Short: "D", Description: "Remove board keys in the same call"},
	{Long: "raw", Short: "r", IsBool: true, Description: "Stream verbatim to stdout: no ANSI, no markdown"},
	{Long: "verbatim", Short: "v", IsBool: true, Description: "Dump the raw wire frames as JSON"},
	{Long: "verbose", Short: "o", IsBool: true, Description: "Expand full tool inputs"},
	{Long: "listen", Short: "l", IsBool: true, Description: "Open the transcript pager at startup"},
	{Long: "exec", Short: "x", IsBool: true, Description: "Treat the prompt as a bash instruction"},
	{Long: "dry-run", Short: "n", IsBool: true, Description: "--exec only: print the script without running it"},
	{Long: "yes", Short: "y", IsBool: true, Description: "--exec only: skip the confirmation prompt"},
	{Long: "forget", Short: "f", IsBool: true, Description: "Submit the prompt and exit immediately"},
	{Long: "json", Short: "j", IsBool: true, Description: "Emit a single JSON line on stdout"},
	{Long: "stay", IsBool: true, Description: "Do not move this shell"},
	{Long: "record", Description: "Record the wire to a tape file"},
}
