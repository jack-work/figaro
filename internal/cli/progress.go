package cli

import (
	"context"
	"fmt"
)

// A SHARED VERB REPORTS TO ITS CALLER, NOT TO THE TERMINAL. The fork's
// "waiting for a safe boundary", the node-cut adjustment and the role
// redirection are progress, and a verb that writes them to the package's own
// writer paints over a pager that owns the pane. The sink rides the request,
// so a shell gets stderr and the pager gets its status row.
type progressKey struct{}

// withProgress points every verb run under ctx at fn.
func withProgress(ctx context.Context, fn func(string)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

// report sends one line of progress. With no sink on the request it goes to
// stderr, which is what a shell wants and what these lines did before.
func report(ctx context.Context, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if fn, ok := ctx.Value(progressKey{}).(func(string)); ok {
		fn(msg)
		return
	}
	fmt.Fprintln(stderrw, msg)
}
