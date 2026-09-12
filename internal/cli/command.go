package cli

// COMMAND MODE: the ':' line in the transcript.
//
// `:` opens a command line whose grammar is the CLI's, not a second dialect.
// The arrangement is vim's: a bare coordinate is a goto (`:12`, `:12.3`, `:0`,
// handled in transcript_jump.go), and everything else is a verb.
//
// THE SUBJECT. The transcript shows one aria at a time, and these verbs are how
// it changes. Their semantics are the shell's, with one difference that comes
// from the pager being ambiently open: a command that RESOLVES an aria replaces
// what is on screen. So `:listen` is `figaro listen` -- look at it, do not bind
// to it; YOU HAVE TO LISTEN TO A FIGARO, there is no `:open` -- and `:attend`
// is `figaro attend` AND a listen, because attending an aria you cannot see is
// not a thing a reader of this pager ever means.
//
//	:listen <spec>   look at another aria; attendance is untouched
//	:attend <spec>   bind this shell to it, AND look at it
//	:at <spec>       the same, abbreviated
//	:send [<spec>] [-f] -- <text>   send; no spec means the aria on screen.
//	                 Sent elsewhere, the transcript FOLLOWS the aria it sent
//	                 to, unless -f (forget) says stay: one rule, shared with
//	                 :fork, mirroring the shell verbs' stream-unless-forget.
//
// A <spec> is anything the CLI takes: an aria id, an `@form` role (resolved
// through target-aria by the same resolver `figaro listen` uses), or an id with
// a coordinate suffix.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

// commandTimeout bounds one command's RPCs. A command runs off the render lock,
// so a slow daemon costs the reader a stale status row and never a frozen pane.
const commandTimeout = 10 * time.Second

// commandAsync runs fn off the input goroutine and reports whatever it says
// into the footer. The note is the ONLY feedback channel a command has: the
// pager owns the screen, so a verb cannot print, and a verb that fails silently
// is indistinguishable from one that did nothing.
func (in *interactiveInput) commandAsync(fn func(context.Context) (string, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		msg, err := fn(ctx)
		if err != nil {
			in.noteErr(wireErrorText(err))
			return
		}
		in.note(msg)
	}()
}

// wireErrorText is an error as a reader should see it. A refusal from the
// daemon arrives wrapped as "jsonrpc error -32000: <sentence>", and the code
// is nothing to a person looking at a status row: the sentence is the whole
// of it.
func wireErrorText(err error) string {
	var jerr *jkrpc.Error
	if errors.As(err, &jerr) {
		return strings.Replace(err.Error(), jerr.Error(), jerr.Message, 1)
	}
	return err.Error()
}

// noteErr is note for TROUBLE: same slot, same retirement, red rather than
// gray. The split is here because this is the one place that knows which it
// was -- the verb returned an error or it did not.
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

// noteLocked is note for a caller that already holds the render lock.
func (in *interactiveInput) noteLocked(msg string) { in.lt.tr.setCommandNote(msg) }

// ---------------------------------------------------------------------------
// The verbs.
// ---------------------------------------------------------------------------

// commandSend is `:send [<spec>] -- <text>`. With no spec the text goes to
// the aria on screen, which is the common case and the reason the spec is
// optional. Sent to another aria the transcript does NOT switch: sending is
// not looking, and `:listen <id>` is one more line.
//
// THE PARSER IS THE CLI'S: planSend, which is extractSendFlags and
// extractPrompt, the same functions the shell runs.
func (in *interactiveInput) commandSend(ctx context.Context, args []string) (string, error) {
	plan, err := planSend(args)
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
		// A prompt sent into a busy aria is a queue entry, and the reader who
		// typed it should see it land EVERY TIME. The auto-open is suppressed
		// by any deliberate pit and by any earlier Esc, so a typed send opens
		// the queue the deliberate way instead. `active` is the daemon's
		// answer to "did this join a running turn".
		//
		// THERE IS NO REFRESH HERE ANY MORE. This used to kick refreshQueued()
		// because the queue was polled and the drawer would otherwise show a
		// stale list for up to half a second after the send that filled it.
		// The queue is a intrinsic now: the patch is already on its way over
		// this same connection, and asking would only race it.
		if resp != nil && resp.Active {
			in.mu.Lock()
			in.lt.tr.openQueueFromKey()
			in.mu.Unlock()
		}
		return "sent", nil
	}
	return "sent to " + id, nil
}

