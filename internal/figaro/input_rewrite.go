package figaro

// INPUT REWRITES: what a prompt's text passes through on its way in.
//
// Two tokens rewrite a prompt today, and before this file they lived on
// opposite sides of the wire with opposite failure policies: `@key!` was
// expanded on the client, permissively, and an unresolved reference was left
// in the text with nobody told. The quote `<lt.block:a-b>!` has to be resolved
// against the log, which only the daemon holds, and a coordinate that names
// nothing must be refused before it is queued. One place, one policy:
//
//	A TERMINATED TOKEN THAT DOES NOT RESOLVE REFUSES THE MESSAGE.
//
// The terminator is what makes that safe. `@` and `<` are everywhere in real
// text; `@key!` and `<412.0>!` are deliberate, and a reader who typed the
// terminator meant it.
//
// Rewrites run in SubmitPromptFrom, the one door every message uses, before
// the form patch is applied and before anything is queued, so a refusal
// leaves the aria exactly as it was and reaches the caller on the reply it is
// already waiting for.

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// InputView is what a rewrite may see: the aria's board and its log, read
// only, plus the settings that shape the rewrite's output.
type InputView struct {
	AriaID   string
	Form     form.Snapshot
	Log      LogReader
	Settings *config.Loaded // nil-safe: every accessor has a default
}

// LogReader is the one read a rewrite needs. Narrow on purpose: a rewrite
// that wanted to append would be a second write path, which journal.go
// exists to forbid.
type LogReader interface {
	Lookup(figaroLT uint64) (store.Entry[message.Message], bool)
}

// InputRewrite is one named pass over a prompt's text.
type InputRewrite interface {
	Name() string
	// Rewrite returns the text to send in place of text, or an error that is
	// the whole of what the caller sees: one short sentence, no wrapping.
	Rewrite(ctx context.Context, view InputView, text string) (string, error)
}

// quaRewrites is the ordered list a figaro.qua passes through. Form references
// first, then the quote, so a board may hold a coordinate under a key; the
// quote runs last because it prepends text that must not itself be rewritten.
// figaro.set and figaro.cast carry no prose and pass through none.
var quaRewrites = []InputRewrite{formRefs{}, quoteRewrite{}}

// rewriteInput runs the qua list over text. An empty prompt (a form-only
// submission) is not text and is returned untouched.
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
