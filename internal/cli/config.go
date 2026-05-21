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

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
)

func mustLoadConfig() *config.Loaded {
	loaded, err := config.Load(config.DefaultConfigDir())
	if err != nil {
		die("config: %s", err)
	}
	return loaded
}

// hushOnce lazily initializes the managed hush instance.
var (
	hushInstance *managed.Hush
	hushOnce     sync.Once
	hushErr      error
)

func mustHush() *managed.Hush {
	hushOnce.Do(func() {
		// FIGARO_HUSH_APP lets dev shells pivot the entire hush
		// surface (identity, encrypted providers, socket) onto an
		// isolated AppName without touching the user's real one.
		appName := os.Getenv("FIGARO_HUSH_APP")
		if appName == "" {
			appName = "figaro"
		}
		hushInstance, hushErr = managed.New(managed.Options{
			AppName: appName,
		})
	})
	if hushErr != nil {
		die("hush: %s", hushErr)
	}
	return hushInstance
}

// ensureHush initializes hush. Must be called from the CLI process.
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

// buildPromptChalkboard collects per-prompt chalkboard values.
// These are read in the CLI process (which inherits the user's
// shell env) and sent with every prompt so the agent always has
// up-to-date values.
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
	// Allowlisted env vars from the caller's shell.
	for k, v := range chalkboard.EnvironmentSnapshot() {
		snap[k] = v
	}
	if len(snap) == 0 {
		return nil
	}
	return &rpc.ChalkboardInput{Context: snap}
}

// buildChalkboard loads body templates with user overrides.
func buildChalkboard() *template.Template {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	if err != nil {
		slog.Warn("chalkboard templates load failed (disabled)", "err", err)
		return nil
	}
	overrideDir := filepath.Join(config.DefaultConfigDir(), "chalkboard")
	if _, err := os.Stat(overrideDir); err == nil {
		if t, err := chalkboard.LoadOverrideTemplates(tmpls, overrideDir); err == nil {
			tmpls = t
		} else {
			slog.Warn("chalkboard override templates (using defaults)", "err", err)
		}
	}
	return tmpls
}

// stateDir returns the directory for persistent figaro state
// (OTel data, aria archives, aria chalkboards). XDG_STATE_HOME and
// FIGARO_STATE_DIR are honored to allow dev-shell isolation.
func stateDir() string {
	if d := os.Getenv("FIGARO_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "figaro")
}