// commandFork is `:fork [<spec>] [-S k=v] [-O outfit] [--stay] -- <prompt>`.
//
// IT MEANS WHAT `figaro fork` MEANS, with the transcript standing in for the
// stream: mint the branch, move the shell's binding to it (when the plan
// forked this shell's own aria), submit the prompt, and SHOW the branch as
// its reply streams. `--stay` mints and prompts and stays on the parent,
// attendance untouched, with `:listen <id>` in the status row. There is
// deliberately no verb that shows the branch and leaves attendance alone:
// that is `:fork --stay` then `:listen <id>`, two commands. `-f` is refused:
// the transcript IS the stream, and `--stay` is the thing you mean.
//
// The plan is planFork, the shell's own parser; the fork and the rebind are
// forkVerb, shared with the shell. Showing the branch is still a FULL
// RELOAD of the transcript (plans/transcript-subject.md section 3).
func (in *interactiveInput) commandFork(ctx context.Context, args []string) (string, error) {
	plan, err := planFork(args)
	if err != nil {
		return "", fmt.Errorf("fork: %w", err)
	}
	if plan.opts.forget {
		return "", errors.New("fork: -f is a shell notion (the transcript is the stream); --stay mints and prompts without following")
	}
	if plan.compose {
		return "", errors.New("fork: the prompt must follow `--` (the pager has no composer)")
	}
	if plan.prompt == "" {
		return "", errors.New("fork: the prompt must follow `--` (a bare fork has no reply to show here; use the shell)")
	}
	if plan.spec == "" {
		// The box's implied aria is the one on screen, which the shell's
		// binding may or may not be; naming it keeps forkVerb's "did I fork
		// my own aria" test honest either way.
		plan.spec = in.currentID()
	}
	env, err := in.verbEnv()
	if err != nil {
		return "", fmt.Errorf("fork: %w", err)
	}
	out, err := forkVerb(ctx, env, plan)
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
	if _, _, err := fcli.Qua(ctx, plan.prompt, buildPromptForm()); err != nil {
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
	if err := in.retarget(ctx, branch, ep); err != nil {
		return "", fmt.Errorf("%s, but could not show it: %w", done, err)
	}
	return done, nil
}

// verbEnv is the box's door for the shared verbs: the angelus it holds and
// the shell that started this pager, whose binding attend and fork move.
func (in *interactiveInput) verbEnv() (verbEnv, error) {
	acli, err := in.angelus()
	if err != nil {
		return verbEnv{}, err
	}
	return verbEnv{loaded: in.loaded, acli: acli, shellPID: shellPID}, nil
}

// switchSubject is THE PRIMITIVE the whole command mode exists for: point the
// transcript at a different aria. attend also binds this shell to it, which is
// the only difference between `:listen` and `:attend`; both run the shell's
// verb (listenVerb, attendVerb) and then show what it resolved.
func (in *interactiveInput) switchSubject(ctx context.Context, spec string, attend bool) (string, error) {
	// A SESSION THAT DOES NOT OWN ITS CONNECTION CANNOT CHANGE SUBJECT, yet.
	// `figaro send` dials the aria itself and blocks on that connection's
	// Done(); its notify pump is not fenced by the subject generation either,
	// so a switch there would both end the session and fold the OLD aria's
	// frames into the NEW aria's store. Both are symptoms of one thing: the
	// pager has two front doors and only one of them owns what it shows.
	// Refuse honestly rather than half-work. See plans/transcript-command-mode.md.
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
	if err := in.retarget(ctx, id, ep); err != nil {
		return "", err
	}
	if attend {
		// THE JUMPLIST RECORDS ARRIVALS, wherever the attend came from: the
		// ':' box, 'a' on a fork point, 'a' on a row of `:ls`. A hop has
		// already moved its cursor onto this id, so recording it again is a
		// no-op and ^O keeps meaning "the one before".
		in.mu.Lock()
		in.jumps.visit(id)
		pos, total := in.jumps.where()
		in.mu.Unlock()
		return fmt.Sprintf("attending %s (%d/%d)", id, pos, total), nil
	}
	return "showing " + id, nil
}

// attendFromPager is the 'a' key: the same body `:attend` runs, on the aria
// a fork point or a list row names.
//
// THE HOOK RUNS ON THE DISPATCH PATH, WHICH HOLDS THE RENDER LOCK. Taking
// it here froze the pager dead -- every key after `a` was swallowed, the
// screen kept its last frame, and the session looked like a binding that
// did nothing. The same trap the 'S' hook fell into (see transcript.hooks).
// So everything below happens on the command goroutine.
func (in *interactiveInput) attendFromPager(id string) {
	if id == "" {
		return
	}
	in.commandAsync(func(ctx context.Context) (string, error) {
		// RECORD THE DEPARTURE, or the first ^O has nowhere to go back to:
		// the list would hold only the aria just arrived at. A hop does not
		// come through here; its cursor already names where it is going.
		in.mu.Lock()
		in.jumps.visit(in.figaroID)
		in.mu.Unlock()
		return in.attendNote(ctx, id)
	})
}

// attendSwitch is the attend without the departure: the jumplist's own hop
// uses it, having moved its cursor already.
func (in *interactiveInput) attendSwitch(id string) {
	in.commandAsync(func(ctx context.Context) (string, error) { return in.attendNote(ctx, id) })
}

func (in *interactiveInput) attendNote(ctx context.Context, id string) (string, error) {
	note, err := in.switchSubject(ctx, id, true)
	if err != nil {
		return "", fmt.Errorf("attend: %w", err)
	}
	return note, nil
}

// hopAria is ^O/^I: back and forward through the arias attended in this
// session. Off the dispatch path, for the reason above.
func (in *interactiveInput) hopAria(dir int) { go in.hop(dir) }

// hop moves the cursor first, so the attend that follows is recorded as
// arriving where the cursor already stands.

func (in *interactiveInput) hop(dir int) {
	in.mu.Lock()
	in.jumps.visit(in.figaroID)
	id, ok := in.jumps.hop(dir)
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
	in.attendSwitch(id)
}

// resolve turns a spec into (id, endpoint) through THE SAME resolver `figaro
// listen` uses -- which is what makes `@role` work here for free: it already
// follows target-aria to the bearer.
func (in *interactiveInput) resolve(ctx context.Context, spec string) (string, transport.Endpoint, error) {
	acli, err := in.angelus()
	if err != nil {
		return "", transport.Endpoint{}, err
	}
	return resolveFigaroTargetEndpoint(ctx, in.loaded, acli, spec, false, dressing{})
}

// angelus dials the daemon's own door ON FIRST USE. A session that never types
// a command never opens it, which is why this is lazy rather than a field every
// caller has to remember to fill -- and forgetting to fill it is exactly how
// `:send` came to be dead in half the program.
func (in *interactiveInput) angelus() (*sdk.Angelus, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.acli != nil {
		return in.acli, nil
	}
	if in.loaded == nil {
		return nil, fmt.Errorf("this session has no daemon connection")
	}
	cli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath()))
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

