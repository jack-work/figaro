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
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/creachadair/jrpc2"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	"github.com/jack-work/figaro/internal/rpc"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/transport"
	hush "github.com/jack-work/hush/client"
)

func main() {
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
		case "models":
			runModels(loaded)
			return
		case "list":
			runList(loaded)
			return
		case "kill":
			runKill(loaded)
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

	a.Handlers = angelus.NewHandlerMap(angelus.ServerConfig{
		Angelus:         a,
		Config:          loaded,
		ProviderFactory: buildProviderFactory(loaded),
		Ctx:             ctx,
	})

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
	fmt.Fprintf(w, "ID\tSTATE\tMODEL\tMESSAGES\tPIDS\n")
	for _, f := range resp.Figaros {
		pids := make([]string, len(f.BoundPIDs))
		for i, p := range f.BoundPIDs {
			pids[i] = fmt.Sprintf("%d", p)
		}
		pidStr := strings.Join(pids, ",")
		if pidStr == "" {
			pidStr = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
			f.ID, f.State, f.Model, f.MessageCount, pidStr)
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

	hushClient, err := hush.New()
	if err != nil {
		die("%s", err)
	}

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
	err = auth.Login(mgr, func() (string, error) {
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

	// Open CLI RPC log.
	rpcEnc, rpcFile := openRPCLog(rpcLogPath)
	if rpcFile != nil {
		defer rpcFile.Close()
	}

	doneCh := make(chan struct{}, 1)

	// jrpc2's client dispatches OnNotify from concurrent goroutines,
	// which reorders notifications. The figaro stamps each notification
	// with a monotonic sequence number inside a figaro.event envelope.
	// We buffer out-of-order events and deliver them in sequence.
	type pendingEvent struct {
		method string
		params json.RawMessage
	}

	var (
		reorderMu   sync.Mutex
		nextSeq     uint64 = 1
		reorderBuf  = make(map[uint64]pendingEvent)
	)

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
				fmt.Print(p.Text)
			}
		case rpc.MethodThinking:
			var p rpc.ThinkingParams
			if json.Unmarshal(params, &p) == nil {
				fmt.Fprintf(os.Stderr, "thinking: %s\n", p.Text)
			}
		case rpc.MethodToolStart:
			var p rpc.ToolStartParams
			if json.Unmarshal(params, &p) == nil {
				fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", p.ToolName)
			}
		case rpc.MethodToolOutput:
			var p rpc.ToolOutputParams
			if json.Unmarshal(params, &p) == nil {
				fmt.Fprint(os.Stderr, p.Chunk)
			}
		case rpc.MethodToolEnd:
			var p rpc.ToolEndParams
			if json.Unmarshal(params, &p) == nil {
				if p.IsError {
					fmt.Fprintf(os.Stderr, "[tool error: %s]\n", p.Result)
				} else {
					// Truncate long results for display.
					result := p.Result
					if len(result) > 500 {
						result = result[:500] + "\n... (truncated)"
					}
					fmt.Fprintf(os.Stderr, "%s\n", result)
				}
			}
		case rpc.MethodError:
			var p rpc.ErrorParams
			if json.Unmarshal(params, &p) == nil {
				fmt.Fprintf(os.Stderr, "error: %s\n", p.Message)
			}
		case rpc.MethodDone:
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	}

	fcli, err := figaro.DialClient(ep, func(method string, req *jrpc2.Request) {
		// All notifications arrive as figaro.event envelopes with a seq number.
		if method != rpc.MethodEvent {
			return
		}

		// Parse the envelope. Params is a JSON object with seq, method, params.
		var raw json.RawMessage
		req.UnmarshalParams(&raw)

		var envelope struct {
			Seq    uint64          `json:"seq"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return
		}

		reorderMu.Lock()
		defer reorderMu.Unlock()

		// Buffer this event.
		reorderBuf[envelope.Seq] = pendingEvent{
			method: envelope.Method,
			params: envelope.Params,
		}

		// Deliver all consecutive events starting from nextSeq.
		for {
			evt, ok := reorderBuf[nextSeq]
			if !ok {
				break
			}
			delete(reorderBuf, nextSeq)
			nextSeq++
			deliverEvent(evt.method, evt.params)
		}
	})
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	if err := fcli.Prompt(ctx, prompt); err != nil {
		die("prompt: %s", err)
	}

	// Wait for stream.done or context cancellation.
	select {
	case <-doneCh:
		fmt.Println()
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "\ninterrupted")
	case <-time.After(120 * time.Second):
		die("timeout waiting for response")
	}
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
			return anthropic.New(acfg, authPath)
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
		p, err := anthropic.New(acfg, authPath)
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
	fmt.Fprintln(os.Stderr, "       figaro context [id]")
	fmt.Fprintln(os.Stderr, "       figaro list")
	fmt.Fprintln(os.Stderr, "       figaro kill <id>")
	fmt.Fprintln(os.Stderr, "       figaro models")
	fmt.Fprintln(os.Stderr, "       figaro login <provider>")
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
