package cli

import (
	"context"
	"fmt"
)

// Progress rides the request: a verb that wrote to stderr would paint over a pager
// owning the pane.
type progressKey struct{}

func withProgress(ctx context.Context, fn func(string)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

func report(ctx context.Context, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if fn, ok := ctx.Value(progressKey{}).(func(string)); ok {
		fn(msg)
		return
	}
	fmt.Fprintln(stderrw, msg)
}
