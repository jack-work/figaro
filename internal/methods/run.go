package methods

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/decide"
)

const Deadline = 1500 * time.Millisecond

type Asker interface {
	Ask(ctx context.Context, src decide.Source, q decide.Question) (rpc.DecideResponse, error)
}

type Verdict string

const (
	Decided   Verdict = "decided"
	Defaulted Verdict = "defaulted"
	Kept      Verdict = "kept"
)

type Outcome struct {
	Method  Method
	Verdict Verdict
	Value   string
	Elapsed time.Duration
	Err     error
}

func Depth(ms []Method) int {
	depth := 0
	for _, m := range ms {
		depth = max(depth, m.Template.Depth())
	}
	return depth
}

func RunAll(ctx context.Context, asker Asker, src decide.Source, ms []Method, inquiries []string) []Outcome {
	out := make([]Outcome, len(ms))
	var wg sync.WaitGroup
	for i, m := range ms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = Run(ctx, asker, src, m, inquiries)
		}()
	}
	wg.Wait()
	return out
}

func Run(ctx context.Context, asker Asker, src decide.Source, m Method, inquiries []string) Outcome {
	ctx, cancel := context.WithTimeout(ctx, Deadline)
	defer cancel()
	started := time.Now()
	value, err := ask(ctx, asker, src, m, inquiries)
	o := Outcome{Method: m, Value: value, Verdict: Decided, Elapsed: time.Since(started), Err: err}
	if err == nil {
		return o
	}
	if m.Default == nil {
		o.Verdict, o.Value = Kept, ""
		return o
	}
	o.Verdict, o.Value = Defaulted, *m.Default
	return o
}

func ask(ctx context.Context, asker Asker, src decide.Source, m Method, inquiries []string) (string, error) {
	type answer struct {
		resp rpc.DecideResponse
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		resp, err := asker.Ask(ctx, src, m.Question(inquiries))
		done <- answer{resp, err}
	}()
	var a answer
	select {
	case a = <-done:
	case <-ctx.Done():
		return "", fmt.Errorf("no answer in %s: %w", Deadline, ctx.Err())
	}
	if a.err != nil {
		return "", a.err
	}
	for _, row := range a.resp.Rows {
		if row.Chosen && hasOption(m.Options, row.Option) {
			return row.Option, nil
		}
	}
	return "", errors.New("the answer chose none of the options")
}
