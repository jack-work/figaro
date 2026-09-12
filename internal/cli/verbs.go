package cli

// VERBS THAT RETURN, INSTEAD OF EXITING.
//
// A CLI verb used to be fused to its process wrapper: it got its connection
// from WithAngelus, reported failure with die() (os.Exit), and wrote to
// stdout. The transcript's ':' box cannot live with any of those, so it grew
// hand-written twins, and a twin drifts. plans/transcript-command-mode.md
// section 4 names the cure: SEPARATE THE VERB'S BODY FROM ITS WRAPPER. The
// body takes (ctx, env, args), speaks to the daemon, and returns a result;
// the shell wrapper keeps die() and stdout and streams; the box renders the
// result into the status row and retargets. One implementation, two doors.
//
// What a body does NOT do is present the reply: at the shell that is a
// stream on the terminal, in the box it is the transcript itself. For fork
// the body stops one step earlier, at "the branch exists and the shell is
// bound to it": the shell submits the prompt through send's own dispatch,
// whose -r/-v/-x modes are a submit and a stream in one, and the box
// submits it and retargets. Parsing, resolution, the fork and the rebind are
// the parts that drifted between twins, and they are shared.
//
// The four verbs the box has are here: listen, attend, fork, send. Each
// deletes the twin it replaced.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

// verbEnv is what a body needs from its door.
type verbEnv struct {
	loaded   *config.Loaded
	acli     *sdk.Angelus
	shellPID int // the shell whose binding attend and fork move
}

// boundAria is the aria the shell attends, or "" with the reason it has none.
func (env verbEnv) boundAria(ctx context.Context) (string, string) {
	if env.shellPID <= 0 {
		return "", "this pager has no shell to bind"
	}
	r, err := resolveBinding(ctx, env.acli, env.shellPID)
	if err != nil {
		return "", "could not read this shell's binding: " + err.Error()
	}
	if !r.Found {
		return "", "this shell attends no aria"
	}
	return r.FigaroID, ""
}

// ---------------------------------------------------------------------------
// listen / attend: resolve a spec to an aria and, for attend, bind to it.

// listenVerb resolves spec (an id, an @role, a coordinate) to the aria and
// its endpoint. Attendance is untouched: you have to listen to a figaro.
func listenVerb(ctx context.Context, env verbEnv, spec string) (string, transport.Endpoint, error) {
	if spec == "" {
		return "", transport.Endpoint{}, errors.New("which aria? (listen <id|@role>)")
	}
	return resolveFigaroTargetEndpoint(ctx, env.loaded, env.acli, spec, false, dressing{})
}

// attendOutcome is what attend did.
type attendOutcome struct {
	ID   string
	EP   transport.Endpoint
	At   forkPoint // the pending fork point, when the spec named one
	Note string    // a node coordinate that landed earlier than named
}

// attendVerb binds env.shellPID to the aria spec names, at the turn it names
// if any, and resolves its endpoint. `:<turn>` alone re-pins the bound aria.
func attendVerb(ctx context.Context, env verbEnv, spec string) (attendOutcome, error) {
	if spec == "" {
		return attendOutcome{}, errors.New("which aria? (attend <id|@role>[:<turn>])")
	}
	trunk, at, err := parseTarget(spec)
	if err != nil {
		return attendOutcome{}, err
	}
	if trunk == "" {
		bound, why := env.boundAria(ctx)
		if bound == "" {
			return attendOutcome{}, fmt.Errorf(":<turn> needs an already-bound aria (%s)", why)
		}
		trunk = bound
	}
	// A binding anchor is an LT, so a named TURN is resolved here; a named
	// LT is already the thing bindBinding wants. A named NODE is resolved
	// first, into whichever of the two it turns out to be.
	anchor, note, err := resolveForkPoint(ctx, env.acli, trunk, at)
	if err != nil {
		return attendOutcome{}, err
	}
	atMainLT := anchor.lt
	if anchor.turn > 0 {
		lt, err := resolveTurn(ctx, env.acli, trunk, anchor.turn)
		if err != nil {
			return attendOutcome{}, err
		}
		atMainLT = lt
	}
	if env.shellPID <= 0 {
		return attendOutcome{}, errors.New("this pager has no shell to bind")
	}
	if err := bindBinding(ctx, env.acli, env.shellPID, trunk, atMainLT); err != nil {
		return attendOutcome{}, attendRefusal(ctx, env.acli, trunk, err)
	}
	id, ep, err := resolveFigaroTargetEndpoint(ctx, env.loaded, env.acli, trunk, false, dressing{})
	if err != nil {
		return attendOutcome{}, err
	}
	return attendOutcome{ID: id, EP: ep, At: at, Note: note}, nil
}