// aria is the current subject's client, read under the lock because a switch
// replaces it.
func (in *interactiveInput) aria() *sdk.Aria {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.subject
}

// ---------------------------------------------------------------------------
// The switch itself.
// ---------------------------------------------------------------------------

// retarget dials an aria and makes it the subject: the ONE path by which this
// process comes to be showing a conversation. `figaro listen` opens through it
// too, so the switch is exercised on every startup rather than only when
// somebody types `:listen` -- a door used once a session is a door that rots.
func (in *interactiveInput) retarget(ctx context.Context, id string, ep transport.Endpoint) error {
	// THE GENERATION IS THE WHOLE SAFETY ARGUMENT. The old connection's notify
	// pump is still live while we dial, and its frames carry the OLD aria's
	// coordinates. Folding one of those into the new client would render one
	// conversation under another's turn ids -- the fabricated-adjacency bug the
	// range store exists to prevent, at aria scale. Every handler checks the
	// generation it was born with before it touches the renderer.
	gen := atomic.AddUint64(&in.subjectGen, 1)

	fcli, err := sdk.DialAriaWith(ep, in.notifyHandler(gen), in.tap)
	if err != nil {
		return fmt.Errorf("connect %s: %w", id, err)
	}

	in.mu.Lock()
	old, ownedOld := in.subject, in.ownsSubject
	in.subject, in.fcli, in.hangup, in.ownsSubject = fcli, fcli, fcli, true
	in.figaroID = id
	in.caughtUp = false
	in.lt.retarget(id, newSessionStatus(id, time.Now()))
	in.lt.setDesync(in.desyncHandler(gen))
	// PITFALL, found in a pty: this was wired inside seedSubject's
	// already-open branch, so on a COLD start (which takes the other branch)
	// the ':' box had no runner and answered "commands need a live session" --
	// the one path every session takes. A hook that is armed on one of two
	// doors is armed on neither.
	in.wireHooks()
	in.mu.Unlock()

	// A NEW SUBJECT HAS DIFFERENT INTRINSIC FORMS. Dropping the mirrors is the same
	// argument as the generation itself: folding the old aria's queue into the
	// new one's drawer is the fabricated-adjacency bug wearing different
	// clothes. Then seed, because a mirror that has never been seeded shows
	// nothing and the bar would sit on whatever it last guessed.
	in.intrinsics.reset(gen)
	in.seedIntrinsics()
	if old != nil && ownedOld {
		old.Close()
	}
	// Watch the new connection, and report its death only while it is ours.
	go func() {
		<-fcli.Done()
		if atomic.LoadUint64(&in.subjectGen) != gen {
			return // superseded: this is the death of a connection we replaced
		}
		select {
		case in.subjectDead <- struct{}{}:
		default:
		}
	}()

	// Seed the pager. enterTranscript is a no-op once the pager is up, so this
	// is the read that fills a window we just emptied.
	in.seedMetrics()
	in.seedSubject()
	return nil
}

