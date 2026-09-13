package cli

// The verb bodies the shell and the pager share: listen, attend, fork, send.
//
// A body takes (ctx, env, plan), speaks to the daemon and RETURNS. It never
// exits, never writes to stdout, and never presents the reply: the shell
// wrapper keeps die() and the stream, the pager renders the result into its
// status row. Progress goes to the caller through the request (progress.go).

import (
	"context"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/mark"

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
	// Home says the shell was UNBOUND rather than moved: `attend null`. There
	// is no aria to show, so a door that shows one shows what it showed.
	Home bool
}

// attendVerb binds env.shellPID to the aria spec names, at the turn it names
// if any, and resolves its endpoint. `:<turn>` alone re-pins the bound aria.
func attendVerb(ctx context.Context, env verbEnv, spec string) (attendOutcome, error) {
	if spec == "" {
		return attendOutcome{}, errors.New("which aria? (attend <id|@role>[:<turn>])")
	}
	// HOME IS PART OF THE VERB, not of the shell's wrapper around it. `null`
	// (and the legacy `~`) drops this shell's binding, which the pager could
	// not do at all while the case lived one layer up.
	if spec == "null" || spec == "~" {
		return unattendVerb(ctx, env)
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

// unattendVerb is `attend null`: the shell goes home, and new conversations
// default to the live outfit again.
func unattendVerb(ctx context.Context, env verbEnv) (attendOutcome, error) {
	if env.shellPID <= 0 {
		return attendOutcome{}, errors.New("this pager has no shell to unbind")
	}
	bound := ""
	if r, err := resolveBinding(ctx, env.acli, env.shellPID); err == nil && r.Found {
		bound = r.FigaroID
	}
	if err := unbindBinding(ctx, env.acli, env.shellPID); err != nil {
		return attendOutcome{}, err
	}
	if bound == "" {
		return attendOutcome{Home: true, Note: "no aria bound to this shell"}, nil
	}
	return attendOutcome{Home: true, Note: "home: unattended " + bound + "; new conversations use the default outfit"}, nil
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

// bindIntent is what the CALLER wants done with the shell's binding when the
// branch is born. It is explicit because the two doors want different things
// and neither can be inferred from argv: a shell fans out (it moves only when
// it forked its OWN aria), while the pager's fork means attend-and-show, from
// whatever the shell happens to be bound to. --stay means neither, on both.
type bindIntent int

const (
	bindFanOut bindIntent = iota // move only when the target IS the shell's aria
	bindBranch                   // attend the branch, whatever was bound
	bindStay                     // touch nothing
)

// forkVerb runs a planned fork: resolve the target (the bound aria when the
// plan names none), fork at the coordinate (with the quote pre-flight
// against the prompt), and move the shell's binding to the branch when the
// plan forked the shell's own aria and did not ask to stay, exactly as
// `figaro fork` does. Submitting the prompt is the door's.
func forkVerb(ctx context.Context, env verbEnv, plan forkPlan, intent bindIntent) (forkOutcome, error) {
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
	move, note := bindDecision(intent, plan.opts.stay, bound, why, target)
	out.BindNote = note
	if move {
		// Registry.Bind rebinds in place.
		if berr := bindBinding(ctx, env.acli, env.shellPID, resp.Alternative, 0); berr != nil {
			out.BindNote = "could not attend " + resp.Alternative + ": " + berr.Error()
		} else {
			out.Rebound = true
		}
	}
	return out, nil
}

// bindDecision is whether the shell moves to the branch, and the one sentence
// that says why not. A fan-out never steals the shell: it moves only when the
// aria forked was the shell's own. bindBranch always moves, which is what the
// pager's fork means; bindStay and --stay never do.
func bindDecision(intent bindIntent, stay bool, bound, why, target string) (bool, string) {
	switch {
	case stay || intent == bindStay:
		return false, ""
	case intent == bindFanOut && bound == "":
		return false, "not attended: " + why
	case intent == bindFanOut && target != bound:
		return false, "not attended: " + target + " is not this shell's aria"
	}
	return true, ""
}

// ---------------------------------------------------------------------------
// The surface a prompt verb was typed at, and what it can therefore honour.

// surface is the CALLER's capability, carried into the plan so that send and
// fork validate the same flags the same way. A shell can spend stdout, run a
// script and open a tape; the pager owns the pane and can do none of those. A
// flag the surface cannot honour is refused BY NAME: parsed and silently
// dropped is a lie, and it is what `:fork -x` used to be.
type surface struct {
	name    string
	streams bool // -r, -v, -j: stdout is the caller's to spend
	spawns  bool // -x and its -n / -y: there is a shell to run a script in
	records bool // --record: a tape of this session's wire
	opens   bool // -l: "open the transcript" means something
	forgets bool // -f: there is a stream to decline
}

var (
	shellSurface = surface{name: "the shell", streams: true, spawns: true, records: true, opens: true, forgets: true}
	pagerSurface = surface{name: "the pager"}
)

// refuse names the first flag this surface cannot honour.
func (s surface) refuse(o sendOpts) error {
	for _, c := range []struct {
		on   bool
		flag string
		why  string
	}{
		{!s.spawns && o.exec, "-x", "runs a script in a shell; " + s.name + " has none"},
		{!s.spawns && o.dryRun, "-n", "belongs to -x"},
		{!s.spawns && o.skipYes, "-y", "belongs to -x"},
		{!s.streams && o.raw, "-r", "is a stream for a pipe; the transcript is the stream here"},
		{!s.streams && o.verbatim, "-v", "dumps wire frames to stdout, which " + s.name + " owns"},
		{!s.streams && o.json, "-j", "prints an object on stdout, which " + s.name + " owns"},
		{!s.opens && o.listen, "-l", "opens the transcript, which is already open"},
		{!s.records && o.record != "", "--record", "records a session's wire, not a message"},
	} {
		if c.on {
			return fmt.Errorf("%s %s", c.flag, c.why)
		}
	}
	return nil
}

// prepareSend and prepareFork are the two prompt verbs' preparation: ONE
// parser, then the capability check the surface declares. Every door runs
// these; nothing else may parse a prompt verb's argv.
func prepareSend(args []string, s surface) (sendPlan, error) {
	plan, err := planSend(args)
	if err != nil {
		return sendPlan{}, err
	}
	return plan, s.refuse(plan.opts)
}

func prepareFork(args []string, s surface) (forkPlan, error) {
	plan, err := planFork(args)
	if err != nil {
		return forkPlan{}, err
	}
	if err := s.refuse(plan.opts); err != nil {
		return forkPlan{}, err
	}
	if !s.forgets && plan.opts.forget {
		return forkPlan{}, errors.New("-f is a shell notion (the transcript is the stream); --stay mints and prompts without following")
	}
	return plan, nil
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
		qua := mark.Span("submit.qua", "to", impliedID, "len", len(plan.prompt))
		_, active, err := implied.Qua(ctx, plan.prompt, buildPromptForm(plan.opts.outfit))
		qua("active", active, "err", err != nil)
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
	qua := mark.Span("submit.qua", "to", id, "len", len(plan.prompt))
	_, active, err := fcli.Qua(ctx, plan.prompt, buildPromptForm(plan.opts.outfit))
	qua("active", active, "err", err != nil)
	if err != nil {
		return id, nil, err
	}
	return id, &rpc.QuaResponse{OK: true, Active: active}, nil
}