// attendRefusal explains a binding the daemon would not make: a cauterized
// anchor (null, an outfit) is not a conversation.
func attendRefusal(ctx context.Context, acli *sdk.Angelus, trunk string, err error) error {
	if r, e := acli.ListGlobal(ctx); e == nil {
		for _, f := range r.Figaros {
			if f.ID == trunk && (f.Kind == "null" || f.Kind == "outfit") {
				return fmt.Errorf("%s is a %s: a closed anchor, not a conversation; it can't be attended.\n"+
					"  figaro attend null  go home (unbind; new conversations use the live outfit)\n"+
					"  figaro ls -h        lists top-level conversations (use -a or -n N to show all or N most recent in scope)\n"+
					"  figaro ls -g        show the full hierarchy (null + outfits + conversations)", trunk, f.Kind)
			}
		}
	}
	return err
}

// ---------------------------------------------------------------------------
// fork: mint the branch, move the shell to it, hand the prompt over.

// forkOutcome is what fork did, for the door to present.
type forkOutcome struct {
	Parent, Continuation, Alternative string
	At                                forkPoint
	Prompt                            string
	// Rebound says the shell now attends the alternative; BindNote says why
	// it does not, in one sentence, when it does not and was meant to.
	Rebound   bool
	BindNote  string
	OwnerNote string
}

// forkVerb runs a planned fork: resolve the target (the bound aria when the
// plan names none), fork at the coordinate (with the quote pre-flight
// against the prompt), and move the shell's binding to the branch when the
// plan forked the shell's own aria and did not ask to stay, exactly as
// `figaro fork` does. Submitting the prompt is the door's.
func forkVerb(ctx context.Context, env verbEnv, plan forkPlan) (forkOutcome, error) {
	target, at, err := parseTarget(plan.spec)
	if err != nil {
		return forkOutcome{}, err
	}
	bound, why := env.boundAria(ctx)
	if target == "" {
		if bound == "" {
			return forkOutcome{}, fmt.Errorf("no aria bound to this shell (%s); name one: fork <id>", why)
		}
		target = bound
	}
	resp, err := waitForFork(ctx, env.acli, target, at, plan.opts.outfit, plan.prompt)
	if err != nil {
		return forkOutcome{}, err
	}
	out := forkOutcome{
		Parent: resp.Parent, Continuation: resp.Continuation, Alternative: resp.Alternative,
		At: at, Prompt: plan.prompt, OwnerNote: resp.OwnerNote,
	}
	// Move to the branch we just made: but only when we forked our OWN bound
	// aria, and only without --stay. Forking someone else's aria is a
	// fan-out; it never steals this shell. Registry.Bind rebinds in place.
	switch {
	case plan.opts.stay:
	case bound == "":
		out.BindNote = "not attended: " + why
	case target != bound:
		out.BindNote = "not attended: " + target + " is not this shell's aria"
	default:
		if berr := bindBinding(ctx, env.acli, env.shellPID, resp.Alternative, 0); berr != nil {
			out.BindNote = "could not attend " + resp.Alternative + ": " + berr.Error()
		} else {
			out.Rebound = true
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// send: the prompt to an aria.

// sendPlan is the parsed shape of a send: the CLI's own flags, the target
// spec, and the prompt.
type sendPlan struct {
	opts   sendOpts
	spec   string // "" means the implied aria (the binding, or the subject)
	prompt string
}

// planSend parses argv with the shell's parser and nothing else.
func planSend(args []string) (sendPlan, error) {
	opts, rest, err := extractSendFlags(args)
	if err != nil {
		return sendPlan{}, err
	}
	plan := sendPlan{opts: opts, spec: opts.target, prompt: extractPrompt(rest)}
	if plan.spec == "" {
		plan.spec = opts.id
	}
	return plan, nil
}

// sendVerb submits the prompt to the aria the plan names and reports where
// it went and whether it joined a running turn. With no spec it goes to
// `implied`, which is the door's idea of "this aria": the binding at a
// shell, the subject in the pager.
func sendVerb(ctx context.Context, env verbEnv, plan sendPlan, implied *sdk.Aria, impliedID string) (string, *rpc.QuaResponse, error) {
	if plan.prompt == "" {
		return "", nil, errors.New("the prompt must follow `--`")
	}
	if plan.spec == "" && implied != nil {
		_, active, err := implied.Qua(ctx, plan.prompt, buildPromptForm())
		if err != nil {
			return impliedID, nil, err
		}
		return impliedID, &rpc.QuaResponse{OK: true, Active: active}, nil
	}
	id, ep, err := resolveFigaroTargetEndpoint(ctx, env.loaded, env.acli, plan.spec, false, dressing{})
	if err != nil {
		return "", nil, err
	}
	fcli, err := sdk.DialAria(ep, nil)
	if err != nil {
		return id, nil, fmt.Errorf("connect %s: %w", id, err)
	}
	defer fcli.Close()
	_, active, err := fcli.Qua(ctx, plan.prompt, buildPromptForm())
	if err != nil {
		return id, nil, err
	}
	return id, &rpc.QuaResponse{OK: true, Active: active}, nil
}
