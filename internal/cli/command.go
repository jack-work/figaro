package cli

// Command mode: the ':' line in the transcript. A bare coordinate is a goto;
// everything else is a CLI verb.
//
//	:listen <spec>   look at another aria; attendance is untouched
//	:attend <spec>   bind this shell to it, and look at it
//	:at <spec>       the same, abbreviated
//	:send [<spec>] -- <text>   send; no spec means the aria on screen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/mark"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/figaro/sdk/aria"
	"github.com/jack-work/jkrpc"
)

const commandTimeout = 10 * time.Second

// commandAsync runs fn off the input goroutine; the note is a verb's only
// feedback channel.
func (in *interactiveInput) commandAsync(fn func(context.Context) (string, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		msg, err := fn(withProgress(ctx, in.note))
		if err != nil {
			in.noteErr(wireErrorText(err))
			return
		}
		in.note(msg)
	}()
}

func wireErrorText(err error) string {
	var jerr *jkrpc.Error
	if errors.As(err, &jerr) {
		return strings.Replace(err.Error(), jerr.Error(), jerr.Message, 1)
	}
	return err.Error()
}

func (in *interactiveInput) noteErr(msg string) {
	in.mu.Lock()
	in.lt.tr.setCommandNoteAt(msg, alertError)
	in.mu.Unlock()
}

// note writes one line into the pager's status row, under the render lock.
func (in *interactiveInput) note(msg string) {
	in.mu.Lock()
	in.lt.tr.setCommandNote(msg)
	in.mu.Unlock()
}

func (in *interactiveInput) progress(msg string) {
	in.mu.Lock()
	in.lt.tr.setProgressNote(msg)
	in.mu.Unlock()
}

func (in *interactiveInput) noteLocked(msg string) { in.lt.tr.setCommandNote(msg) }

// sendComposer is called with the render lock held, so it hands off.
func (in *interactiveInput) sendComposer(text string) { go in.execSendComposer(text) }

// execSendComposer submits the draft byte-for-byte: a draft is not a command line.
func (in *interactiveInput) execSendComposer(text string) {
	in.commandAsync(func(ctx context.Context) (string, error) {
		plan := sendPlan{prompt: text}
		id, resp, err := sendVerb(ctx, verbEnv{}, plan, in.aria(), in.currentID())
		if err != nil {
			return "", fmt.Errorf("send: %w", err)
		}
		if resp != nil && resp.Active {
			in.mu.Lock()
			in.lt.tr.openQueueFromKey()
			in.mu.Unlock()
		}
		_ = id
		return "sent", nil
	})
}

func (in *interactiveInput) completeComposer(line string) []string {
	completionSubject = in.figaroID
	defer func() { completionSubject = "" }()
	return composerCandidates(line)
}

// commandSend is `:send [<spec>] -- <text>`; sending to another aria does not
// switch the transcript.
func (in *interactiveInput) commandSend(ctx context.Context, args []string) (string, error) {
	plan, err := prepareSend(args, pagerSurface)
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	env, err := in.verbEnv()
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	id, resp, err := sendVerb(ctx, env, plan, in.aria(), in.currentID())
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	if plan.spec == "" {
		if resp != nil && resp.Active {
			in.mu.Lock()
			in.lt.tr.openQueueFromKey()
			in.mu.Unlock()
		}
		return "sent", nil
	}
	return "sent to " + id, nil
}

