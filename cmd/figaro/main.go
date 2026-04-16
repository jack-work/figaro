// figaro is a minimal CLI coding agent.
//
// Usage:
//
//	figaro -- <prompt>               # prompt (resolved via ppid)
//	figaro new -- <prompt>           # new figaro + prompt
//	figaro context                   # show chat history
//	figaro list                      # list all figaros
//	figaro kill <id>                 # kill a figaro
//	figaro models                    # list available models
//	figaro login <provider>          # OAuth login
//	figaro --angelus                 # (internal) run as supervisor
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jack-work/largo"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/jack-work/hush/managed"
	"golang.org/x/term"
)

func main() {
	// Re-exec guard: if we were spawned by managed.SpawnDaemon to serve
	// as the embedded hush agent, run it and exit immediately.
	if managed.IsAgentChild() {
		if err := managed.RunAgentChild(); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Multi-call binary: if invoked as "q", rewrite args to "figaro --".
	// Create the symlink: ln -s $(which figaro) ~/go/bin/q
	if filepath.Base(os.Args[0]) == "q" {
		os.Args = append([]string{"figaro", "--"}, os.Args[1:]...)
	}

	// Multi-call: "l" → "figaro plain --". Raw, ephemeral, pipe-friendly.
	// Create the symlink: ln -s $(which figaro) ~/go/bin/l
	if filepath.Base(os.Args[0]) == "l" {
		os.Args = append([]string{"figaro", "plain", "--"}, os.Args[1:]...)
	}

	// Internal flag: run as angelus supervisor.
	if len(os.Args) > 1 && os.Args[1] == "--angelus" {
		runAngelus()
		return
	}

	ctx := context.Background()
	loaded := mustLoadConfig()

	// Initialize OpenTelemetry.
	logCfg := loaded.Log()
	traceFile := filepath.Join(filepath.Dir(logCfg.RPCFile), "traces.jsonl")
	shutdown, err := figOtel.Init(ctx, traceFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer shutdown(ctx)
	}

	// Dispatch subcommands.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin(loaded)
			return
		case "rest":
			runRest()
			return
		case "models":
			runModels(loaded)
			return
		case "list":
			runList(loaded)
			return
		case "kill":
			runKill(loaded)
			return
		case "attend":
			runAttend(loaded)
			return
		case "context":
			runContext(loaded)
			return
		case "new":
			prompt := extractPrompt(os.Args[2:])
			if prompt == "" {
				die("usage: figaro new -- <prompt>")
			}
			runNewPrompt(loaded, prompt)
			return
		case "plain":
			prompt := extractPrompt(os.Args[2:])
			if prompt == "" {
				die("usage: figaro plain -- <prompt>")
			}
			runPlainPrompt(loaded, prompt)
			return
		}
	}

	// Default: prompt via existing or new figaro.
	prompt := extractPrompt(os.Args[1:])
	if prompt == "" {
		printUsage()
		os.Exit(1)
	}
	runPrompt(loaded, prompt)
}

// --- Angelus mode (supervisor) ---

