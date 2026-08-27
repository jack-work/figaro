package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"os/signal"
	"strings"

	"github.com/jack-work/figaro/internal/config"
)

// `figaro fork [flags] [<id>[:<turn>]] [-- <prompt>]`.
//
// Without a prompt this is the imperative branch it always was: freeze the
// target, mint a continuation and an empty alternative (see runFork).
//
// With a prompt it gains `new`'s semantics: fork, then immediately send -
// and it is the same parser as `send`, so the send flags mean here exactly
// what they mean there. Three rules hold the composed form together:
//
//   - The prompt always lands on the ALTERNATIVE (the fresh empty branch).
//     That is the whole point of forking with something to say; the
//     continuation carries the original line, untouched.
//   - `--stay` governs the SHELL, not the prompt. Without it, forking your
//     OWN bound aria moves you to the branch you just prompted (you froze
//     the aria you were on; the branch is where the work now is). Forking
//     any other aria never moves you: that is a fan-out, not a rescope.
//     This is fork's own longstanding rule, pointed at the alternative
//     instead of the continuation because that is where the prompt went.
//
// Note the deliberate difference from `send <id>:<turn> --stay -- …`, where
// --stay parks the alternative and sends to the ORIGINAL trunk. `send`'s
// subject is the message ("where does this land?"); `fork`'s subject is the
// branch ("what did I just make?"). Under fork, the branch is always what
// gets prompted.

// forkPlan is the parsed shape of a `fork` invocation: which aria, which
// flags, and what (if anything) to say to the new branch. Parsing and
// validation are pure so the grammar can be tested without a daemon.
type forkPlan struct {
	spec    string   // target: "", <id>, <id>:<turn>
	opts    sendOpts // the shared send/fork flag set
	prompt  string   // "" when this is the imperative, prompt-less fork
	compose bool     // `fork --` with an empty body: open the composer
}

// hasPrompt reports whether this invocation will end up sending. The
// composer form counts: the prompt is merely not typed yet.
func (p forkPlan) hasPrompt() bool { return p.prompt != "" || p.compose }

// planFork parses argv into a forkPlan, or explains why it cannot. It never
// touches the daemon; every rejection here is a grammar rejection.
func planFork(args []string) (forkPlan, error) {
	opts, rest, err := extractForkFlags(args)
	if err != nil {
		return forkPlan{}, err
	}
	plan := forkPlan{opts: opts, spec: opts.id}
	if plan.spec == "" {
		plan.spec = opts.target
	}
	plan.prompt = extractPrompt(rest)
	plan.compose = plan.prompt == "" && hasDashBoundary(rest)

	if _, _, perr := parseTarget(plan.spec); perr != nil {
		return forkPlan{}, perr
	}
	if !plan.hasPrompt() {
		if bad := forkPromptOnlyFlags(opts); bad != "" {
			return forkPlan{}, fmt.Errorf("%s only applies with a prompt (fork %s-- <prompt>)", bad, forkTargetHint(plan.spec))
		}
		return plan, nil
	}
	// The same contradictions `send` enforces, from the same function, a
	// fork's prompt is a send, and two copies of a rule is how the fork-send
	// path came to keep the print-JSON-then-stream contract after send lost
	// it. --json's rules ride along: -j with --raw/--verbatim/--exec/
	// --listen/--verbose cannot be honoured here either.
	if err := validateSendOpts(opts, false); err != nil {
		return forkPlan{}, err
	}
	return plan, nil
}

// runForkCmd is the `fork` verb's entry point: plan, then dispatch to the
// prompt-less imperative fork or the fork-and-send form.
func runForkCmd(loaded *config.Loaded, rawArgs []string) {
	plan, err := planFork(rawArgs)
	if err != nil {
		die("fork: %s", err)
	}
	if plan.compose {
		// A boundary with nothing after it is an invitation, as in `send`:
		// open the composer and fork with whatever gets written.
		text, cerr := composePrompt("Write a prompt for the new branch. Markdown is fine.")
		if cerr != nil {
			if _, cancelled := cerr.(composeCancelled); cancelled {
				return // nothing written; not an error
			}
			die("fork: %s", cerr)
		}
		plan.prompt = strings.TrimSpace(text)
		if plan.prompt == "" {
			return
		}
	}
	if plan.prompt == "" {
		runFork(loaded, plan.spec, plan.opts)
		return
	}
	runForkPrompt(loaded, plan.spec, plan.opts, plan.prompt)
}

// forkPromptOnlyFlags names the first flag that only means something once a
// prompt follows `--`. A prompt-less `fork -r` would silently do nothing
// with -r; say so instead.
func forkPromptOnlyFlags(opts sendOpts) string {
	for _, f := range []struct {
		set  bool
		name string
	}{
		{opts.raw, "-r/--raw"},
		{opts.verbatim, "-v/--verbatim"},
		{opts.verbose, "-o/--verbose"},
		{opts.listen, "-l/--listen"},
		{opts.exec, "-x/--exec"},
		{opts.forget, "-f/--forget"},
		{opts.dryRun, "-n/--dry-run"},
		{opts.skipYes, "-y/--yes"},
	} {
		if f.set {
			return f.name
		}
	}
	return ""
}