// commandFork is `:fork [<spec>] [-S k=v] [-O outfit] [--stay] -- <prompt>`:
// mint, rebind, prompt, show the branch.
func (in *interactiveInput) commandFork(ctx context.Context, args []string) (string, error) {
	plan, err := prepareFork(args, pagerSurface)
	if err != nil {
		return "", fmt.Errorf("fork: %w", err)
	}
	if plan.compose {
		return "", errors.New("fork: the prompt must follow `--` (the pager has no composer)")
	}
	if plan.prompt == "" {
		return "", errors.New("fork: the prompt must follow `--` (a bare fork has no reply to show here; use the shell)")
	}
	if plan.spec == "" {
		plan.spec = in.currentID()
	}
	env, err := in.verbEnv()
	if err != nil {
		return "", fmt.Errorf("fork: %w", err)
	}
	// Without --stay a fork both shows the branch and moves attendance to it.
	intent := bindBranch
	if plan.opts.stay {
		intent = bindStay
	}
	out, err := forkVerb(ctx, env, plan, intent)
	if err != nil {
		return "", fmt.Errorf("fork: %w", err)
	}
	branch := out.Alternative
	_, ep, err := in.resolve(ctx, branch)
	if err != nil {
		return "", fmt.Errorf("fork: forked %s but could not reach it: %w", branch, err)
	}
	fcli, err := sdk.DialAria(ep, nil)
	if err != nil {
		return "", fmt.Errorf("fork: forked %s but could not connect: %w", branch, err)
	}
	if _, _, err := fcli.Qua(ctx, plan.prompt, buildPromptForm(plan.opts.outfit)); err != nil {
		fcli.Close()
		return "", fmt.Errorf("fork: forked %s but the prompt was refused: %w", branch, err)
	}
	fcli.Close()
	done := fmt.Sprintf("forked %s at %s, prompting %s", out.Parent, out.At, branch)
	if plan.opts.stay {
		return done + " (:listen " + branch + " to follow)", nil
	}
	if out.Rebound {
		done += ", attending"
	} else if out.BindNote != "" {
		done += " (" + out.BindNote + ")"
	}
	in.mu.Lock()
	owns := in.ownsSubject
	in.mu.Unlock()
	if !owns {
		return done + "; this is a send session and cannot change aria (:listen " + branch + " from a listen)", nil
	}
	from := in.currentID()
	if err := in.retarget(ctx, branch, ep); err != nil {
		return "", fmt.Errorf("%s, but could not show it: %w", done, err)
	}
	in.recordAttend(from, branch, arrivalNew)
	return done, nil
}

func (in *interactiveInput) verbEnv() (verbEnv, error) {
	acli, err := in.angelus()
	if err != nil {
		return verbEnv{}, err
	}
	return verbEnv{loaded: in.loaded, acli: acli, shellPID: shellPID}, nil
}

// switchSubject points the transcript at another aria; attend also binds the shell
// to it.
func (in *interactiveInput) switchSubject(ctx context.Context, spec string, attend bool) (string, error) {
	return in.switchSubjectAs(ctx, spec, attend, arrivalNew)
}

func (in *interactiveInput) switchSubjectAs(ctx context.Context, spec string, attend bool, how arrival) (string, error) {
	// A session that does not own its connection (`figaro send`) cannot change
	// subject: its notify pump is not fenced by the subject generation.
	in.mu.Lock()
	owns := in.ownsSubject
	in.mu.Unlock()
	if !owns {
		return "", fmt.Errorf("changing aria needs a `figaro listen` session (this one is a send)")
	}
	env, err := in.verbEnv()
	if err != nil {
		return "", err
	}
	var id string
	var ep transport.Endpoint
	if attend {
		out, err := attendVerb(ctx, env, spec)
		if err != nil {
			return "", fmt.Errorf("attend: %w", err)
		}
		if out.Home {
			return out.Note, nil
		}
		id, ep = out.ID, out.EP
	} else {
		id, ep, err = listenVerb(ctx, env, spec)
		if err != nil {
			return "", err
		}
	}
	if id == in.currentID() {
		if attend {
			return "attending " + id + " (already showing)", nil
		}
		return "already showing " + id, nil
	}
	from := in.currentID()
	if err := in.retarget(ctx, id, ep); err != nil {
		return "", err
	}
	if attend {
		pos, total := in.recordAttend(from, id, how)
		return fmt.Sprintf("attending %s (%d/%d)", id, pos, total), nil
	}
	return "showing " + id, nil
}

// arrival says how a switch enters the jumplist: a new destination, or a step
// along the existing path.
type arrival int

