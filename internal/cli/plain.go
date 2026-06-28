package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

// runPlainPrompt streams raw output from an aria. With no --id, it
// creates an ephemeral aria, streams, and kills it. With --id, it
// scopes to that aria (auto-creating if missing) and leaves it alive.
func runPlainPrompt(loaded *config.Loaded, rawArgs []string) {
	id, rest, err := extractIDFlag(rawArgs)
	if err != nil {
		die("plain: %s", err)
	}
	prompt := extractPrompt(rest)
	if prompt == "" {
		die("usage: figaro plain [--id <id>] -- <prompt>")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	var figaroID string
	var figaroEP transport.Endpoint
	ephemeral := id == ""

	if ephemeral {
		createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, "", nil) })
		if err != nil {
			die("create figaro: %s", err)
		}
		figaroID = createResp.FigaroID
		figaroEP = transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
		defer func() {
			killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer killCancel()
			_ = acli.Kill(killCtx, figaroID, false)
		}()
		waitForSocket(figaroEP.Address, 3*time.Second)
	} else {
		figaroID, figaroEP, err = resolveTargetEndpoint(ctx, loaded, acli, id, true)
		if err != nil {
			die("%s", err)
		}
	}

	prompt = expandAtRefsForEndpoint(ctx, figaroEP, prompt)
	exitCode := plainPrompt(ctx, figaroEP, prompt, os.Stdout)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runExecPrompt asks a figaro for bash, then executes it. With --id
// the prompt is scoped to that aria (auto-created if missing) and the
// aria is left alive afterward; without --id, an ephemeral aria is
// spun up and killed.
func runExecPrompt(loaded *config.Loaded, rawArgs []string) {
	id, rest, err := extractIDFlag(rawArgs)
	if err != nil {
		die("x: %s", err)
	}
	instruction := extractPrompt(rest)
	if instruction == "" {
		die("usage: figaro x [--id <id>] [-n|-y] -- <instruction>")
	}

	dryRun := false
	skipConfirm := false
	for _, a := range rest {
		if a == "--" {
			break
		}
		switch a {
		case "-n", "--dry-run":
			dryRun = true
		case "-y", "--yes":
			skipConfirm = true
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	var figaroID string
	var figaroEP transport.Endpoint
	ephemeral := id == ""

	if ephemeral {
		createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) { return acli.CreateEphemeral(ctx, "", nil) })
		if err != nil {
			die("create figaro: %s", err)
		}
		figaroID = createResp.FigaroID
		figaroEP = transport.Endpoint{Scheme: createResp.Endpoint.Scheme, Address: createResp.Endpoint.Address}
		defer func() {
			killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer killCancel()
			_ = acli.Kill(killCtx, figaroID, false)
		}()
		waitForSocket(figaroEP.Address, 3*time.Second)
	} else {
		figaroID, figaroEP, err = resolveTargetEndpoint(ctx, loaded, acli, id, true)
		if err != nil {
			die("%s", err)
		}
	}
	_ = figaroID

	instruction = expandAtRefsForEndpoint(ctx, figaroEP, instruction)
	prompt := "You will write a bash script. Output ONLY raw bash, " +
		"no markdown fences, no prose, no commentary, no explanations. " +
		"The script will be executed verbatim via `bash -c`. " +
		"Instruction: " + instruction

	var buf bytes.Buffer
	exitCode := plainPrompt(ctx, figaroEP, prompt, &buf)
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	script := stripBashFences(buf.String())
	if strings.TrimSpace(script) == "" {
		die("figaro x: empty script from agent")
	}

	if dryRun {
		fmt.Print(script)
		if !strings.HasSuffix(script, "\n") {
			fmt.Println()
		}
		return
	}

	if !skipConfirm && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "--- figaro x: about to execute ---")
		fmt.Fprintln(os.Stderr, script)
		fmt.Fprintln(os.Stderr, "--- press enter to run, ctrl-c to abort ---")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}

	sh := exec.Command("bash", "-c", script)
	sh.Stdin = os.Stdin
	sh.Stdout = os.Stdout
	sh.Stderr = os.Stderr
	if err := sh.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		die("figaro x: bash: %s", err)
	}
}

// stripBashFences removes markdown code fences.
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