// forkTargetHint renders the target back into the usage nudge, so the
// suggested command is the one the user actually typed plus a prompt.
func forkTargetHint(spec string) string {
	if spec == "" {
		return ""
	}
	return spec + " "
}

// runForkPrompt is `fork … -- <prompt>`: branch, announce, then send the
// prompt to the alternative through the same dispatch `send` uses, so -r,
// -v, -o, -l, -x/-n/-y and -f behave identically on either verb.
func runForkPrompt(loaded *config.Loaded, spec string, opts sendOpts, prompt string) {
	target, at, perr := parseTarget(spec)
	if perr != nil {
		die("fork: %s", perr)
	}

	branch := ""
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx := context.Background()
		ppid := shellPID

		bound := ""
		if r, err := resolveBinding(ctx, acli, ppid); err == nil && r.Found {
			bound = r.FigaroID
		}
		if target == "" {
			if bound == "" {
				die("fork: no aria bound to this shell (try: <id> or <id>:<turn>)")
			}
			target = bound
		}

		// The coordinate goes on the wire in the form the user named it;
		// the server owns any translation.

		resp, err := waitForFork(ctx, acli, target, at, opts.outfit)
		if err != nil {
			die("fork: %s", err)
		}
		branch = resp.Alternative

		// Move to the branch we just prompted: but only when we forked our
		// OWN bound aria, and only without --stay. Forking someone else's
		// aria is a fan-out; it never steals this shell. Registry.Bind
		// rebinds in place, so no Unbind is needed first.
		rescoped := false
		if target == bound && !opts.stay {
			if berr := bindBinding(ctx, acli, ppid, resp.Alternative, 0); berr != nil {
				fmt.Fprintf(stderrw, "warning: could not attend %s: %s\n", resp.Alternative, berr)
			} else {
				rescoped = true
			}
		}

		if opts.json {
			// aria_id is the aria the prompt goes to, always the branch.
			enc := json.NewEncoder(stdout)
			_ = enc.Encode(struct {
				AriaID       string `json:"aria_id"`
				Parent       string `json:"parent"`
				Continuation string `json:"continuation"`
				Alternative  string `json:"alternative"`
				Turn         uint64 `json:"turn,omitempty"`
				Node         *int   `json:"node,omitempty"`
				Rescoped     bool   `json:"rescoped"`
				OwnerNote    string `json:"owner_note,omitempty"`
				Mode         string `json:"mode"`
			}{
				AriaID:       resp.Alternative,
				Parent:       resp.Parent,
				Continuation: resp.Continuation,
				Alternative:  resp.Alternative,
				Turn:         at.turn,
				Node:         at.nodeJSON(),
				Rescoped:     rescoped,
				OwnerNote:    resp.OwnerNote,
				Mode:         "fork-send",
			})
			return nil
		}

		if resp.OwnerNote != "" {
			fmt.Fprintf(stderrw, "%s\n", resp.OwnerNote)
		}
		altNote := "(prompting)"
		if rescoped {
			altNote = "(prompting; this shell)"
		}
		fmt.Fprintf(stderrw,
			"forked %s at %s (now a frozen fork point)\n  continuation %s  (attend to continue)\n  alternative  %s  %s\n",
			resp.Parent, at, resp.Continuation, resp.Alternative, altNote)
		return nil
	})

	if branch == "" {
		die("fork: no alternative branch to prompt")
	}
	if opts.json {
		// --json submits and exits: the object printed above IS the output.
		// This path used to print it and then stream the rendered turn onto
		// the same stdout: the second copy of the defect `send` shed.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		submitAndExit(ctx, loaded, branch, prompt)
		return
	}
	promptForkedAria(loaded, branch, opts, prompt)
}

// forkPromptRoute names the send dispatch a fork's prompt takes. Pure, so
// the flag-to-branch mapping is testable; promptForkedAria just obeys it.
func forkPromptRoute(opts sendOpts) string {
	switch {
	case opts.forget:
		return "forget"
	case opts.verbatim:
		return "verbatim"
	case opts.exec:
		return "exec"
	case opts.raw:
		return "raw"
	default:
		return "rich"
	}
}

// promptForkedAria sends the prompt to the freshly minted branch, reusing
// `send`'s dispatch table verbatim. It is never reached under --json: that
// contract submits and exits before this point.
func promptForkedAria(loaded *config.Loaded, ariaID string, opts sendOpts, prompt string) {
	opts.id = ariaID
	opts.target = ""
	set := renderSettings{verbose: opts.verbose, listen: opts.listen}
	switch forkPromptRoute(opts) {
	case "forget":
		runSendForget(loaded, opts, prompt)
	case "verbatim":
		runSendVerbatim(loaded, opts, prompt)
	case "exec":
		runSendExec(loaded, opts, prompt)
	case "raw":
		runSendRaw(loaded, ariaID, dressing{}, prompt)
	default:
		promptAria(loaded, ariaID, prompt, set)
	}
}