const (
	arrivalNew  arrival = 0
	arrivalStep arrival = 1
)

// recordAttend is the one place an attendance transition enters the jumplist.
func (in *interactiveInput) recordAttend(from, to string, how arrival) (int, int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch how {
	case arrivalStep:
		in.jumps.visit(from)
		if to != "" {
			in.jumps.stepTo(to)
		}
	default:
		in.jumps.arrive(from, to)
	}
	return in.jumps.where()
}

// attendFromPager runs on the dispatch path, which holds the render lock, so
// the work hands off.
func (in *interactiveInput) attendFromPager(id string) {
	if id == "" {
		return
	}
	in.commandAsync(func(ctx context.Context) (string, error) { return in.attendNote(ctx, id) })
}

// commandKill decides the successor before the kill: a dead aria cannot be
// asked for its parent.
func (in *interactiveInput) commandKill(ctx context.Context, args []string) (string, error) {
	spec, recursive, err := parseKillArgs(args)
	if err != nil {
		return "", err
	}
	id := in.currentID()
	if spec != "" {
		resolved, _, rerr := in.resolve(ctx, spec)
		if rerr != nil {
			return "", fmt.Errorf("kill: %w", rerr)
		}
		id = resolved
	}
	if id == "" {
		return "", errors.New("kill: no aria named and none on screen")
	}
	return in.killAria(ctx, id, recursive, "")
}

// prefer is honoured only if alive and not the aria being killed.
func (in *interactiveInput) killAria(ctx context.Context, id string, recursive bool, prefer string) (string, error) {
	acli, err := in.angelus()
	if err != nil {
		return "", err
	}
	// Move before you kill: killing the aria on screen closes the connection
	// under the session.
	onScreen := id == in.currentID()
	if !onScreen {
		if err := acli.Kill(ctx, id, recursive); err != nil {
			return "", fmt.Errorf("kill %s: %w", id, err)
		}
		in.forgetAria(id)
		return "killed " + id, nil
	}
	successor := in.successorFor(ctx, acli, id, prefer)
	if successor == "" {
		if err := acli.Kill(ctx, id, recursive); err != nil {
			return "", fmt.Errorf("kill %s: %w", id, err)
		}
		in.forgetAria(id)
		in.leaveSession()
		return "killed " + id + ": nothing left to attend", nil
	}
	note, aerr := in.attendNote(ctx, successor)
	if aerr != nil {
		return "", aerr
	}
	if err := acli.Kill(ctx, id, recursive); err != nil {
		return "", fmt.Errorf("kill %s: %w", id, err)
	}
	in.forgetAria(id)
	return "killed " + id + "; " + note, nil
}

func (in *interactiveInput) forgetAria(id string) {
	in.mu.Lock()
	in.jumps.forget(id)
	in.mu.Unlock()
}

func (in *interactiveInput) askKill(args []string) {
	spec, _, err := parseKillArgs(args)
	if err != nil {
		in.noteErr(err.Error())
		return
	}
	target := spec
	if target == "" {
		target = in.currentID()
	}
	if target == "" {
		in.noteErr("kill: no aria named and none on screen")
		return
	}
	in.mu.Lock()
	in.lt.tr.askConfirm(killPrompt(target), func() {
		in.commandAsync(func(ctx context.Context) (string, error) {
			return in.commandKill(ctx, args)
		})
	})
	in.mu.Unlock()
}

func parseKillArgs(args []string) (spec string, recursive bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-r" || a == "--recursive":
			recursive = true
		case a == "--id" || a == "-i":
			if i+1 < len(args) {
				i++
				spec = args[i]
			}
		case strings.HasPrefix(a, "--id="):
			spec = strings.TrimPrefix(a, "--id=")
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("kill: unknown flag %s", a)
		default:
			spec = a
		}
	}
	return spec, recursive, nil
}

