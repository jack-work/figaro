package cli

import (
	"context"
	"github.com/jack-work/figaro/sdk"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/internal/term"
)

// runListen tails an aria with the same renderer the rich send uses,
// minus the Qua call: it catches up to the committed cursor, follows
// live frames, supports Ctrl-T transcript mode, and stays open until
// the user closes it. Ctrl-C still sends figaro.interrupt (just like
// inside a send stream); Ctrl-D disconnects without touching the turn.
// runListen is THE pager. formPit opens the aria's form in the pit, at full
// height, the moment the transcript is up: that -- and nothing else -- is what
// `fig form listen` is.
func runListen(loaded *config.Loaded, ariaID, recordPath, note string, formPit bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resolvedID, figaroEP, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		die("%s", err)
	}

	var rec *tape.Writer
	if recordPath != "" {
		// The header is taken BEFORE the dial so its Started is the zero of
		// every frame offset, including the catch-up read the pager fires on
		// its way up.
		rec, err = tape.Create(recordPath, tape.Header{
			Aria:    resolvedID,
			Cols:    term.Width(),
			Rows:    term.Height(),
			Term:    os.Getenv("TERM"),
			Binary:  buildRevision(),
			Command: strings.Join(os.Args, " "),
			Note:    note,
		})
		if err != nil {
			die("record: %s", err)
		}
		defer func() {
			// TO THE LOG, NOT THE TERMINAL: this runs as the session ends,
			// where a line lands over the shell prompt that is already back.
			if cerr := rec.Close(); cerr != nil {
				slog.Error("tape close", "err", cerr)
			}
		}()
	}

	tailFigaro(ctx, cancel, figaroEP, resolvedID, loaded, tailOpts{acli: acli, tape: rec, formPit: formPit})
}

// tailFigaro is the read-only twin of mustPromptFigaro. It opens the
// same incipit-freeze renderer, catches up from LT 0, then follows live
// frames forever. Ctrl-C -> figaro.interrupt; Ctrl-D -> clean
// disconnect (turn keeps running); Ctrl-T -> transcript pager.
// Returns when the user disconnects, the agent socket dies, or ctx
// is canceled.
// tailOpts are the affordances only a non-interactive caller wants. The zero
// value is `figaro listen` exactly, which is why they are a struct and not
// three more positional parameters: the ordinary path names none of them.
type tailOpts struct {
	// acli is the angelus door, which the SUBJECT needs: resolving `:open <id>`
	// to an endpoint is an angelus read, and `:attend` binds through it. The
	// zero value leaves command mode's aria-changing verbs inert, which is what
	// a replay wants -- a tape has no daemon behind it.
	acli *sdk.Angelus
	// tape records the wire (nil = record nothing).
	tape *tape.Writer
	// end closes when the stream is over by the caller's own reckoning: the
	// end of a replayed tape. It joins the SAME select the turn-over path uses,
	// so the exit is the clean one and not an invented interrupt.
	end <-chan struct{}
	// startedAt overrides the session clock shown in the status row. A replay
	// passes the recording's own start so the same tape paints the same pixels
	// at any hour; live callers leave it zero and get time.Now.
	startedAt time.Time
	// formPit opens the subject's form in the pit, fullscreen, as the session
	// starts. It is the whole of `fig form listen`.
	formPit bool
}

// tailFigaro is the LISTEN entrance: one session, with no prompt in it. It
// follows until the reader leaves. See runsession.go.
func tailFigaro(ctx context.Context, cancel context.CancelFunc, ep transport.Endpoint, figaroID string, loaded *config.Loaded, opt tailOpts) {
	ctx, span := figOtel.Start(ctx, "cli.listen")
	defer span.End()
	runSession(ctx, cancel, sessionOpts{
		figaroID: figaroID, ep: ep, loaded: loaded,
		set:  renderSettings{listen: true}, // listen stays open past turn-done
		acli: opt.acli, tape: opt.tape, end: opt.end, startedAt: opt.startedAt,
		formPit: opt.formPit, ownsSubject: true,
		// Ctrl-C means "interrupt the turn" here as it does in send; a
		// listener has no cancellable context of its own to arrange that.
		signals: true,
	})
}