// plainPrompt streams the response and returns an exit code.
func plainPrompt(ctx context.Context, ep transport.Endpoint, prompt string, out io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sink := newPlainSink(out)
	doneCh := sink.doneCh

	fcli, err := figaro.DialClient(ep, sink.handle)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: connect figaro:", err)
		return 1
	}
	defer fcli.Close()

	if err := fcli.Qua(ctx, prompt, buildPromptChalkboard()); err != nil {
		fmt.Fprintln(os.Stderr, "error: prompt:", err)
		return 1
	}

	select {
	case <-doneCh:
		if sink.sawError {
			return 1
		}
		return 0
	case <-fcli.Done():
		fmt.Fprintln(os.Stderr, "error: agent disconnected before turn completed")
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
		return 130
	}
}

// verbatimPrompt dumps the raw wire frames as JSON (one object per line)
// and returns an exit code. No formatting, no delta application — the
// literal protocol stream.
func verbatimPrompt(ctx context.Context, ep transport.Endpoint, prompt string, out io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sink := &verbatimSink{out: out, doneCh: make(chan struct{}, 1)}
	doneCh := sink.doneCh

	fcli, err := figaro.DialClient(ep, sink.handle)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: connect figaro:", err)
		return 1
	}
	defer fcli.Close()

	if err := fcli.Qua(ctx, prompt, buildPromptChalkboard()); err != nil {
		fmt.Fprintln(os.Stderr, "error: prompt:", err)
		return 1
	}

	select {
	case <-doneCh:
		if sink.sawError {
			return 1
		}
		return 0
	case <-fcli.Done():
		fmt.Fprintln(os.Stderr, "error: agent disconnected before turn completed")
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
		return 130
	}
}

// verbatimSink writes every wire notification as a JSON line
// {"method","params"} — the protocol exactly as it arrives, no decoding.
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

// plainSink streams the assistant unit to out as raw text for
// pipes/scripts: it maintains the current unit's node list and writes the
// new tail of its flattened text on each update. The user's prompt unit
// is skipped. Tool nodes contribute their raw output (no widget chrome);
// raw-mode callers (figaro x) prompt the model for plain output anyway.
type plainSink struct {
	out      io.Writer
	client   *aria.Client
	written  string // exactly what's been emitted for the current assistant unit
	doneCh   chan struct{}
	sawError bool
}

func newPlainSink(out io.Writer) *plainSink {
	s := &plainSink{out: out, doneCh: make(chan struct{}, 1), client: aria.NewClient()}
	s.client.OnLive = func(_ int, role string, nodes []livedoc.Node) {
		if role == "assistant" {
			s.emit(plainText(nodes))
		}
	}
	s.client.OnClosed = func(m aria.Message) {
		if m.Role != "assistant" {
			return
		}
		s.emit(plainText(m.Nodes))
		if s.written != "" {
			s.out.Write([]byte("\n")) // terminate the unit's line
		}
		s.written = ""
	}
	return s
}

func (s *plainSink) handle(method string, params json.RawMessage) {
	switch method {
	case rpc.MethodAriaFrame:
		var r aria.AriaRead
		if json.Unmarshal(params, &r) == nil {
			s.client.Apply(r)
		}
	case rpc.MethodTurnDone:
		var d rpc.DoneEntry
		_ = json.Unmarshal(params, &d)
		if strings.HasPrefix(d.Reason, "error:") {
			fmt.Fprintln(os.Stderr, d.Reason)
			s.sawError = true
		}
		select {
		case s.doneCh <- struct{}{}:
		default:
		}
	}
}

// emit writes the assistant unit's new text tail. It only emits when the
// flattened text grows monotonically (the streaming case); a structural
// change that rewrites already-printed text is swallowed rather than
// reprinted — raw mode keeps the streamed copy.
func (s *plainSink) emit(text string) {
	b := strings.TrimRight(text, "\n")
	if strings.HasPrefix(b, s.written) {
		s.out.Write([]byte(b[len(s.written):]))
	}
	s.written = b
}

// plainText flattens a node list to raw text: prose markdown verbatim,
// tool nodes as their streamed output.
func plainText(nodes []livedoc.Node) string {
	var parts []string
	for _, n := range nodes {
		switch n.Type {
		case livedoc.NodeThinking:
			// Thinking is omitted from raw output (it's for pipes/scripts).
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