// successorFor: the caller's preferred neighbour, else the jumplist's most
// recent living aria, else the parent.
func (in *interactiveInput) successorFor(ctx context.Context, acli *sdk.Angelus, id, prefer string) string {
	resp, err := acli.List(ctx)
	if err != nil {
		return ""
	}
	alive := make(map[string]bool, len(resp.Figaros))
	parent := ""
	for _, f := range resp.Figaros {
		alive[f.ID] = true
		if f.ID != id {
			continue
		}
		parent = f.Present
		if parent == "" {
			parent = f.Parent
		}
	}
	if prefer != "" && prefer != id && alive[prefer] {
		return prefer
	}
	in.mu.Lock()
	trail := in.jumps.trail()
	in.mu.Unlock()
	for i := len(trail) - 1; i >= 0; i-- {
		if trail[i] != id && alive[trail[i]] {
			return trail[i]
		}
	}
	if parent != "" && alive[parent] {
		return parent
	}
	return ""
}

func (in *interactiveInput) attendParentFromPager() {
	in.commandAsync(func(ctx context.Context) (string, error) {
		id := in.currentID()
		if id == "" {
			return "", errors.New("no aria on screen")
		}
		acli, err := in.angelus()
		if err != nil {
			return "", err
		}
		resp, lerr := acli.List(ctx)
		if lerr != nil {
			return "", fmt.Errorf("parent: %w", lerr)
		}
		parent := ""
		for _, f := range resp.Figaros {
			if f.ID != id {
				continue
			}
			parent = f.Present
			if parent == "" {
				parent = f.Parent
			}
			break
		}
		// A top-level aria's parent is its outfit stump, which attend refuses.
		if parent == "" || strings.HasPrefix(parent, "@") {
			return id + " has no parent aria: it is top-level", nil
		}
		return in.attendNote(ctx, parent)
	})
}

func (in *interactiveInput) leaveSession() {
	select {
	case in.disconnectCh <- struct{}{}:
	default:
	}
}

func (in *interactiveInput) attendNote(ctx context.Context, id string) (string, error) {
	return in.attendNoteAs(ctx, id, arrivalNew)
}

func (in *interactiveInput) attendNoteAs(ctx context.Context, id string, how arrival) (string, error) {
	note, err := in.switchSubjectAs(ctx, id, true, how)
	if err != nil {
		return "", fmt.Errorf("attend: %w", err)
	}
	return note, nil
}

func (in *interactiveInput) hopAria(dir int) { go in.hop(dir) }

// hop moves the cursor first, so the attend that follows is recorded as
// arriving where the cursor already stands.
func (in *interactiveInput) hop(dir int) {
	in.mu.Lock()
	in.jumps.visit(in.figaroID)
	id, ok := in.jumps.peek(dir)
	pos, total := in.jumps.where()
	in.mu.Unlock()
	if !ok {
		if dir < 0 {
			in.note(fmt.Sprintf("jumplist: nothing older (%d/%d)", pos, total))
		} else {
			in.note(fmt.Sprintf("jumplist: nothing newer (%d/%d)", pos, total))
		}
		return
	}
	in.commandAsync(func(ctx context.Context) (string, error) {
		return in.attendNoteAs(ctx, id, arrivalStep)
	})
}

// resolve goes through the same resolver `figaro listen` uses, so `@role`
// follows target-aria.
func (in *interactiveInput) resolve(ctx context.Context, spec string) (string, transport.Endpoint, error) {
	acli, err := in.angelus()
	if err != nil {
		return "", transport.Endpoint{}, err
	}
	return resolveFigaroTargetEndpoint(ctx, in.loaded, acli, spec, false, dressing{})
}

// angelus dials the daemon lazily, on first use.
func (in *interactiveInput) angelus() (*sdk.Angelus, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.acli != nil {
		return in.acli, nil
	}
	if in.loaded == nil {
		return nil, fmt.Errorf("this session has no daemon connection")
	}
	cli, err := sdk.DialAngelus(transport.UnixEndpoint(boot.AngelusSocketPath()))
	if err != nil {
		return nil, fmt.Errorf("connect angelus: %w", err)
	}
	in.acli = cli
	return cli, nil
}

