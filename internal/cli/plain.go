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
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
	"golang.org/x/term"
)

// runPlainPrompt creates a fresh ephemeral figaro, streams the
// response verbatim to stdout, and kills the figaro on exit. No
// PID binding, no aria file, no ANSI, no markdown rendering, no
// tool decorations. Tool output is still streamed raw so that
// flows like `l 'run: ls' | sort` work naturally.
func runPlainPrompt(loaded *config.Loaded, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	createResp, err := acli.CreateEphemeral(ctx, "", nil)
	if err != nil {
		die("create figaro: %s", err)
	}
	figaroID := createResp.FigaroID
	figaroEP := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}

	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer killCancel()
		_ = acli.Kill(killCtx, figaroID)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(figaroEP.Address); serr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	exitCode := plainPrompt(ctx, figaroEP, prompt, os.Stdout)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runExecPrompt asks an ephemeral figaro to emit bash for the given
// instruction, then executes the captured bash locally via `bash -c`.
//
// Flags (before --):
//
//	-n / --dry-run   print the bash to stdout instead of executing
//	-y / --yes       skip the confirmation prompt
func runExecPrompt(loaded *config.Loaded, rawArgs []string, instruction string) {
	dryRun := false
	skipConfirm := false
	for _, a := range rawArgs {
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

	createResp, err := acli.CreateEphemeral(ctx, "", nil)
	if err != nil {
		die("create figaro: %s", err)
	}
	figaroID := createResp.FigaroID
	figaroEP := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer killCancel()
		_ = acli.Kill(killCtx, figaroID)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(figaroEP.Address); serr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

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

// stripBashFences removes a leading ```bash / ``` fence and trailing ```
// if the model emits them despite instructions to the contrary.
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

// plainPrompt streams the assistant's response (and any tool output)
// to the given writer. Returns a process exit code: 0 on clean Done,
// 1 on error, 130 on interrupt.
func plainPrompt(ctx context.Context, ep transport.Endpoint, prompt string, out io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	doneCh := make(chan struct{}, 1)
	var sawError bool

	deliverEvent := func(method string, params json.RawMessage) {
		switch method {
		case rpc.MethodDelta:
			var p rpc.DeltaParams
			if json.Unmarshal(params, &p) == nil {
				out.Write([]byte(p.Text))
			}
		case rpc.MethodToolOutput:
			var p rpc.ToolOutputParams
			if json.Unmarshal(params, &p) == nil {
				out.Write([]byte(p.Chunk))
			}
		case rpc.MethodError:
			var p rpc.ErrorParams
			if json.Unmarshal(params, &p) == nil {
				fmt.Fprintln(os.Stderr, "error:", p.Message)
				sawError = true
			}
		case rpc.MethodDone:
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	}

	fcli, err := figaro.DialClient(ep, deliverEvent)
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
		if sawError {
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
