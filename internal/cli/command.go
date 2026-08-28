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
// what is on screen. So `:open` is `figaro listen` -- look at it, do not bind to
// it -- and `:attend` is `figaro attend` AND a listen, because attending an aria
// you cannot see is not a thing a reader of this pager ever means.
//
//	:open <spec>     look at another aria; attendance is untouched
//	:listen <spec>   the same verb under the shell's name for it
//	:attend <spec>   bind this shell to it, AND look at it
//	:at <spec>       the same, abbreviated
//	:send [<spec>] -- <text>   send; no spec means the aria on screen
//
// A <spec> is anything the CLI takes: an aria id, an `@form` role (resolved
// through target-aria by the same resolver `figaro listen` uses), or an id with
// a coordinate suffix.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/sdk"
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
			in.noteErr(err.Error())
			return
		}
		in.note(msg)
	}()
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

// commandSend is `:send [<spec>] -- <text>`. With no spec the text goes to the
// aria on screen, which is the common case and the reason the spec is optional.
//
// THE PARSER IS THE CLI'S. extractPrompt owns what `--` means, so a prompt
// typed here and the same prompt typed at a shell are cut the same way.
func (in *interactiveInput) commandSend(ctx context.Context, fields []string) (string, error) {
	prompt := extractPrompt(fields)
	if prompt == "" {
		return "", fmt.Errorf("send: the prompt must follow `--`")
	}
	// Anything before the boundary is a target spec.
	spec := ""
	for _, f := range fields {
		if f == "--" {
			break
		}
		spec = f
	}
	if spec == "" {
		// The aria on screen. One RPC on the connection we already hold.
		if _, _, err := in.aria().Qua(ctx, prompt, buildPromptForm()); err != nil {
			return "", fmt.Errorf("send: %w", err)
		}
		return "sent", nil
	}
	id, ep, err := in.resolve(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	fcli, err := sdk.DialAria(ep, nil)
	if err != nil {
		return "", fmt.Errorf("send: connect %s: %w", id, err)
	}
	defer fcli.Close()
	if _, _, err := fcli.Qua(ctx, prompt, buildPromptForm()); err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	return "sent to " + id, nil
}

// switchSubject is THE PRIMITIVE the whole command mode exists for: point the
// transcript at a different aria. attend also binds this shell to it, which is
// the only difference between `:open` and `:attend`.
func (in *interactiveInput) switchSubject(ctx context.Context, spec string, attend bool) (string, error) {
	if spec == "" {
		return "", fmt.Errorf("which aria? (:open <id|@role>)")
	}
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
	id, ep, err := in.resolve(ctx, spec)
	if err != nil {
		return "", err
	}
	if id == in.currentID() {
		return "already showing " + id, nil
	}
	if attend {
		acli, aerr := in.angelus()
		if aerr != nil {
			return "", aerr
		}
		if err := bindBinding(ctx, acli, shellPID, id, 0); err != nil {
			return "", fmt.Errorf("attend %s: %w", id, err)
		}
	}
	if err := in.retarget(ctx, id, ep); err != nil {
		return "", err
	}
	if attend {
		return "attending " + id, nil
	}
	return "showing " + id, nil
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
// somebody types `:open` -- a door used once a session is a door that rots.
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
	in.lt.setCommandRunner(in.runCommand)
	in.lt.setCommandCompleter(in.complete)
	in.lt.tr.dropRow = in.dropDrawerRow
	in.mu.Unlock()

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
	in.seedSubject()
	return nil
}

// notifyHandler folds one connection's frames, and only while that connection
// is the subject.
func (in *interactiveInput) notifyHandler(gen uint64) sdk.NotifyHandler {
	return func(method string, params json.RawMessage) {
		if atomic.LoadUint64(&in.subjectGen) != gen {
			return
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		switch method {
		case rpc.MethodAriaFrame:
			var r aria.Page
			if json.Unmarshal(params, &r) == nil {
				in.lt.apply(r)
			}
		case rpc.MethodTurnDone:
			var d rpc.DoneEntry
			_ = json.Unmarshal(params, &d)
			in.lt.finishTurn(d.Reason)
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
			r, rerr := cli.Read(rctx, sinceLT)
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
func (in *interactiveInput) seedSubject() {
	in.mu.Lock()
	active := in.lt.transcriptActive()
	in.mu.Unlock()
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
	in.lt.setQueuedFetch(in.refreshQueued)
	in.lt.setHistoryFetcher(in.historyFetcher())
	in.lt.setCommandRunner(in.runCommand)
	in.lt.setCommandCompleter(in.complete)
	in.lt.tr.dropRow = in.dropDrawerRow
	in.lt.invalidateTranscriptWindow()
	in.lt.render()
}

// dropDrawerRow is 'x' in a drawer: what dropping means depends on which
// drawer. Today only the queue can be dropped from; the switch is here rather
// than in the transcript because every arm of it is an RPC.
func (in *interactiveInput) dropDrawerRow(name, id string) {
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
