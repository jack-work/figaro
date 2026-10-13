package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"io"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk/aria"
)

func stripBashFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		} else {
			s = ""
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	}
	return s
}

func plainPrompt(ctx context.Context, ep transport.Endpoint, prompt string, out io.Writer, d dressing) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sink := newPlainSink(out)
	doneCh := sink.doneCh

	fcli, err := sdk.DialAria(ep, sink.handle)
	if err != nil {
		fmt.Fprintln(stderrw, "error: connect figaro:", err)
		return 1
	}
	defer fcli.Close()

	if _, _, err := fcli.Qua(ctx, prompt, buildPromptForm(d)); err != nil {
		if !reportClosure(err, "prompt: %s", err) {
			fmt.Fprintln(stderrw, "error: prompt:", err)
		}
		return 1
	}

	select {
	case <-doneCh:
		if sink.sawError {
			return 1
		}
		return 0
	case <-fcli.Done():
		fmt.Fprintln(stderrw, "error: agent disconnected before turn completed")
		return 1
	case <-ctx.Done():
		intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = fcli.Interrupt(intCtx)
		intCancel()
		select {
		case <-doneCh:
		case <-fcli.Done():
		case <-time.After(3 * time.Second):
		}
		return exitInterrupted
	}
}

// verbatimPrompt writes the wire frames as JSON lines, with no delta application.
func verbatimPrompt(ctx context.Context, ep transport.Endpoint, prompt string, out io.Writer, d dressing) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sink := &verbatimSink{out: out, doneCh: make(chan struct{}, 1)}
	doneCh := sink.doneCh

	fcli, err := sdk.DialAria(ep, sink.handle)
	if err != nil {
		fmt.Fprintln(stderrw, "error: connect figaro:", err)
		return 1
	}
	defer fcli.Close()

	if _, _, err := fcli.Qua(ctx, prompt, buildPromptForm(d)); err != nil {
		if !reportClosure(err, "prompt: %s", err) {
			fmt.Fprintln(stderrw, "error: prompt:", err)
		}
		return 1
	}

	select {
	case <-doneCh:
		if sink.sawError {
			return 1
		}
		return 0
	case <-fcli.Done():
		fmt.Fprintln(stderrw, "error: agent disconnected before turn completed")
		return 1
	case <-ctx.Done():
		intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = fcli.Interrupt(intCtx)
		intCancel()
		select {
		case <-doneCh:
		case <-fcli.Done():
		case <-time.After(3 * time.Second):
		}
		return exitInterrupted
	}
}

type verbatimSink struct {
	out      io.Writer
	doneCh   chan struct{}
	sawError bool
}

func (s *verbatimSink) handle(method string, params json.RawMessage) {
	line, err := json.Marshal(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}{method, params})
	if err == nil {
		s.out.Write(line)
		s.out.Write([]byte("\n"))
	}
	if method == rpc.MethodTurnDone {
		var d rpc.DoneEntry
		_ = json.Unmarshal(params, &d)
		if strings.HasPrefix(d.Reason, "error:") {
			s.sawError = true
		}
		select {
		case s.doneCh <- struct{}{}:
		default:
		}
	}
}

// plainSink writes the new tail of the assistant unit's flattened text on each update.
type plainSink struct {
	out      io.Writer
	client   *aria.Client
	written  string
	doneCh   chan struct{}
	sawError bool
}

func newPlainSink(out io.Writer) *plainSink {
	s := &plainSink{out: out, doneCh: make(chan struct{}, 1), client: aria.NewClient()}
	s.client.OnLive = func(m aria.Message) {
		if m.Role == livedoc.RoleOutput {
			s.emit(plainText(m.Nodes))
		}
	}
	s.client.OnClosed = func(m aria.Message) {
		if m.Role != livedoc.RoleOutput {
			return
		}
		s.emit(plainText(m.Nodes))
		if s.written != "" {
			s.out.Write([]byte("\n"))
		}
		s.written = ""
	}
	return s
}

func (s *plainSink) handle(method string, params json.RawMessage) {
	switch method {
	case rpc.MethodAriaFrame:
		var r aria.Page
		if json.Unmarshal(params, &r) == nil {
			s.client.Apply(r, aria.Notify)
		}
	case rpc.MethodTurnDone:
		var d rpc.DoneEntry
		_ = json.Unmarshal(params, &d)
		if strings.HasPrefix(d.Reason, "error:") {
			fmt.Fprintln(stderrw, d.Reason)
			s.sawError = true
		}
		select {
		case s.doneCh <- struct{}{}:
		default:
		}
	}
}

// emit writes only monotonic growth; a rewrite of printed text is swallowed, not
// reprinted.
func (s *plainSink) emit(text string) {
	b := strings.TrimRight(text, "\n")
	if strings.HasPrefix(b, s.written) {
		s.out.Write([]byte(b[len(s.written):]))
	}
	s.written = b
}

func plainText(nodes []livedoc.Node) string {
	var parts []string
	for _, n := range nodes {
		switch n.Type {
		case livedoc.NodeThinking:
			// Omitted from raw output.
		case livedoc.NodeTool:
			if strings.TrimSpace(n.Output) != "" {
				parts = append(parts, n.Output)
			}
		default:
			if strings.TrimSpace(n.Markdown) != "" {
				parts = append(parts, n.Markdown)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