func runAngelus() {
	loaded := mustLoadConfig()
	runtimeDir := angelusRuntimeDir()

	// Initialize otel in the angelus process.
	logCfg := loaded.Log()
	traceFile := filepath.Join(filepath.Dir(logCfg.RPCFile), "traces.jsonl")
	otelShutdown, err := figOtel.Init(context.Background(), traceFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer otelShutdown(context.Background())
	}

	logger, logFile := mustOpenLog()
	defer logFile.Close()

	a := angelus.New(angelus.Config{
		RuntimeDir: runtimeDir,
		Logger:     logger,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	handlers := angelus.NewHandlers(angelus.ServerConfig{
		Angelus:         a,
		Config:          loaded,
		ProviderFactory: buildProviderFactory(loaded),
		Ctx:             ctx,
	})
	a.Handlers = handlers.Map

	// Restore persisted arias from disk before accepting connections.
	handlers.RestoreArias(ctx)

	if err := a.Run(ctx); err != nil {
		logger.Fatalf("angelus: %v", err)
	}
}

// --- CLI: prompt (default) ---

func runPrompt(loaded *config.Loaded, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()

	// Resolve existing figaro for this shell.
	resp, err := acli.Resolve(ctx, ppid)
	if err != nil {
		die("resolve: %s", err)
	}

	var figaroID string
	var figaroEP transport.Endpoint

	if resp.Found {
		figaroID = resp.FigaroID
		figaroEP = transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
	} else {
		// Create a new figaro.
		figaroID, figaroEP = mustCreateAndBind(ctx, acli, loaded, ppid)
	}
	_ = figaroID

	// Connect to figaro and prompt.
	mustPromptFigaro(ctx, figaroEP, prompt, loaded.Log().RPCFile)
}

// --- CLI: new ---

func runNewPrompt(loaded *config.Loaded, prompt string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()

	// Unbind existing if any.
	acli.Unbind(ctx, ppid)

	// Create new figaro and bind.
	_, figaroEP := mustCreateAndBind(ctx, acli, loaded, ppid)

	mustPromptFigaro(ctx, figaroEP, prompt, loaded.Log().RPCFile)
}

// --- CLI: plain (raw, ephemeral, pipe-friendly) ---

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

	provName := loaded.Config.DefaultProvider
	model := defaultModel(loaded, provName)

	createResp, err := acli.CreateEphemeral(ctx, provName, model)
	if err != nil {
		die("create figaro: %s", err)
	}
	figaroID := createResp.FigaroID
	figaroEP := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}

	// Always clean up the ephemeral figaro on exit. Uses a detached
	// context so interrupt doesn't prevent the kill.
	defer func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer killCancel()
		_ = acli.Kill(killCtx, figaroID)
	}()

	// Wait for the figaro socket to appear.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(figaroEP.Address); serr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	exitCode := plainPrompt(ctx, figaroEP, prompt)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// plainPrompt streams the assistant's response (and any tool output)
// verbatim to stdout. Returns a process exit code: 0 on clean Done,
// 1 on error, 130 on interrupt.
func plainPrompt(ctx context.Context, ep transport.Endpoint, prompt string) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	doneCh := make(chan struct{}, 1)
	var sawError bool

	out := os.Stdout

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

	if err := fcli.Prompt(ctx, prompt); err != nil {
		fmt.Fprintln(os.Stderr, "error: prompt:", err)
		return 1
	}

	select {
	case <-doneCh:
		if sawError {
			return 1
		}
		return 0
	case <-ctx.Done():
		intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = fcli.Interrupt(intCtx)
		intCancel()
		select {
		case <-doneCh:
		case <-time.After(3 * time.Second):
		}
		return 130
	}
}

// --- CLI: list ---

func runList(loaded *config.Loaded) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resp, err := acli.List(ctx)
	if err != nil {
		die("list: %s", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "\tID\tSTATE\tMODEL\tMSGS\tCONTEXT\tPIDS\n")
	for _, f := range resp.Figaros {
		pids := make([]string, len(f.BoundPIDs))
		for i, p := range f.BoundPIDs {
			pids[i] = fmt.Sprintf("%d", p)
		}
		pidStr := strings.Join(pids, ",")
		if pidStr == "" {
			pidStr = "-"
		}
		ctxStr := fmt.Sprintf("%dk", f.ContextTokens/1000)
		if !f.ContextExact {
			ctxStr = "~" + ctxStr
		}
		current := ""
		if slices.Contains(f.BoundPIDs, os.Getppid()) {
			current = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			current, f.ID, f.State, f.Model, f.MessageCount, ctxStr, pidStr)
	}
	w.Flush()
}

// --- CLI: kill ---

func runKill(loaded *config.Loaded) {
	if len(os.Args) < 3 {
		die("usage: figaro kill <id>")
	}
	figaroID := os.Args[2]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	if err := acli.Kill(ctx, figaroID); err != nil {
		die("kill: %s", err)
	}
	fmt.Fprintf(os.Stderr, "killed %s\n", figaroID)
}

// --- CLI: attend ---

