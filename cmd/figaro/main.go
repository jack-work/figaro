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
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/agent"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider/anthropic"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	hush "github.com/jack-work/hush/client"
)

// loaded is the global config, initialized once in main.
var loaded *config.Loaded

func main() {
	ctx := context.Background()

	// Initialize config.
	loaded = mustLoadConfig()

	// Initialize OpenTelemetry.
	logCfg := loaded.Log()
	traceFile := filepath.Join(filepath.Dir(logCfg.RPCFile), "traces.jsonl")
	shutdown, err := figOtel.Init(ctx, traceFile)
	if err != nil {
		// Non-fatal — tracing is optional.
		fmt.Fprintf(os.Stderr, "warning: otel init: %s\n", err)
	} else {
		defer shutdown(ctx)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin()
			return
		case "models":
			runModels()
			return
		}
	}

	runPrompt()
}

// --- prompt (default command) ---

func runPrompt() {
	// Parse prompt from args: everything after "--" is the prompt.
	prompt := extractPrompt(os.Args[1:])
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: figaro -- <prompt>")
		fmt.Fprintln(os.Stderr, "       figaro new -- <prompt>")
		fmt.Fprintln(os.Stderr, "       figaro context [id]")
		fmt.Fprintln(os.Stderr, "       figaro list")
		fmt.Fprintln(os.Stderr, "       figaro kill <id>")
		fmt.Fprintln(os.Stderr, "       figaro models")
		fmt.Fprintln(os.Stderr, "       figaro login <provider>")
		os.Exit(1)
	}

	prov, maxTokens := buildProvider(loaded, loaded.Config.DefaultProvider)

	// JSON-RPC log
	logCfg := loaded.Log()
	rpcLog, err := openRPCLog(logCfg.RPCFile)
	if err != nil {
		die("rpc log: %s", err)
	}
	defer rpcLog.Close()
	rpcEnc := json.NewEncoder(rpcLog)

	out := make(chan rpc.Notification, 128)
	done := make(chan struct{})
	go func() {
		for n := range out {
			rpcEnc.Encode(n)
			if n.Method == rpc.MethodDelta {
				if p, ok := n.Params.(rpc.DeltaParams); ok {
					fmt.Print(p.Text)
				}
			}
		}
		close(done)
	}()

	ag := &agent.Agent{
		Store:        store.NewMemStore(),
		Provider:     prov,
		SystemPrompt: "You are a helpful assistant. Be concise.",
		MaxTokens:    maxTokens,
		Out:          out,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ctx, span := figOtel.Start(ctx, "figaro.prompt")
	defer span.End()

	if err := ag.Prompt(ctx, prompt); err != nil {
		close(out)
		<-done
		die("%s", err)
	}

	fmt.Println()
	close(out)
	<-done
}

// --- models subcommand ---

func runModels() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	providerNames := loaded.ListProviders()
	if len(providerNames) == 0 {
		// If no providers directory exists, use the default.
		providerNames = []string{loaded.Config.DefaultProvider}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "PROVIDER\tMODEL ID\tNAME\n")

	for _, name := range providerNames {
		prov, _ := buildProvider(loaded, name)
		if prov == nil {
			fmt.Fprintf(os.Stderr, "warning: skipping unknown provider %q\n", name)
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

// --- login subcommand ---

func runLogin() {
	if len(os.Args) < 3 {
		die("usage: figaro login <provider>")
	}
	providerName := os.Args[2]
	loaded := mustLoadConfig()

	hushClient, err := hush.New()
	if err != nil {
		die("%s", err)
	}

	// Resolve auth path for this provider.
	authPath := loaded.ProviderAuthPath(providerName)

	// Ensure provider directory exists.
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

// --- provider construction ---

// buildProvider loads a named provider's config and constructs it.
func buildProvider(loaded *config.Loaded, name string) (providerPkg.Provider, int) {
	switch name {
	case "anthropic":
		var acfg config.AnthropicProvider
		acfg.Model = "claude-sonnet-4-20250514"
		acfg.MaxTokens = 8192

		if err := loaded.LoadProviderConfig(name, &acfg); err != nil {
			die("config providers/%s: %s", name, err)
		}

		if loaded.Config.DefaultModel != "" {
			acfg.Model = loaded.Config.DefaultModel
		}

		authPath := loaded.ProviderAuthPath(name)
		p, err := anthropic.New(acfg, authPath)
		if err != nil {
			die("%s", err)
		}
		return p, acfg.MaxTokens

	default:
		return nil, 0
	}
}

// --- helpers ---

func mustLoadConfig() *config.Loaded {
	loaded, err := config.Load(config.DefaultConfigDir())
	if err != nil {
		die("config: %s", err)
	}
	return loaded
}

func openRPCLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

// extractPrompt finds "--" in args and joins everything after it as the prompt.
// Returns empty string if "--" is not found.
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

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
