package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/jack-work/largo"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/pacer"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// recordWireTrace appends one NDJSON line per wire event. Best-effort;
// opt-in via FIGARO_WIRE_TRACE. Failures are silent.
func recordWireTrace(w io.Writer, method string, params json.RawMessage) {
	var peek struct {
		Index uint64 `json:"index"`
	}
	_ = json.Unmarshal(params, &peek)
	rec := map[string]interface{}{"method": method, "index": peek.Index}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

// streamRenderer renders the live tail of an aria to the terminal under
// the log.* model. It holds at most one open message and rerenders it as
// log.open frames arrive, finalizing on the sealing log.entry. The
// respec shrinks the client's job to "render the tail": there is no
// solo/batch tool choreography — tool calls and their output are just
// blocks of the open assistant / tool_result messages.
//
// Threading: Handle runs on the single JSON-RPC notify goroutine. The
// pacer drainer is a separate goroutine writing to sw on a ticker; every
// write to sw goes through writeMu (the pacer via pacedWriter, the
// renderer's raw path inline) so a Write can't race a Suspend.
type streamRenderer struct {
	ctx context.Context

	writeMu    sync.Mutex
	suspendBuf []byte

	sw     *largo.Writer
	pace   *pacer.Pacer
	rawOut io.Writer

	// Open-tail render state. printed tracks how many bytes of each
	// block's body have already been emitted, so each log.open emits
	// only the new suffix; announced marks tool_invoke headers already
	// printed.
	openIdx   uint64
	openRole  message.Role
	printed   map[int]int
	announced map[int]bool

	done      chan struct{}
	wireTrace io.Writer
}

func newStreamRenderer(ctx context.Context, sw *largo.Writer) *streamRenderer {
	r := &streamRenderer{
		ctx:       ctx,
		sw:        sw,
		printed:   map[int]int{},
		announced: map[int]bool{},
		done:      make(chan struct{}, 1),
	}
	if path := os.Getenv("FIGARO_WIRE_TRACE"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			r.wireTrace = f
		} else {
			slog.Warn("FIGARO_WIRE_TRACE open failed", "path", path, "err", err)
		}
	}
	return r
}

// SetPacer wires the pacer in. It must be built against r.PacedOut() so
// its drainer writes go through writeMu.
func (r *streamRenderer) SetPacer(p *pacer.Pacer) { r.pace = p }

// pacedWriter serializes pacer-drainer writes against Suspend/Resume.
type pacedWriter struct{ r *streamRenderer }

func (pw *pacedWriter) Write(p []byte) (int, error) {
	pw.r.writeMu.Lock()
	defer pw.r.writeMu.Unlock()
	if pw.r.rawOut != nil {
		// sw is suspended; buffer until resume so post-suspend deltas
		// aren't dropped.
		pw.r.suspendBuf = append(pw.r.suspendBuf, p...)
		return len(p), nil
	}
	return pw.r.sw.Write(p)
}

func (r *streamRenderer) flushSuspendBufLocked() {
	if len(r.suspendBuf) == 0 {
		return
	}
	buf := r.suspendBuf
	r.suspendBuf = nil
	_, _ = r.sw.Write(buf)
}

// PacedOut returns the writer the pacer must be constructed against.
func (r *streamRenderer) PacedOut() io.Writer { return &pacedWriter{r: r} }

// lockedFlush flushes sw under writeMu (shutdown paths).
func (r *streamRenderer) lockedFlush() {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.sw.Flush()
}

// Done closes when turn.done has fired.
func (r *streamRenderer) Done() <-chan struct{} { return r.done }

// suspendIfNeeded switches stdout from markdown to raw mode and returns
// the raw writer.
func (r *streamRenderer) suspendIfNeeded() io.Writer {
	if r.rawOut != nil {
		return r.rawOut
	}
	r.pace.Flush() // drain queued chars before grabbing writeMu
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.sw.Flush()
	r.rawOut = r.sw.Suspend()
	return r.rawOut
}