func runAttend(loaded *config.Loaded) {
	if len(os.Args) < 3 {
		die("usage: figaro attend <id>")
	}
	figaroID := os.Args[2]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()

	// Unbind from any existing figaro first.
	acli.Unbind(ctx, ppid)

	if err := acli.Bind(ctx, ppid, figaroID); err != nil {
		die("attend: %s", err)
	}
	fmt.Fprintf(os.Stderr, "attending %s\n", figaroID)
}

// --- CLI: context ---

func runContext(loaded *config.Loaded) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	resp, err := acli.Resolve(ctx, ppid)
	if err != nil {
		die("resolve: %s", err)
	}
	if !resp.Found {
		die("no figaro bound to this shell")
	}

	figaroEP := transport.Endpoint{Scheme: resp.Endpoint.Scheme, Address: resp.Endpoint.Address}
	fcli, err := figaro.DialClient(figaroEP, nil)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	ctxResp, err := fcli.Context(ctx)
	if err != nil {
		die("context: %s", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(ctxResp.Messages)
}

// --- CLI: rest (put the angelus to rest) ---

func runRest() {
	sockPath := angelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if cli, err := angelus.DialClient(ep); err == nil {
		cli.Close()
	} else {
		fmt.Fprintln(os.Stderr, "angelus is not running")
		return
	}

	// Find and signal the angelus process.
	pidBytes, err := os.ReadFile(filepath.Join(angelusRuntimeDir(), "angelus.pid"))
	if err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err == nil {
			syscall.Kill(pid, syscall.SIGTERM)
			fmt.Fprintf(os.Stderr, "angelus (pid %d) put to rest\n", pid)
			return
		}
	}

	// Fallback: just remove the socket so next invocation starts fresh.
	os.Remove(sockPath)
	fmt.Fprintln(os.Stderr, "angelus socket removed; will restart on next invocation")
}

// --- CLI: models (client-side, no angelus) ---

func runModels(loaded *config.Loaded) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	providerNames := loaded.ListProviders()
	if len(providerNames) == 0 {
		providerNames = []string{loaded.Config.DefaultProvider}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "PROVIDER\tMODEL ID\tNAME\n")

	for _, name := range providerNames {
		prov, _ := buildProvider(loaded, name)
		if prov == nil {
			continue
		}
		models, err := prov.Models(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", name, err)
			continue
		}
		for _, m := range models {
			fmt.Fprintf(w, "%s\t%s\t%s\n", m.Provider, m.ID, m.Name)
		}
	}
	w.Flush()
}

// --- CLI: login (client-side, no angelus) ---

func runLogin(loaded *config.Loaded) {
	if len(os.Args) < 3 {
		die("usage: figaro login <provider>")
	}
	providerName := os.Args[2]

	h := mustHush()
	hushClient := h.Client()

	authPath := loaded.ProviderAuthPath(providerName)
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		die("create provider dir: %s", err)
	}

	var oauthCfg auth.OAuthConfig
	switch providerName {
	case "anthropic":
		oauthCfg = auth.AnthropicOAuth
	default:
		die("no OAuth config for provider %q", providerName)
	}

	mgr := auth.NewManager(hushClient, oauthCfg, authPath)
	err := auth.Login(mgr, func() (string, error) {
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		return strings.TrimSpace(line), err
	})
	if err != nil {
		die("%s", err)
	}
}

// --- Angelus connection helpers ---

func angelusRuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "figaro")
	}
	return filepath.Join(os.TempDir(), "figaro")
}

func angelusSocketPath() string {
	return filepath.Join(angelusRuntimeDir(), "angelus.sock")
}

// ensureAngelus starts the angelus if it's not running.
func ensureAngelus() {
	sockPath := angelusSocketPath()
	// Try connecting.
	ep := transport.UnixEndpoint(sockPath)
	if cli, err := angelus.DialClient(ep); err == nil {
		cli.Close()
		return // already running
	}

	// Fork ourselves as the angelus.
	exe, err := os.Executable()
	if err != nil {
		die("find executable: %s", err)
	}

	cmd := exec.Command(exe, "--angelus")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Detach from terminal.
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		die("start angelus: %s", err)
	}

	// Wait for socket to appear.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cli, err := angelus.DialClient(ep); err == nil {
			cli.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	die("angelus did not start within 5 seconds")
}

