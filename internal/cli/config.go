package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	"github.com/jack-work/hush/managed"
	"golang.org/x/term"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
)

func mustLoadConfig() *config.Loaded {
	loaded, err := config.Load(config.DefaultConfigDir())
	if err != nil {
		die("config: %s", err)
	}
	return loaded
}

// hushOnce lazily initializes the managed hush instance. The managed
// package detects a running hush agent (ModeExternal) or starts one
// via the hush CLI or embedded re-exec (ModeEmbedded).
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
// prompt for a passphrase on the terminal.
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

// buildPromptChalkboard collects the per-prompt chalkboard values the
// CLI surfaces to its figaro: cwd and datetime (hour-precision).
func buildPromptChalkboard() *rpc.ChalkboardInput {
	cwd, _ := os.Getwd()
	snap := map[string]json.RawMessage{}
	if cwd != "" {
		if b, err := json.Marshal(cwd); err == nil {
			snap["cwd"] = b
		}
	}
	dt := time.Now().Format("Monday, January 2, 2006, 3PM MST")
	if b, err := json.Marshal(dt); err == nil {
		snap["datetime"] = b
	}
	if len(snap) == 0 {
		return nil
	}
	return &rpc.ChalkboardInput{Context: snap}
}

// buildChalkboard loads the embedded default body templates plus any
// user overrides from ~/.config/figaro/chalkboard/.
func buildChalkboard() *template.Template {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	if err != nil {
		slog.Warn("chalkboard templates load failed (disabled)", "err", err)
		return nil
	}
	home, _ := os.UserHomeDir()
	overrideDir := filepath.Join(home, ".config", "figaro", "chalkboard")
	if _, err := os.Stat(overrideDir); err == nil {
		if t, err := chalkboard.LoadOverrideTemplates(tmpls, overrideDir); err == nil {
			tmpls = t
		} else {
			slog.Warn("chalkboard override templates (using defaults)", "err", err)
		}
	}
	return tmpls
}

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "figaro")
}
