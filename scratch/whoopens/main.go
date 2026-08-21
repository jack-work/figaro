// whoopens prints the call stack of every "log opened" during ONE node read.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

type h struct{ n int }

func (x *h) Enabled(context.Context, slog.Level) bool { return true }
func (x *h) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "log opened" {
		return nil
	}
	x.n++
	if x.n > 9 {
		return nil
	}
	var pcs [24]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		f, more := frames.Next()
		if !strings.Contains(f.Function, "figaro") {
			if !more {
				break
			}
			continue
		}
		fn := f.Function[strings.LastIndex(f.Function, "/")+1:]
		out = append(out, fmt.Sprintf("%s:%d", fn, f.Line))
		if !more || len(out) > 7 {
			break
		}
	}
	fmt.Printf("open #%d\n    %s\n", x.n, strings.Join(out, "\n    "))
	return nil
}
func (x *h) WithAttrs([]slog.Attr) slog.Handler { return x }
func (x *h) WithGroup(string) slog.Handler      { return x }

func main() {
	root := os.Args[1]
	b, err := store.NewXwalBackend(root, 2<<20)
	if err != nil {
		panic(err)
	}
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		panic(err)
	}
	fmt.Println("--- who opens logs during Nodes()")
	slog.SetDefault(slog.New(&h{}))
	_ = b.Nodes()
}