func mustConnectAngelus(loaded *config.Loaded) *angelus.Client {
	ensureHush()
	ensureAngelus()
	ep := transport.UnixEndpoint(angelusSocketPath())
	cli, err := angelus.DialClient(ep)
	if err != nil {
		die("connect angelus: %s", err)
	}
	return cli
}

func mustCreateAndBind(ctx context.Context, acli *angelus.Client, loaded *config.Loaded, ppid int) (string, transport.Endpoint) {
	provName := loaded.Config.DefaultProvider
	model := defaultModel(loaded, provName)

	createResp, err := acli.Create(ctx, provName, model)
	if err != nil {
		die("create figaro: %s", err)
	}

	if err := acli.Bind(ctx, ppid, createResp.FigaroID); err != nil {
		die("bind: %s", err)
	}

	ep := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}

	// Wait for figaro socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(ep.Address); serr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return createResp.FigaroID, ep
}

func openRPCLog(path string) (*json.Encoder, *os.File) {
	if path == "" {
		return nil, nil
	}
	os.MkdirAll(filepath.Dir(path), 0700)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil
	}
	return json.NewEncoder(f), f
}

func mustPromptFigaro(ctx context.Context, ep transport.Endpoint, prompt string, rpcLogPath string) {
	ctx, span := figOtel.Start(ctx, "cli.prompt")
	defer span.End()

	// Derive a cancellable child so Ctrl+D (stdin EOF) can cancel the same
	// context used for SIGINT. Go's signal.NotifyContext already wires
	// os.Interrupt into ctx above us; we just need to add EOF.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Watch stdin for EOF (Ctrl+D) in the background. When we detect it,
	// cancel the context — same path as Ctrl+C. Only meaningful when
	// stdin is a terminal; non-interactive pipes would EOF immediately
	// and kill the prompt before we ever receive output.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		go func() {
			buf := make([]byte, 256)
			for {
				_, err := os.Stdin.Read(buf)
				if err != nil {
					// EOF (Ctrl+D) or any read error — treat as interrupt.
					cancel()
					return
				}
			}
		}()
	}

	// Open CLI RPC log.
	rpcEnc, rpcFile := openRPCLog(rpcLogPath)
	if rpcFile != nil {
		defer rpcFile.Close()
	}

	doneCh := make(chan struct{}, 1)

	// One largo writer owns the entire output stream. Every event below
	// is expressed as markdown into it; tool stdout is the one exception
	// and goes through Suspend/Resume so it appears verbatim (with its
	// own ANSI colors) instead of being mangled by glamour.
	sw, err := largo.NewWriter(os.Stdout, largo.Options{})
	if err != nil {
		die("largo: %s", err)
	}

	// rawOut is non-nil while a tool call is streaming output through
	// largo's pass-through region.
	var rawOut io.Writer

	// resumeIfSuspended is idempotent: safe to call before any
	// transition that needs largo back in markdown-rendering mode.
	resumeIfSuspended := func() {
		if rawOut == nil {
			return
		}
		_ = sw.Resume()
		rawOut = nil
	}

	deliverEvent := func(method string, params json.RawMessage) {
		// Log to CLI RPC file.
		if rpcEnc != nil {
			rpcEnc.Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  method,
				"params":  json.RawMessage(params),
			})
		}

		switch method {
		case rpc.MethodDelta:
			var p rpc.DeltaParams
			if json.Unmarshal(params, &p) == nil {
				figOtel.Event(ctx, "cli.recv.delta",
					attribute.String("text", p.Text),
				)
				sw.Write([]byte(p.Text))
			}

		case rpc.MethodThinking:
			var p rpc.ThinkingParams
			if json.Unmarshal(params, &p) == nil {
				sw.Write([]byte("\n> *🤔 " + largo.EscapeInline(p.Text) + "*\n\n"))
			}

		case rpc.MethodToolStart:
			var p rpc.ToolStartParams
			if json.Unmarshal(params, &p) == nil {
				header := "\n---\n`▶ " + p.ToolName + "`"
				if detail := toolDetail(p); detail != "" {
					header += " " + largo.InlineCode(detail)
				}
				header += "\n\n"
				sw.Write([]byte(header))
				rawOut = sw.Suspend()
			}

		case rpc.MethodToolOutput:
			var p rpc.ToolOutputParams
			if json.Unmarshal(params, &p) == nil && rawOut != nil {
				rawOut.Write([]byte(p.Chunk))
			}

		case rpc.MethodToolEnd:
			var p rpc.ToolEndParams
			if json.Unmarshal(params, &p) == nil {
				resumeIfSuspended()
				if p.IsError {
					sw.Write([]byte("\n**⚠ error:** " + largo.EscapeInline(p.Result) + "\n\n"))
				}
				sw.Write([]byte("\n---\n\n"))
			}

		case rpc.MethodError:
			// Errors are advisory — print and keep waiting. The agent
			// is responsible for sending Done if the turn cannot
			// continue. Don't terminate the CLI on Error alone.
			var p rpc.ErrorParams
			if json.Unmarshal(params, &p) == nil {
				resumeIfSuspended()
				sw.Write([]byte("\n**❌ error:** " + largo.EscapeInline(p.Message) + "\n\n"))
			}

		case rpc.MethodDone:
			resumeIfSuspended()
			sw.Flush()
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	}

	// Notifications are delivered in wire order — no reordering needed.
	// Our jsonrpc.Client calls OnNotify synchronously on the reader goroutine.
	fcli, err := figaro.DialClient(ep, func(method string, params json.RawMessage) {
		deliverEvent(method, params)
	})
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	if err := fcli.Prompt(ctx, prompt); err != nil {
		die("prompt: %s", err)
	}

	// Wait for stream.done or an interrupt (Ctrl+C / Ctrl+D). No wall-clock
	// timeout — multi-tool sessions can run for minutes; the figaro agent
	// has its own SSE/HTTP timeouts that surface as Error → Done.
	select {
	case <-doneCh:
		fmt.Println()
	case <-ctx.Done():
		// Signal the agent to abort the current turn and wait briefly
		// for its graceful shutdown — stream.error + stream.done will
		// arrive, close the output cleanly, and unblock doneCh.
		fmt.Fprintln(os.Stderr, "\ninterrupting...")
		intCtx, intCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = fcli.Interrupt(intCtx)
		intCancel()

		select {
		case <-doneCh:
		case <-time.After(3 * time.Second):
			// Agent didn't ack in time — bail out anyway.
		}
		resumeIfSuspended()
		sw.Flush()
		fmt.Fprintln(os.Stderr, "interrupted")
	}
}

