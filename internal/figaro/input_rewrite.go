package figaro

// Input rewrites run in SubmitPromptFrom before anything is queued or patched:
// a terminated token that does not resolve refuses the message.

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// InputView is read-only: the board, the log, and the settings.
type InputView struct {
	AriaID   string
	Form     form.Snapshot
	Log      LogReader
	Settings *config.Loaded // nil-safe: every accessor has a default
}

// LogReader is narrow on purpose: a rewrite that appended would be a second write path.
type LogReader interface {
	Lookup(figaroLT uint64) (store.Entry[message.Message], bool)
}

type InputRewrite interface {
	Name() string
	// Rewrite's error is the whole of what the caller sees: one short sentence.
	Rewrite(ctx context.Context, view InputView, text string) (string, error)
}

// Form references run first, the quote last: it prepends text that must not itself be rewritten.
var quaRewrites = []InputRewrite{formRefs{}, quoteRewrite{}}

// rewriteInput returns an empty prompt untouched.
func (a *Agent) rewriteInput(ctx context.Context, text string) (string, error) {
	if text == "" {
		return text, nil
	}
	view := InputView{AriaID: a.id, Log: a.figLog, Settings: a.settings}
	if a.form != nil {
		view.Form = a.form.Snapshot()
	}
	for _, rw := range quaRewrites {
		out, err := rw.Rewrite(ctx, view, text)
		if err != nil {
			return "", fmt.Errorf("%s: %w", rw.Name(), err)
		}
		text = out
	}
	return text, nil
}