func (in *interactiveInput) currentID() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.figaroID
}

func (in *interactiveInput) aria() *sdk.Aria {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.subject
}

// retarget dials an aria and makes it the subject; `figaro listen` opens through
// it too.
func (in *interactiveInput) retarget(ctx context.Context, id string, ep transport.Endpoint) error {
	// The old connection's pump is live while we dial and its frames carry the
	// old aria's coordinates; every handler checks the generation it was born
	// with before touching the renderer.
	gen := atomic.AddUint64(&in.subjectGen, 1)
	from := in.currentID()
	hop := mark.Span("hop", "from", from, "to", id, "gen", gen)

	base := make(chan int, 1)
	go func() { base <- in.divergence(ctx, id, from) }()

	dial := mark.Span("hop.dial", "to", id, "gen", gen)
	fcli, err := sdk.DialAriaWith(ep, in.notifyHandler(gen, id), markTap(in.tap))
	dial()
	// Asking the angelus takes in.mu, so the answer is taken before the lock.
	shared := <-base
	if err != nil {
		return fmt.Errorf("connect %s: %w", id, err)
	}

	old, ownedOld, claimed := in.claimSubject(gen, id, from, fcli, shared)
	if !claimed {
		fcli.Close()
		mark.Mark("hop.superseded", "to", id, "gen", gen)
		return nil
	}

	// A new subject has different intrinsic forms: drop the mirrors, then seed.
	// Paint what is held before asking for what is not.
	in.paintHeld(gen)

	in.intrinsics.reset(gen)
	in.forgetQueueDebuts()
	in.seedIntrinsics()
	if old != nil && ownedOld {
		old.Close()
	}
	go func() {
		<-fcli.Done()
		if atomic.LoadUint64(&in.subjectGen) != gen {
			return
		}
		select {
		case in.subjectDead <- struct{}{}:
		default:
		}
	}()

	// Both reads carry the generation: a seed owed to a superseded switch must
	// not be applied.
	in.seedMetrics(gen)
	in.recomputeLimit()
	in.seedBoard(gen)
	in.seedSubject(gen)
	hop()
	return nil
}

// claimSubject is the swap. The fence is checked here, under the lock, so two
// hops install in the order asked for rather than in dial order. Returns the
// displaced connection, whether it was ours to close, and whether the claim was
// granted.
func (in *interactiveInput) claimSubject(gen uint64, id, from string, fcli *sdk.Aria, shared int) (*sdk.Aria, bool, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.current(gen) {
		return nil, false, false
	}
	old, ownedOld := in.subject, in.ownsSubject
	in.subject, in.fcli, in.hangup, in.ownsSubject = fcli, fcli, fcli, true
	in.figaroID = id
	in.caughtUp = false
	status := newSessionStatus(id, time.Now())
	// The outgoing subject is shelved before the new one takes its place,
	// because retarget drops it.
	in.parkSubject(from)
	in.board = &formMirror{}
	if p := in.takeParked(id); p != nil {
		in.seed = in.lt.adopt(id, status, p, shared)
		if p.board != nil {
			in.board = p.board
		}
	} else {
		in.seed = in.lt.retarget(id, status, shared)
	}
	in.lt.setDesync(in.desyncHandler(gen))
	// Hooks are armed here, on both the cold and the warm door.
	in.wireHooks()
	return old, ownedOld, true
}

func (in *interactiveInput) paintHeld(gen uint64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.current(gen) || !in.lt.transcriptActive() || in.lt.client.Count() == 0 {
		return
	}
	in.lt.invalidateTranscriptWindow()
	in.lt.renderNow()
	mark.Mark("hop.paint", "gen", gen, "msgs", in.lt.client.Count())
}

func (in *interactiveInput) current(gen uint64) bool {
	return atomic.LoadUint64(&in.subjectGen) == gen
}

func (in *interactiveInput) subjectGeneration() uint64 {
	return atomic.LoadUint64(&in.subjectGen)
}