// toolDetail returns the most useful single-line detail for a tool
// call: the bash command, the file path, etc. Empty if the tool has
// no obvious one-line summary.
func toolDetail(p rpc.ToolStartParams) string {
	switch p.ToolName {
	case "bash":
		if cmd, ok := p.Arguments["command"].(string); ok {
			return cmd
		}
	case "read", "write", "edit":
		if path, ok := p.Arguments["path"].(string); ok {
			return path
		}
	}
	return ""
}

// --- Provider construction (used by angelus and models) ---

func buildProviderFactory(loaded *config.Loaded) angelus.ProviderFactory {
	return func(providerName, model string) (providerPkg.Provider, error) {
		switch providerName {
		case "anthropic":
			var acfg config.AnthropicProvider
			acfg.Model = model
			acfg.MaxTokens = 8192
			if err := loaded.LoadProviderConfig(providerName, &acfg); err != nil {
				return nil, err
			}
			if model != "" {
				acfg.Model = model // override from create request
			}
			authPath := loaded.ProviderAuthPath(providerName)
			h := mustHush()
			return anthropic.New(acfg, authPath, h.Client())
		default:
			return nil, fmt.Errorf("unknown provider: %q", providerName)
		}
	}
}

func buildProvider(loaded *config.Loaded, name string) (providerPkg.Provider, int) {
	switch name {
	case "anthropic":
		var acfg config.AnthropicProvider
		acfg.Model = "claude-sonnet-4-20250514"
		acfg.MaxTokens = 8192
		if err := loaded.LoadProviderConfig(name, &acfg); err != nil {
			return nil, 0
		}
		if loaded.Config.DefaultModel != "" {
			acfg.Model = loaded.Config.DefaultModel
		}
		authPath := loaded.ProviderAuthPath(name)
		h := mustHush()
		p, err := anthropic.New(acfg, authPath, h.Client())
		if err != nil {
			return nil, 0
		}
		return p, acfg.MaxTokens
	default:
		return nil, 0
	}
}

