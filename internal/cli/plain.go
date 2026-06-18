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
	"github.com/jack-work/figaro/internal/message"
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
			_ = acli.Kill(killCtx, figaroID)
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
			_ = acli.Kill(killCtx, figaroID)
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

	sink := &plainSink{out: out, printed: map[int]int{}, doneCh: make(chan struct{}, 1)}
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

// plainSink folds the log.* tail into raw text on out: assistant prose
// and tool_result output, skipping the user's own prompt tics. It tracks
// per-block emitted lengths so each full-mode frame writes only the new
// suffix.
type plainSink struct {
	out      io.Writer
	openIdx  uint64
	printed  map[int]int
	doneCh   chan struct{}
	sawError bool
}

func (s *plainSink) handle(method string, params json.RawMessage) {
	switch method {
	case rpc.MethodLogOpen:
		var e rpc.OpenEntry
		if json.Unmarshal(params, &e) == nil {
			s.render(e.Index, e.Message, false)
		}
	case rpc.MethodLogEntry:
		var e rpc.LogEntry
		if json.Unmarshal(params, &e) == nil {
			s.render(e.Index, e.Message, true)
		}
	case rpc.MethodLogAbort:
		s.openIdx = 0
		clear(s.printed)
	case rpc.MethodTurnDone:
		var d rpc.DoneEntry
		_ = json.Unmarshal(params, &d)
		if len(d.Reason) >= 6 && d.Reason[:6] == "error:" {
			fmt.Fprintln(os.Stderr, d.Reason)
			s.sawError = true
		}
		select {
		case s.doneCh <- struct{}{}:
		default:
		}
	}
}

func (s *plainSink) render(idx uint64, msg message.Message, sealed bool) {
	if idx != s.openIdx {
		clear(s.printed)
		s.openIdx = idx
	}
	for i, c := range msg.Content {
		switch c.Type {
		case message.ContentText:
			if msg.Role == message.RoleAssistant {
				s.out.Write([]byte(c.Text[min(s.printed[i], len(c.Text)):]))
				s.printed[i] = len(c.Text)
			}
		case message.ContentToolResult:
			s.out.Write([]byte(c.Text[min(s.printed[i], len(c.Text)):]))
			s.printed[i] = len(c.Text)
		}
	}
	if sealed && idx == s.openIdx {
		s.openIdx = 0
	}
}
