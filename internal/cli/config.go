package cli

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	hushconfig "github.com/jack-work/hush/config"
	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tui"
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

// hushAgentTTL is how long the embedded hush agent lives before it
// self-terminates. Upstream defaults to 30m, which silently kills the
// credential mid-session on a long autonomous run: the daemon issues no CLI
// activity to respawn the agent, so after 30m every model request fails with
// "no credential". figaro needs the agent to outlive hour-long+ turns, so we
// set it long; the daemon also keep-alives/respawns it (see angelus.go).
// Override with FIGARO_HUSH_TTL (a Go duration) for testing.
func hushAgentTTL() time.Duration {
	if v := os.Getenv("FIGARO_HUSH_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 24 * time.Hour
}

func mustHush() *managed.Hush {
	hushOnce.Do(func() {
		// FIGARO_HUSH_APP lets dev shells pivot the entire hush
		// surface (identity, encrypted providers, socket) onto an
		// isolated AppName without touching the user's real one.
		appName := os.Getenv("FIGARO_HUSH_APP")
		if appName == "" {
			appName = "figaro"
		}
		ttl := hushAgentTTL()
		opts := managed.Options{
			AppName: appName,
			TTL:     ttl,
			// opts.TTL only sets THIS parent's view; the re-exec'd embedded
			// agent re-loads its own config and would use the 30m default.
			// hush binds HUSH_TTL via viper AutomaticEnv and passes AgentEnv to
			// the child, so this is what actually gives the agent a long life
			// (without it, the credential dies every 30m mid-session).
			AgentEnv: []string{"HUSH_TTL=" + ttl.String()},
			// Drive the first-run passphrase UX from figaro's TUI
			// so hush doesn't try to read /dev/tty in parallel with
			// a bubbletea form holding the terminal. Falls back to a
			// plain numbered prompt when the env can't host the TUI.
			PromptPassphrase: func() ([]byte, error) {
				// Test/dev bypass: a preset passphrase unlocks the hush without
				// a TTY prompt, so a detached daemon doesn't hang on first run
				// (dev shells autogenerate one). Honored ONLY for an isolated
				// embedded hush (FIGARO_HUSH_DIR set) so it can never touch the
				// user's real keystore.
				if pass := os.Getenv("FIGARO_HUSH_PASSPHRASE"); pass != "" && os.Getenv("FIGARO_HUSH_DIR") != "" {
					return []byte(pass), nil
				}
				return tui.PromptPassphrase(appName)
			},
		}
		// FIGARO_HUSH_DIR pins a fully isolated, EMBEDDED hush rooted at one
		// directory (its own identity, encrypted secrets, and agent socket
		// under <dir>/run). Hermetic dev shells set it so figaro runs its
		// own hush instance — re-authenticated per shell — instead of
		// reaching the user's shared agent, whose socket isn't visible
		// inside the sandbox. Without it, hush resolves its normal surface.
		if dir := os.Getenv("FIGARO_HUSH_DIR"); dir != "" {
			opts.Mode = managed.ModeEmbedded
			opts.Dirs = &hushconfig.Dirs{
				ConfigDir:  filepath.Join(dir, "config"),
				StateDir:   filepath.Join(dir, "state"),
				RuntimeDir: filepath.Join(dir, "run"),
			}
		}
		hushInstance, hushErr = managed.New(opts)
	})
	if hushErr != nil {
		die("hush: %s", hushErr)
	}
	return hushInstance
}

// ensureHush initializes hush. Must be called from the CLI process.
//
// The first-run identity flow (prompt for passphrase, init the age
// identity, persist to keyring) is owned by hush's managed package —
// figaro is just a consumer here. Anything more elaborate (provider
// selection, default loadout) is layered above via runFirstRunIfNeeded.
func ensureHush() {
	h := mustHush()
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

// cacheDir returns the directory for ephemeral figaro data that can
// be regenerated (update-check memo, etc). XDG_CACHE_HOME and
// FIGARO_CACHE_DIR win in that order.
func cacheDir() string {
	if d := os.Getenv("FIGARO_CACHE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "figaro")
}
