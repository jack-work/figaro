package cli

import (
	"context"
	"github.com/jack-work/figaro/internal/boot"
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

// runListen stays open past turn-done; Ctrl-C interrupts the turn, Ctrl-D only
// disconnects.
func runListen(loaded *config.Loaded, ariaID, recordPath, note string, formPit bool) {
	runListenIntrinsic(loaded, ariaID, recordPath, note, formPit, "")
}

func runListenIntrinsic(loaded *config.Loaded, ariaID, recordPath, note string, formPit bool, intrinsic string) {
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
		// Before the dial: Started is the zero of every frame offset, the
		// catch-up read included.
		rec, err = tape.Create(recordPath, tape.Header{
			Aria:    resolvedID,
			Cols:    term.Width(),
			Rows:    term.Height(),
			Term:    os.Getenv("TERM"),
			Binary:  boot.BuildRevision(),
			Command: strings.Join(os.Args, " "),
			Note:    note,
		})
		if err != nil {
			die("record: %s", err)
		}
		defer func() {
			// To the log: the shell prompt is already back.
			if cerr := rec.Close(); cerr != nil {
				slog.Error("tape close", "err", cerr)
			}
		}()
	}

	tailFigaro(ctx, cancel, figaroEP, resolvedID, loaded,
		tailOpts{acli: acli, tape: rec, formPit: formPit, formIntrinsic: intrinsic})
}

// tailOpts' zero value is `figaro listen` exactly.
type tailOpts struct {
	acli          *sdk.Angelus    // nil leaves the aria-changing command verbs inert (a replay)
	tape          *tape.Writer    // nil records nothing
	end           <-chan struct{} // the caller's own end of stream (a replayed tape); exits cleanly
	startedAt     time.Time       // a replay passes the recording's start; zero is time.Now
	formPit       bool
	formIntrinsic string // "" is the board
}

func tailFigaro(ctx context.Context, cancel context.CancelFunc, ep transport.Endpoint, figaroID string, loaded *config.Loaded, opt tailOpts) {
	ctx, span := figOtel.Start(ctx, "cli.listen")
	defer span.End()
	runSession(ctx, cancel, sessionOpts{
		figaroID: figaroID, ep: ep, loaded: loaded,
		set:  listenSettings(loaded),
		acli: opt.acli, tape: opt.tape, end: opt.end, startedAt: opt.startedAt,
		formPit: opt.formPit, formIntrinsic: opt.formIntrinsic, ownsSubject: true,
		signals: true,
	})
}

func listenSettings(loaded *config.Loaded) renderSettings {
	set := pagerSettings(loaded)
	set.listen = true
	return set
}