// resumeIfSuspended hands stdout back to largo/markdown.
func (r *streamRenderer) resumeIfSuspended() {
	if r.rawOut == nil {
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_ = r.sw.Resume()
	r.rawOut = nil
	r.flushSuspendBufLocked()
}

// Handle dispatches one wire notification.
func (r *streamRenderer) Handle(method string, params json.RawMessage) {
	slog.Debug("rpc recv", "method", method, "params", json.RawMessage(params))
	if r.wireTrace != nil {
		recordWireTrace(r.wireTrace, method, params)
	}

	switch method {
	case rpc.MethodLogOpen:
		var e rpc.OpenEntry
		if json.Unmarshal(params, &e) == nil {
			r.renderOpen(e.Index, e.Message)
		}
	case rpc.MethodLogEntry:
		var e rpc.LogEntry
		if json.Unmarshal(params, &e) == nil {
			r.renderSealed(e.Index, e.Message)
		}
	case rpc.MethodLogAbort:
		var e rpc.AbortEntry
		if json.Unmarshal(params, &e) == nil {
			r.dropOpen(e.Index, e.Reason)
		}
	case rpc.MethodTurnDone:
		var d rpc.DoneEntry
		_ = json.Unmarshal(params, &d)
		r.finish(d.Reason)
	}
}

// renderOpen renders the current state of the open tail, emitting only
// what is new since the last frame.
func (r *streamRenderer) renderOpen(idx uint64, msg message.Message) {
	if idx != r.openIdx {
		r.startMessage(idx, msg.Role)
	}
	r.renderBlocks(msg.Content)
}

// renderSealed finalizes a message. If it seals the open tail, flush the
// remainder and close it; otherwise render it whole (catch-up / a
// message that never streamed), skipping the user's own prompt tics.
func (r *streamRenderer) renderSealed(idx uint64, msg message.Message) {
	if idx == r.openIdx {
		r.renderBlocks(msg.Content)
		r.endMessage()
		return
	}
	switch msg.Role {
	case message.RoleAssistant:
		r.startMessage(idx, msg.Role)
		r.renderBlocks(msg.Content)
		r.endMessage()
	case message.RoleUser:
		if hasToolResult(msg.Content) {
			r.startMessage(idx, msg.Role)
			r.renderBlocks(msg.Content)
			r.endMessage()
		}
		// Otherwise it's the user's prompt or a state-only patch tic —
		// the user typed it; don't echo it back.
	}
}

func (r *streamRenderer) startMessage(idx uint64, role message.Role) {
	r.openIdx = idx
	r.openRole = role
	for k := range r.printed {
		delete(r.printed, k)
	}
	for k := range r.announced {
		delete(r.announced, k)
	}
}

// renderBlocks emits the new suffix of each block since the last frame.
func (r *streamRenderer) renderBlocks(content []message.Content) {
	for i, c := range content {
		switch c.Type {
		case message.ContentText:
			r.emitText(c.Text[min(r.printed[i], len(c.Text)):])
			r.printed[i] = len(c.Text)
		case message.ContentThinking:
			r.emitRaw(term.Dim(c.Text[min(r.printed[i], len(c.Text)):]))
			r.printed[i] = len(c.Text)
		case message.ContentToolInvoke:
			if !r.announced[i] {
				r.emitRaw("\n" + term.Dim("● "+toolHeader(c)) + "\n")
				r.announced[i] = true
			}
		case message.ContentToolResult:
			suffix := c.Text[min(r.printed[i], len(c.Text)):]
			if suffix != "" {
				r.emitRaw(term.Dim(suffix))
			}
			r.printed[i] = len(c.Text)
		}
	}
}

func (r *streamRenderer) emitText(s string) {
	if s == "" {
		return
	}
	r.resumeIfSuspended()
	r.pace.Push(s)
}

func (r *streamRenderer) emitRaw(s string) {
	if s == "" {
		return
	}
	w := r.suspendIfNeeded()
	io.WriteString(w, s)
}

func (r *streamRenderer) endMessage() {
	r.openIdx = 0
	r.openRole = ""
}

func (r *streamRenderer) dropOpen(idx uint64, reason string) {
	if idx != r.openIdx {
		return
	}
	r.emitRaw("\n" + term.Dim("(interrupted: "+reason+")") + "\n")
	r.endMessage()
}

func (r *streamRenderer) finish(reason string) {
	r.pace.Flush()
	r.resumeIfSuspended()
	r.lockedFlush()
	if reason != "" && len(reason) >= 6 && reason[:6] == "error:" {
		fmt.Fprintln(os.Stderr, "\n"+reason)
	}
	select {
	case r.done <- struct{}{}:
	default:
	}
}

// hasToolResult reports whether any block is a tool_result.
func hasToolResult(content []message.Content) bool {
	for _, c := range content {
		if c.Type == message.ContentToolResult {
			return true
		}
	}
	return false
}

// toolHeader renders a one-line tool-call summary.
func toolHeader(c message.Content) string {
	if d := toolDetail(c.ToolName, c.Arguments); d != "" {
		return fmt.Sprintf("%s(%s)", c.ToolName, d)
	}
	return c.ToolName
}

// toolDetail pulls the displayable argument from a decoded tool call.
func toolDetail(name string, args map[string]interface{}) string {
	switch name {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "read", "write", "edit":
		if path, ok := args["path"].(string); ok {
			return path
		}
	}
	return ""
}
