package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

const (
	succeedDefaultTurn = 2
	succeedDefaultTTL  = "10m"
	succeededPrefix    = "SUCCEEDED: "

	abdicateRetire = "retire"
	abdicateLinger = "linger"
)

type succeedPlan struct {
	role       string
	successor  string
	turn       uint64
	ttl        time.Duration
	abdication string
	title      string
	prompt     string
	stay       bool
}

type succeedReport struct {
	Role         string   `json:"role"`
	Predecessor  string   `json:"predecessor"`
	Mantra       string   `json:"predecessor_mantra,omitempty"`
	Successor    string   `json:"successor,omitempty"`
	Forked       bool     `json:"forked,omitempty"`
	Turn         uint64   `json:"turn,omitempty"`
	DemotedUnder string   `json:"demoted_under,omitempty"`
	Prompted     bool     `json:"prompted"`
	Steered      bool     `json:"steered,omitempty"`
	Cast         bool     `json:"cast"`
	Dropped      bool     `json:"dropped"`
	Abdication   string   `json:"abdication,omitempty"`
	TTL          string   `json:"ttl,omitempty"`
	Killed       bool     `json:"killed,omitempty"`
	Abdicated    bool     `json:"abdicated"`
	FailedAt     string   `json:"failed_at,omitempty"`
	Error        string   `json:"error,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

func (r *succeedReport) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

func (r *succeedReport) fail(step string, err error) error {
	r.FailedAt, r.Error = step, err.Error()
	return fmt.Errorf("succeed: %s: %w", step, err)
}

func parseSucceedPlan(role, successor, turn, ttl, abdication, title, prompt string) (succeedPlan, error) {
	p := succeedPlan{role: role, successor: successor, title: title, prompt: prompt, turn: succeedDefaultTurn}
	if strings.TrimSpace(role) == "" {
		return p, errors.New("name the role: succeed <@role> -- <prompt>")
	}
	if strings.TrimSpace(prompt) == "" {
		return p, errors.New("the succession prompt must follow `--`")
	}
	if turn != "" {
		var n uint64
		if _, err := fmt.Sscanf(turn, "%d", &n); err != nil || n < 1 || fmt.Sprint(n) != turn {
			return p, fmt.Errorf("--turn %q: want a turn number, 1 or more", turn)
		}
		if successor != "" {
			return p, errors.New("--turn picks where to fork; --successor names an aria that already exists. Pass one")
		}
		p.turn = n
	}
	if ttl == "" {
		ttl = succeedDefaultTTL
	}
	d, err := store.ParseTTL(ttl)
	if err != nil || d < 0 {
		return p, fmt.Errorf("--ttl %q: want a duration (0, 10m, 6h, 2d)", ttl)
	}
	p.ttl = d
	switch abdication {
	case "", abdicateRetire:
		p.abdication = abdicateRetire
	case abdicateLinger:
		p.abdication = abdicateLinger
	default:
		return p, fmt.Errorf("--abdication %q: want retire or linger", abdication)
	}
	return p, nil
}

func succeedPlanFrom(ctx *cmdkit.RunContext) (succeedPlan, error) {
	role, prompt := splitPromptArgs(ctx.Args, ctx.AfterSeparator)
	if len(role) > 1 {
		return succeedPlan{}, fmt.Errorf("succeed: one role, got %v (the prompt goes after --)", role)
	}
	plan, err := parseSucceedPlan(first(role), ctx.Flag("successor"), ctx.Flag("turn"), ctx.Flag("ttl"),
		ctx.Flag("abdication"), ctx.Flag("title"), prompt)
	if err != nil {
		return plan, fmt.Errorf("succeed: %w", err)
	}
	plan.stay = ctx.BoolFlag("stay")
	return plan, nil
}

type succeedParties struct {
	role, predecessor, successor *rpc.FigaroInfoResponse
	otherRoles                   []string
}

func succeedAssert(ctx context.Context, acli *sdk.Angelus, plan succeedPlan) (succeedParties, error) {
	var p succeedParties
	roleID, err := doorID(ctx, acli, plan.role)
	if err != nil {
		return p, fmt.Errorf("role %s: %w", plan.role, err)
	}
	successorID := ""
	if plan.successor != "" {
		if successorID, err = doorID(ctx, acli, plan.successor); err != nil {
			return p, fmt.Errorf("successor %s: %w", plan.successor, err)
		}
	}
	listing, err := acli.ListGlobal(ctx)
	if err != nil {
		return p, fmt.Errorf("list: %w", err)
	}
	row := func(id string) *rpc.FigaroInfoResponse {
		for i := range listing.Figaros {
			if listing.Figaros[i].ID == id {
				return &listing.Figaros[i]
			}
		}
		return nil
	}
	if p.role = row(roleID); p.role == nil || !isFormRow(p.role) {
		return p, fmt.Errorf("%s is not a role (an unbound form carrying target-aria)", plan.role)
	}
	target := p.role.TargetAria
	if target == "" {
		if f, ferr := acli.Form(ctx, roleID); ferr == nil {
			target = roleTargetOf(p.role, f.Snapshot)
		}
	}
	if target == "" {
		return p, fmt.Errorf("%s targets no aria; cast one into it instead: figaro cast <aria> %s", ariaName(roleID), ariaName(roleID))
	}
	if p.predecessor = row(target); p.predecessor == nil || isFormRow(p.predecessor) {
		return p, fmt.Errorf("%s targets %s, which does not exist", ariaName(roleID), ariaName(target))
	}
	if p.predecessor.ReapedAt != 0 {
		return p, fmt.Errorf("%s targets %s, which was reaped", ariaName(roleID), ariaName(target))
	}
	for _, f := range listing.Figaros {
		if f.ID != roleID && f.TargetAria == target {
			p.otherRoles = append(p.otherRoles, f.ID)
		}
	}
	if p.predecessor.RetiredAt != 0 && successorID == "" {
		return p, fmt.Errorf("%s targets %s, which is retired and cannot be forked; name a --successor", ariaName(roleID), ariaName(target))
	}
	if successorID == "" {
		return p, nil
	}
	if successorID == target {
		return p, fmt.Errorf("%s already targets %s", ariaName(roleID), ariaName(target))
	}
	if p.successor = row(successorID); p.successor == nil || isFormRow(p.successor) {
		return p, fmt.Errorf("successor %s is not an aria", plan.successor)
	}
	if p.successor.RetiredAt != 0 || p.successor.ReapedAt != 0 {
		return p, fmt.Errorf("successor %s is retired or reaped and takes no prompts", ariaName(successorID))
	}
	return p, nil
}

func mantraPatch(m string) *rpc.FormPatch {
	raw, _ := json.Marshal(m)
	p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": raw}, nil)
	return &p
}

func succeededMantra(m string) string {
	if strings.HasPrefix(m, succeededPrefix) {
		return m
	}
	return succeededPrefix + m
}

func abdicationPatch(plan succeedPlan, mantra string, createdAt int64, now time.Time) rpc.FormPatch {
	patch := form.Build(form.Snapshot{}, nil, nil)
	if plan.abdication == abdicateRetire {
		patch = retirePatch(dressing{}, false, now)
	}
	raw, _ := json.Marshal(succeededMantra(mantra))
	ttl, _ := json.Marshal(ttlFromNow(createdAt, plan.ttl, now))
	return form.Merge(patch, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra":           raw,
		store.SystemTTLKey: ttl,
	}, nil))
}

type succeedHooks struct {
	progress func(string)
	handover func(succeedReport) error
}

func succeedVerb(ctx context.Context, acli *sdk.Angelus, plan succeedPlan, hooks succeedHooks) (succeedReport, error) {
	progress := hooks.progress
	if progress == nil {
		progress = func(string) {}
	}
	r := succeedReport{Abdication: plan.abdication}
	parties, err := succeedAssert(ctx, acli, plan)
	if err != nil {
		return r, r.fail("assert", err)
	}
	role, old := parties.role.ID, parties.predecessor
	r.Role, r.Predecessor, r.Mantra = role, old.ID, old.Mantra
	for _, other := range parties.otherRoles {
		r.warn("%s also targets %s and stays pointed at it, holding it from the reap", ariaName(other), ariaName(old.ID))
	}
	if n := len(old.BoundPIDs); n > 0 && plan.ttl > 0 {
		r.warn("%d shell(s) attend %s and hold it from the reap until they move", n, ariaName(old.ID))
	}

	if parties.successor != nil {
		r.Successor = parties.successor.ID
		if plan.title != "" {
			if err := withAria(r.Successor, func(a *sdk.Aria) error {
				_, err := a.Set(ctx, *mantraPatch(plan.title), 0)
				return err
			}); err != nil {
				return r, r.fail("title", err)
			}
		}
	} else {
		progress(fmt.Sprintf("forking %s at turn %d", ariaName(old.ID), plan.turn))
		var patch *rpc.FormPatch
		if plan.title != "" {
			patch = mantraPatch(plan.title)
		}
		out, err := acli.Fork(ctx, old.ID, plan.turn, 0, nil, patch)
		if err != nil {
			return r, r.fail("fork", err)
		}
		r.Successor, r.Forked, r.Turn = out.Alternative, true, plan.turn
		if out.OwnerNote != "" {
			r.warn("%s", out.OwnerNote)
		}
	}

	progress("demoting " + ariaName(old.ID))
	dem, err := acli.Demote(ctx, old.ID, r.Successor)
	switch {
	case err != nil:
		return r, r.fail("demote", err)
	case dem.Unsupported:
		r.warn("no trunk capability: %s was not demoted, and deleting it takes what is drawn under it", ariaName(old.ID))
	case dem.Under != "" && !dem.Preferred:
		r.warn("%s is not drawn under %s; %s took its place instead", ariaName(r.Successor), ariaName(old.ID), ariaName(dem.Under))
	}
	r.DemotedUnder = dem.Under

	progress("prompting " + ariaName(r.Successor))
	if err := withAria(r.Successor, func(a *sdk.Aria) error {
		_, active, err := a.Qua(ctx, plan.prompt, nil)
		r.Steered = active
		return err
	}); err != nil {
		return r, r.fail("prompt", err)
	}
	r.Prompted = true

	if err := withAria(r.Successor, func(a *sdk.Aria) error {
		_, err := a.Cast(ctx, rpc.CastRequest{FormID: role})
		return err
	}); err != nil {
		return r, r.fail("cast", err)
	}
	r.Cast = true

	if hooks.handover != nil {
		if err := hooks.handover(r); err != nil {
			r.warn("%s", err)
		}
	}

	if err := withAria(old.ID, func(a *sdk.Aria) error {
		_, err := a.Drop(ctx, role)
		return err
	}); err != nil {
		r.warn("%s still studies %s: %s", ariaName(old.ID), ariaName(role), err)
	} else {
		r.Dropped = true
	}

	if plan.ttl == 0 {
		progress("killing " + ariaName(old.ID))
		if _, err := acli.Kill(ctx, rpc.KillRequest{FigaroID: old.ID}); err != nil {
			return r, r.fail("kill", err)
		}
		r.Killed, r.Abdicated = true, true
		return r, nil
	}
	r.TTL = shortDuration(plan.ttl)
	if err := withAria(old.ID, func(a *sdk.Aria) error {
		_, err := a.Set(ctx, abdicationPatch(plan, old.Mantra, old.CreatedAt, time.Now()), 0)
		return err
	}); err != nil {
		return r, r.fail("abdicate", err)
	}
	r.Abdicated = true
	return r, nil
}

func splitPromptArgs(args, tail []string) (positional []string, prompt string) {
	return args[:len(args)-len(tail)], strings.Join(tail, " ")
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func withAria(id string, fn func(*sdk.Aria) error) error {
	a, err := sdk.DialAria(ariaAddr(id), nil)
	if err != nil {
		return err
	}
	defer a.Close()
	return fn(a)
}

func (r succeedReport) recovery(plan succeedPlan) []string {
	var out []string
	if r.Successor == "" {
		return nil
	}
	if r.DemotedUnder == "" && r.FailedAt == "demote" {
		out = append(out, fmt.Sprintf("figaro demote %s --under %s", r.Predecessor, r.Successor))
	}
	if !r.Prompted {
		out = append(out, fmt.Sprintf("figaro send -f --id %s -- <the succession prompt>", r.Successor))
	}
	if !r.Cast {
		out = append(out, fmt.Sprintf("figaro cast %s %s", r.Successor, r.Role))
	}
	if !r.Dropped {
		out = append(out, fmt.Sprintf("figaro drop %s %s", r.Predecessor, r.Role))
	}
	if !r.Abdicated {
		if plan.ttl == 0 {
			out = append(out, "figaro kill "+r.Predecessor)
		} else if plan.abdication == abdicateRetire {
			out = append(out, fmt.Sprintf("figaro retire %s --ttl %s -S mantra=%q", r.Predecessor, shortDuration(plan.ttl), succeededMantra(r.Mantra)))
		} else {
			out = append(out, fmt.Sprintf("figaro set %s mantra %q", r.Predecessor, succeededMantra(r.Mantra)),
				fmt.Sprintf("figaro set %s system.ttl <its age + %s>", r.Predecessor, shortDuration(plan.ttl)))
		}
	}
	return out
}

func (r succeedReport) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s succeeds %s in %s", ariaName(r.Successor), ariaName(r.Predecessor), ariaName(r.Role))
	switch {
	case r.Killed:
		b.WriteString("; predecessor killed")
	case r.Abdication == abdicateLinger:
		fmt.Fprintf(&b, "; predecessor lingers, reaped in %s unless held", r.TTL)
	default:
		fmt.Fprintf(&b, "; predecessor retired, reaped in %s unless held", r.TTL)
	}
	return b.String()
}

func runSucceed(plan succeedPlan, asJSON bool) error {
	acli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		return fmt.Errorf("connect angelus: %w", err)
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	progress := func(s string) {
		if !asJSON {
			fmt.Fprintln(stderrw, s)
		}
	}
	r, serr := succeedVerb(ctx, acli, plan, succeedHooks{progress: progress})
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(r)
	}
	for _, w := range r.Warnings {
		fmt.Fprintln(stderrw, "warning: "+w)
	}
	if serr != nil {
		if steps := r.recovery(plan); len(steps) > 0 {
			fmt.Fprintln(stderrw, "the succession stopped partway; to finish it by hand:")
			for _, s := range steps {
				fmt.Fprintln(stderrw, "  "+s)
			}
		}
		return serr
	}
	if !asJSON {
		fmt.Fprintln(stdout, r.summary())
	}
	return nil
}

const succeedPagerTimeout = 2 * time.Minute

func (in *interactiveInput) commandSucceed(args []string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), succeedPagerTimeout)
		defer cancel()
		msg, err := in.succeed(ctx, args)
		if err != nil {
			in.noteErr(wireErrorText(err))
			return
		}
		in.note(msg)
	}()
}

func (in *interactiveInput) succeed(ctx context.Context, args []string) (string, error) {
	rc, err := buildRouter("figaro", in.loaded).Parse("succeed", args)
	if err != nil {
		return "", fmt.Errorf("succeed: %w", err)
	}
	plan, err := succeedPlanFrom(rc)
	if err != nil {
		return "", err
	}
	acli, err := in.angelus()
	if err != nil {
		return "", fmt.Errorf("succeed: %w", err)
	}
	moved := false
	handover := func(r succeedReport) error {
		if r.Predecessor != in.currentID() || (plan.stay && plan.ttl > 0) {
			return nil
		}
		if _, err := in.switchSubject(ctx, r.Successor, true); err != nil {
			return fmt.Errorf("the pager could not follow to %s: %w", ariaName(r.Successor), err)
		}
		moved = true
		return nil
	}
	r, err := succeedVerb(ctx, acli, plan, succeedHooks{progress: in.progress, handover: handover})
	if r.Killed {
		in.forgetAria(r.Predecessor)
	}
	if err != nil {
		if steps := r.recovery(plan); len(steps) > 0 {
			return "", fmt.Errorf("%w; to finish: %s", err, strings.Join(steps, "; "))
		}
		return "", err
	}
	msg := r.summary()
	if len(r.Warnings) > 0 {
		msg += " (" + strings.Join(r.Warnings, "; ") + ")"
	}
	if moved {
		msg += ", attending the successor"
	}
	return msg, nil
}