func (in *interactiveInput) seedRead(ctx context.Context, p seedPlan) (aria.Page, error) {
	tail := aria.Anchor{Turn: recentCursor}
	floor := aria.Anchor{Turn: uint64(p.from)}
	switch p.kind {
	case seedNothing:
		mark.Mark("hop.read", "kind", "seed.none", "from", p.from)
		return aria.Page{}, nil
	case seedSuffix:
		read := mark.Span("hop.read", "kind", "seed.suffix", "from", p.from)
		r, err := in.fcli.Read(ctx, floor, wireBudget(transcriptPageSize))
		read("parts", len(r.Parts), "err", err != nil)
		return r, err
	}
	if tr, ok := in.fcli.(tailReader); ok && p.from > 0 {
		read := mark.Span("hop.read", "kind", "seed.tail", "from", p.from)
		r, err := tr.ReadBeforeFloor(ctx, tail, floor, wireBudget(transcriptPageSize))
		read("parts", len(r.Parts), "err", err != nil)
		return r, err
	}
	read := mark.Span("hop.read", "kind", "seed")
	r, err := in.fcli.ReadBefore(ctx, tail, wireBudget(transcriptPageSize))
	read("parts", len(r.Parts), "err", err != nil)
	return r, err
}

// divergence is the first turn the target does not share with the aria being
// left; zero is "share nothing", which every failure answers.
func (in *interactiveInput) divergence(ctx context.Context, to, from string) int {
	if to == "" || from == "" || to == from {
		return 0
	}
	acli, err := in.angelus()
	if err != nil {
		return 0
	}
	done := mark.Span("hop.lineage", "to", to, "from", from)
	lctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := acli.Lineage(lctx, to, from)
	if err != nil || out == nil {
		done("shared", 0, "err", true)
		return 0
	}
	shared := int(min(out.Divergence, uint64(maxRetainedTurn)))
	in.mu.Lock()
	// A changed epoch means the tree changed shape: the divergence in hand is
	// current, the shelf is not.
	if in.lineageEpoch != out.Epoch {
		if len(in.parked) > 0 {
			mark.Mark("hop.unpark", "reason", "epoch", "held", len(in.parked))
		}
		in.dropParked()
		in.lineageEpoch = out.Epoch
	}
	in.mu.Unlock()
	done("shared", shared, "ancestor", out.Ancestor, "epoch", out.Epoch)
	return shared
}

const maxRetainedTurn = 1 << 40

// seedMetrics asks for the bar's figures: nothing volunteers them, and this
// must not smuggle a message into the window.
func (in *interactiveInput) seedMetrics(gen uint64) {
	cli := in.aria()
	if cli == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		m, err := cli.Metrics(ctx)
		if err != nil || m == nil {
			return
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		if !in.current(gen) {
			return
		}
		in.lt.status.update(*m)
		in.lt.render()
	}()
}

// notifyHandler folds a connection's frames only while that connection is the
// claimed subject: the generation alone is not a fence, since the connection is
// subscribed before claimSubject swaps the client (#25).
func (in *interactiveInput) notifyHandler(gen uint64, id string) sdk.NotifyHandler {
	return func(method string, params json.RawMessage) {
		if atomic.LoadUint64(&in.subjectGen) != gen {
			return
		}
		// A form patch takes the render lock itself and may resync over the
		// wire, so it is handled before the lock is taken.
		if method == rpc.MethodFormDelta {
			if in.currentID() != id {
				return
			}
			in.applyFormDelta(gen, params)
			return
		}
		in.mu.Lock()
		if in.figaroID != id {
			in.mu.Unlock()
			mark.Mark("hop.early", "to", id, "method", method)
			return
		}
		switch method {
		case rpc.MethodAriaFrame:
			in.turnFrame(params)
		case rpc.MethodTurnDone:
			in.turnDone(params)
		}
		// A gap a live frame put on screen is chased now, not at the next
		// keystroke.
		chase := in.lt.transcriptShowsGap()
		in.mu.Unlock()
		if chase {
			go in.pageTranscript()
		}
	}
}