func defaultModel(loaded *config.Loaded, providerName string) string {
	if loaded.Config.DefaultModel != "" {
		return loaded.Config.DefaultModel
	}
	switch providerName {
	case "anthropic":
		var acfg config.AnthropicProvider
		acfg.Model = "claude-sonnet-4-20250514"
		loaded.LoadProviderConfig(providerName, &acfg)
		return acfg.Model
	}
	return ""
}

// --- Helpers ---

func mustLoadConfig() *config.Loaded {
	loaded, err := config.Load(config.DefaultConfigDir())
	if err != nil {
		die("config: %s", err)
	}
	return loaded
}

// hushOnce lazily initializes the managed hush instance. The managed
// package detects a running hush agent (ModeExternal) or starts one
// via the hush CLI or embedded re-exec (ModeEmbedded). This replaces
// direct hush.Client construction and eliminates the "agent not running"
// dead end in non-interactive contexts.
var (
	hushInstance *managed.Hush
	hushOnce     sync.Once
	hushErr      error
)

func mustHush() *managed.Hush {
	hushOnce.Do(func() {
		hushInstance, hushErr = managed.New(managed.Options{
			AppName: "figaro",
		})
	})
	if hushErr != nil {
		die("hush: %s", hushErr)
	}
	return hushInstance
}

// ensureHush initializes hush and starts the agent if needed.
// Must be called from the CLI process (not the angelus) so it can
// prompt for a passphrase on the terminal. After this returns, the
// angelus can use hush without interactive prompts.
func ensureHush() {
	h := mustHush()
	if !h.HasIdentity() {
		fmt.Fprintln(os.Stderr, "No hush identity found. Creating one...")
		fmt.Fprint(os.Stderr, "Passphrase (for encrypting secrets at rest): ")
		passphrase, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			die("read passphrase: %s", err)
		}
		pub, err := h.Init(passphrase)
		if err != nil {
			die("init hush identity: %s", err)
		}
		fmt.Fprintf(os.Stderr, "Identity created. Public key: %s\n", pub)
	}
	if err := h.EnsureReady(); err != nil {
		die("hush: %s", err)
	}
}

func mustOpenLog() (*log.Logger, *os.File) {
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".local", "state", "figaro")
	os.MkdirAll(logDir, 0700)
	logPath := filepath.Join(logDir, "angelus.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		die("open log: %s", err)
	}
	return log.New(f, "angelus: ", log.LstdFlags), f
}

func extractPrompt(args []string) string {
	for i, arg := range args {
		if arg == "--" {
			rest := args[i+1:]
			if len(rest) == 0 {
				return ""
			}
			return strings.Join(rest, " ")
		}
	}
	return ""
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: figaro -- <prompt>")
	fmt.Fprintln(os.Stderr, "       figaro new -- <prompt>")
	fmt.Fprintln(os.Stderr, "       figaro plain -- <prompt>   (raw, ephemeral, pipe-friendly; also 'l')")
	fmt.Fprintln(os.Stderr, "       figaro attend <id>")
	fmt.Fprintln(os.Stderr, "       figaro context [id]")
	fmt.Fprintln(os.Stderr, "       figaro list")
	fmt.Fprintln(os.Stderr, "       figaro kill <id>")
	fmt.Fprintln(os.Stderr, "       figaro models")
	fmt.Fprintln(os.Stderr, "       figaro rest")
	fmt.Fprintln(os.Stderr, "       figaro login <provider>")
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