// seedMetrics ASKS for what the bar says, because nothing volunteers it.
// Metrics -- the capacity figure AND the mantra -- ride reads and frames, so a
// session that has not read history and has not yet seen a turn (a cold
// `fig listen`, and every `fig form listen`, which reads nothing on purpose)
// had no figure on the bar at all and grew one the moment somebody spoke. The
// same gap made `fig set mantra` from another shell invisible until the next
// turn. It is asked for on connect, and again on the pager's clock.
//
// One backward read of one message, for the metrics attached to it: the page
// is discarded, because seeding the WINDOW is seedSubject's job and this must
// not smuggle a message into it.
func (in *interactiveInput) seedMetrics() {
	cli := in.aria()
	if cli == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		page, err := cli.ReadBefore(ctx, aria.Anchor{}, 1)
		if err != nil || page.Metrics == nil {
			return
		}
		in.mu.Lock()
		in.lt.status.update(*page.Metrics)
		in.lt.render()
		in.mu.Unlock()
	}()
}

// notifyHandler folds one connection's frames, and only while that connection
// is the subject.
func (in *interactiveInput) notifyHandler(gen uint64) sdk.NotifyHandler {
	return func(method string, params json.RawMessage) {
		if atomic.LoadUint64(&in.subjectGen) != gen {
			return
		}
		// A INTRINSIC FORM PATCH IS HANDLED BEFORE THE RENDER LOCK IS TAKEN, not by
		// releasing it in the middle of a dispatch. It takes the lock itself,
		// and may go to the wire to resync; an earlier version unlocked and
		// relocked around it, which worked but left a window inside a handler
		// that reads as if it holds the lock throughout. One entrance, one
		// lock discipline.
		if method == rpc.MethodFormDelta {
			in.applyIntrinsicDelta(params)
			return
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		switch method {
		case rpc.MethodAriaFrame:
			in.turnFrame(params)
		case rpc.MethodTurnDone:
			in.turnDone(params)
		}
	}
}

// desyncHandler re-reads from the highest sealed turn, on the connection that
// asked. A desync raised by a connection we have since left is not ours.
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

// seedSubject fills the pager's window from the new subject's tail.
// wireHooks arms everything the pager asks the input loop to do. ONE PLACE:
// they used to be set at three call sites, and the file carried the scar --
// "a hook that is armed on one of two doors is armed on neither" -- which is
// exactly how `:send` came to answer "commands need a live session" inside a
// `figaro send`, and how S was dead in one entrance while it worked in the
// other. Caller holds the render lock.
func (in *interactiveInput) wireHooks() {
	in.lt.setQueuedFetch(in.refreshQueued) // 'Q' works before the pager is up
	in.lt.setHistoryFetcher(in.historyFetcher())
	in.lt.setCommandRunner(in.runCommand)
	in.lt.setCommandCompleter(in.complete)
	in.lt.setCatchUp(in.pagerCatchUp)
	in.lt.tr.dropRow = in.dropPitRow
	in.lt.tr.attendAria = in.attendFromPager
	in.lt.tr.ariaHop = in.hopAria
	// The hooks the pager calls FROM DISPATCH hand off to a goroutine: that
	// path already holds the render lock, and taking it twice freezes.
	in.lt.tr.openForm = func() { go in.openLive("form show", "", false) }
}

func (in *interactiveInput) seedSubject() {
	in.mu.Lock()
	active := in.lt.transcriptActive()
	inline := in.startInline
	in.mu.Unlock()
	if inline {
		// AN ORDINARY SEND STAYS INLINE. Its window is opened by the preamble
		// it is about to print, not by a read here, and promoting the pager at
		// startup would put every `figaro send` on a screen it did not ask for.
		return
	}
	if !active {
		in.enterTranscript() // the cold door: it reads and opens
		return
	}
	// Already up and now empty: the same read the deliberate door performs.
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	r, rerr := in.fcli.ReadBefore(rctx, aria.Anchor{Turn: recentCursor}, wireBudget(transcriptPageSize))
	rcancel()
	in.mu.Lock()
	defer in.mu.Unlock()
	in.caughtUp = rerr == nil
	if rerr == nil {
		in.lt.apply(r)
		in.lt.setMoreBefore(r.More.Before)
	}
	in.wireHooks()
	in.lt.invalidateTranscriptWindow()
	in.lt.render()
}

// dropPitRow is 'x' in a pit: what dropping means depends on which
// pit. Today only the queue can be dropped from; the switch is here rather
// than in the transcript because every arm of it is an RPC.
func (in *interactiveInput) dropPitRow(name, id string) {
	if name != "queue" {
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
		// A REFUSAL IS A NORMAL ANSWER HERE, not an error: the results carry it,
		// one per requested id. Ignoring them is how `x` came to look like it
		// worked while the next poll put the message straight back.
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
		in.refreshQueued() // the daemon's answer replaces the optimistic removal
	}()
}
