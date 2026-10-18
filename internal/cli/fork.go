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

// forkPlan is `figaro fork [flags] [<id>[:<turn>]] [-- <prompt>]`, parsed and
// validated purely. The prompt always lands on the alternative; --stay governs
// the shell, not the prompt.
type forkPlan struct {
	spec    string
	opts    sendOpts
	prompt  string
	compose bool
}

// The composer form counts: the prompt is merely not typed yet.
func (p forkPlan) hasPrompt() bool { return p.prompt != "" || p.compose }

// planFork never touches the daemon.
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
	// The same contradictions `send` enforces, from the same function.
	if err := validateSendOpts(opts, false); err != nil {
		return forkPlan{}, err
	}
	return plan, nil
}

func runForkCmd(loaded *config.Loaded, rawArgs []string) {
	plan, err := prepareFork(rawArgs, shellSurface)
	if err != nil {
		die("fork: %s", err)
	}
	if plan.compose {
		text, cerr := composePrompt("Write a prompt for the new branch. Markdown is fine.")
		if cerr != nil {
			if _, cancelled := cerr.(composeCancelled); cancelled {
				return
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
	runForkPrompt(loaded, plan)
}

// forkPromptOnlyFlags names the first flag that only means something once a
// prompt follows `--`.
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

func forkTargetHint(spec string) string {
	if spec == "" {
		return ""
	}
	return spec + " "
}

// runForkPrompt presents the reply through the same dispatch `send` uses; the
// fork itself is forkVerb's.
func runForkPrompt(loaded *config.Loaded, plan forkPlan) {
	opts, prompt := plan.opts, plan.prompt
	var out forkOutcome
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx := context.Background()
		env := verbEnv{loaded: loaded, acli: acli, shellPID: shellPID}
		var err error
		out, err = forkVerb(ctx, env, plan, bindFanOut)
		if err != nil {
			die("fork: %s", err)
		}
		if out.BindNote != "" && !opts.stay {
			fmt.Fprintf(stderrw, "warning: %s\n", out.BindNote)
		}
		if opts.json {
			// aria_id is always the branch.
			enc := json.NewEncoder(stdout)
			_ = enc.Encode(struct {
				AriaID       string `json:"aria_id"`
				Handle       string `json:"handle"`
				Parent       string `json:"parent"`
				Continuation string `json:"continuation"`
				Alternative  string `json:"alternative"`
				Turn         uint64 `json:"turn,omitempty"`
				Node         *int   `json:"node,omitempty"`
				Rescoped     bool   `json:"rescoped"`
				OwnerNote    string `json:"owner_note,omitempty"`
				Mode         string `json:"mode"`
			}{
				AriaID:       out.Alternative,
				Handle:       ariaName(out.Alternative),
				Parent:       out.Parent,
				Continuation: out.Continuation,
				Alternative:  out.Alternative,
				Turn:         out.At.turn,
				Node:         out.At.nodeJSON(),
				Rescoped:     out.Rebound,
				OwnerNote:    out.OwnerNote,
				Mode:         "fork-send",
			})
			return nil
		}
		if out.OwnerNote != "" {
			fmt.Fprintf(stderrw, "%s\n", out.OwnerNote)
		}
		altNote := "(prompting)"
		if out.Rebound {
			altNote = "(prompting; this shell)"
		}
		// A head fork seals nothing; only an interior fork seals the node it
		// cuts at. Both branches stay live.
		atNote := "(fork point sealed; both branches live)"
		if out.At.isHead() {
			atNote = "(new branch; both live)"
		}
		fmt.Fprintf(stderrw,
			"forked %s at %s %s\n  continuation %s  (attend to continue)\n  alternative  %s  %s\n",
			out.Parent, out.At, atNote, out.Continuation, out.Alternative, altNote)
		return nil
	})

	branch := out.Alternative
	if branch == "" {
		die("fork: no alternative branch to prompt")
	}
	if opts.json {
		// --json submits and exits: the object printed above is the output.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		submitAndExit(ctx, loaded, branch, prompt, opts.outfit)
		return
	}
	promptForkedAria(loaded, branch, opts, prompt)
}

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

// promptForkedAria is never reached under --json.
func promptForkedAria(loaded *config.Loaded, ariaID string, opts sendOpts, prompt string) {
	opts.id = ariaID
	opts.target = ""
	set := pagerSettings(loaded)
	set.verbose, set.listen = opts.verbose, opts.listen
	switch forkPromptRoute(opts) {
	case "forget":
		runSendForget(loaded, opts, prompt)
	case "verbatim":
		runSendVerbatim(loaded, opts, prompt)
	case "exec":
		runSendExec(loaded, opts, prompt)
	case "raw":
		runSendRaw(loaded, ariaID, opts.outfit, prompt)
	default:
		promptAria(loaded, ariaID, prompt, set, opts.outfit)
	}
}
