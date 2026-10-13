package cli

// The verb bodies the shell and the pager share. A body speaks to the daemon
// and returns: it never exits, never writes stdout, never presents the reply.
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

type verbEnv struct {
	loaded   *config.Loaded
	acli     *sdk.Angelus
	shellPID int
}

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

// listenVerb resolves a spec to the aria and its endpoint; attendance is untouched.
func listenVerb(ctx context.Context, env verbEnv, spec string) (string, transport.Endpoint, error) {
	if spec == "" {
		return "", transport.Endpoint{}, errors.New("which aria? (listen <id|@role>)")
	}
	return resolveFigaroTargetEndpoint(ctx, env.loaded, env.acli, spec, false, dressing{})
}

type attendOutcome struct {
	ID   string
	EP   transport.Endpoint
	At   forkPoint
	Note string
	// Home says the shell was unbound (`attend null`), so a door that shows an
	// aria keeps what it showed.
	Home bool
}

// attendVerb binds env.shellPID to the aria spec names; `:<turn>` alone re-pins
// the bound aria.
func attendVerb(ctx context.Context, env verbEnv, spec string) (attendOutcome, error) {
	if spec == "" {
		return attendOutcome{}, errors.New("which aria? (attend <id|@role>[:<turn>])")
	}
	// `null` (and the legacy `~`) drops this shell's binding.
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
	// A binding anchor is an LT, so a named turn or node is resolved here.
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
// anchor is not a conversation.
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

type forkOutcome struct {
	Parent, Continuation, Alternative string
	At                                forkPoint
	Prompt                            string
	// BindNote says why the shell did not move, when it was meant to.
	Rebound   bool
	BindNote  string
	OwnerNote string
}

// bindIntent is explicit: a shell fans out (moves only when it forked its own
// aria), the pager's fork means attend-and-show, --stay means neither.
type bindIntent int

const (
	bindFanOut bindIntent = iota
	bindBranch
	bindStay
)

// forkVerb forks at the coordinate and moves the shell's binding when the plan
// forked the shell's own aria and did not ask to stay. Submitting the prompt is
// the door's.
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
		if berr := bindBinding(ctx, env.acli, env.shellPID, resp.Alternative, 0); berr != nil {
			out.BindNote = "could not attend " + resp.Alternative + ": " + berr.Error()
		} else {
			out.Rebound = true
		}
	}
	return out, nil
}

// A fan-out never steals the shell; bindBranch always moves; bindStay never does.
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

// surface is the caller's capability: a flag the surface cannot honour is
// refused by name, never silently dropped.
type surface struct {
	name    string
	streams bool
	spawns  bool
	records bool
	opens   bool
	forgets bool
}

var (
	shellSurface = surface{name: "the shell", streams: true, spawns: true, records: true, opens: true, forgets: true}
	pagerSurface = surface{name: "the pager"}
)

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

// prepareSend and prepareFork are the only parsers of a prompt verb's argv.
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

type sendPlan struct {
	opts   sendOpts
	spec   string
	prompt string
}

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

// With no spec the prompt goes to `implied`: the binding at a shell, the
// subject in the pager.
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