func (in *interactiveInput) desyncHandler(gen uint64) func(int) {
	return func(sinceLT int) {
		go func() {
			if atomic.LoadUint64(&in.subjectGen) != gen {
				return
			}
			rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rcancel()
			cli := in.aria()
			if cli == nil {
				return
			}
			r, rerr := cli.Read(rctx, aria.Anchor{Turn: uint64(sinceLT)}, 0)
			if rerr != nil || atomic.LoadUint64(&in.subjectGen) != gen {
				return
			}
			in.mu.Lock()
			in.lt.apply(r)
			in.mu.Unlock()
		}()
	}
}

// wireHooks is the one place the pager's hooks are armed. Caller holds the render lock.
func (in *interactiveInput) wireHooks() {
	in.lt.setQueuedFetch(in.refreshQueued)
	in.lt.setHistoryFetcher(in.historyFetcher())
	in.lt.setCommandRunner(in.runCommand)
	in.lt.setCommandCompleter(in.complete)
	in.lt.tr.composerCompleter = in.completeComposer
	in.lt.tr.sendComposer = in.sendComposer
	in.lt.tr.decideComposer = in.decideComposer
	in.lt.setCatchUp(in.pagerCatchUp)
	in.lt.tr.dropRow = in.dropPitRow
	in.lt.tr.attendAria = in.attendFromPager
	in.lt.tr.ariaHop = in.hopAria
	in.lt.tr.attendParent = in.attendParentFromPager
	// Hooks called from dispatch already hold the render lock: hand off.
	in.lt.tr.openForm = func() { go in.openLive("form show", "", false) }
	in.lt.tr.openPath = in.openPath
}

func (in *interactiveInput) seedSubject(gen uint64) {
	in.mu.Lock()
	active := in.lt.transcriptActive()
	inline := in.startInline
	in.mu.Unlock()
	if inline {
		// An ordinary send stays inline.
		return
	}
	if !active {
		in.enterTranscript()
		return
	}
	in.mu.Lock()
	plan := in.seed
	in.mu.Unlock()
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	r, rerr := in.seedRead(rctx, plan)
	rcancel()
	if plan.kind == seedNothing {
		in.mu.Lock()
		defer in.mu.Unlock()
		if !in.current(gen) {
			return
		}
		in.caughtUp = true
		in.wireHooks()
		in.lt.invalidateTranscriptWindow()
		in.lt.renderNow()
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	// A page owed to a superseded switch belongs to another aria.
	if !in.current(gen) {
		return
	}
	in.caughtUp = rerr == nil
	if rerr == nil {
		in.lt.apply(r)
		// A floored page's More.Before is about the floor, not the aria's
		// beginning; the retained store already carries the real flag.
		if plan.kind == seedWindow {
			in.lt.setMoreBefore(r.More.Before)
		}
	}
	in.wireHooks()
	in.lt.invalidateTranscriptWindow()
	in.lt.renderNow()
}

func (in *interactiveInput) dropPitRow(name, id, next string) {
	if name != "queue" {
		if rpc.ValidateAriaID(id) == nil {
			in.commandAsync(func(ctx context.Context) (string, error) {
				return in.killAria(ctx, id, false, next)
			})
		}
		return
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		cli := in.aria()
		if cli == nil {
			return
		}
		// A refusal is a normal answer, carried per id in the results.
		in.mu.Lock()
		epoch := in.queueEpoch
		in.mu.Unlock()
		resp, err := cli.DeleteQueued(ctx, rpc.QueueDeleteRequest{Epoch: epoch, IDs: []uint64{n}})
		if err != nil {
			in.note("queue rm: " + err.Error())
		} else {
			for _, r := range resp.Results {
				if r.Outcome == rpc.QueueRejected {
					msg := "queue rm " + id + ": " + string(r.Reason)
					if r.Detail != "" {
						msg += " (" + r.Detail + ")"
					}
					in.note(msg)
				}
			}
		}
		in.refreshQueued()
	}()
}
