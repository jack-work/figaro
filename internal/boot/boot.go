// Package boot holds what both entries of the binary need before they
// diverge: configuration, state paths, the hush vault, the build identity.
package boot

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	hushconfig "github.com/jack-work/hush/config"
	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/config"
)

func LoadConfig() (*config.Loaded, error) {
	dir := config.DefaultConfigDir()
	if changes, mErr := config.MigrateLegacyOutfits(dir); mErr != nil {
		slog.Warn("outfit migration", "dir", dir, "err", mErr)
	} else {
		for _, c := range changes {
			fmt.Fprintln(os.Stderr, "figaro: "+c)
		}
	}
	loaded, err := config.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return loaded, nil
}

var (
	hushInstance *managed.Hush
	hushOnce     sync.Once
	hushErr      error

	// PromptPassphrase is the CLI's vault prompt. Nil in the daemon, which
	// has no terminal to ask on.
	PromptPassphrase func(h *managed.Hush, appName string) ([]byte, error)
)

// HushAgentTTL is how long the embedded hush agent lives before it
// self-terminates. Upstream defaults to 30m, which kills the credential
// mid-session on a long autonomous run: the daemon issues no CLI activity
// to respawn the agent. Override with FIGARO_HUSH_TTL (a Go duration).
func HushAgentTTL() time.Duration {
	if v := os.Getenv("FIGARO_HUSH_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 24 * time.Hour
}

func Hush() (*managed.Hush, error) {
	hushOnce.Do(func() {
		// FIGARO_HUSH_APP lets dev shells pivot the entire hush
		// surface (identity, encrypted providers, socket) onto an
		// isolated AppName without touching the user's real one.
		appName := os.Getenv("FIGARO_HUSH_APP")
		if appName == "" {
			appName = "figaro"
		}
		ttl := HushAgentTTL()
		opts := managed.Options{
			AppName: appName,
			TTL:     ttl,
			// opts.TTL only sets THIS parent's view; the re-exec'd embedded
			// agent re-loads its own config and would use the 30m default.
			// hush binds HUSH_TTL via viper AutomaticEnv and passes AgentEnv to
			// the child, so this is what actually gives the agent a long life.
			AgentEnv: []string{"HUSH_TTL=" + ttl.String()},
			PromptPassphrase: func() ([]byte, error) {
				// Test/dev bypass: a preset passphrase unlocks the hush without
				// a TTY prompt, so a detached daemon doesn't hang on first run
				// (dev shells autogenerate one). Honored ONLY for an isolated
				// embedded hush (FIGARO_HUSH_DIR set) so it can never touch the
				// user's real keystore.
				if pass := os.Getenv("FIGARO_HUSH_PASSPHRASE"); pass != "" && os.Getenv("FIGARO_HUSH_DIR") != "" {
					return []byte(pass), nil
				}
				if PromptPassphrase == nil {
					return nil, fmt.Errorf("vault locked and no terminal to ask on")
				}
				// hushInstance is assigned before anything can call this (the
				// callback fires from EnsureReady, never from managed.New).
				return PromptPassphrase(hushInstance, appName)
			},
		}
		// FIGARO_HUSH_DIR pins a fully isolated, EMBEDDED hush rooted at one
		// directory (its own identity, encrypted secrets, and agent socket
		// under <dir>/run). Hermetic dev shells set it so figaro runs its
		// own hush instance: re-authenticated per shell: instead of
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
		return nil, fmt.Errorf("hush: %w", hushErr)
	}
	return hushInstance, nil
}

// BuildFormTemplates loads body templates with user overrides.
func BuildFormTemplates() *template.Template {
	tmpls, err := form.LoadDefaultTemplates()
	if err != nil {
		slog.Warn("form templates load failed (disabled)", "err", err)
		return nil
	}
	overrideDir := filepath.Join(config.DefaultConfigDir(), "form")
	if _, err := os.Stat(overrideDir); err == nil {
		if t, err := form.LoadOverrideTemplates(tmpls, overrideDir); err == nil {
			tmpls = t
		} else {
			slog.Warn("form override templates (using defaults)", "err", err)
		}
	}
	return tmpls
}

// StateDir returns the directory for persistent figaro state
// (OTel data, aria archives, aria forms). XDG_STATE_HOME and
// FIGARO_STATE_DIR are honored to allow dev-shell isolation.
func StateDir() string {
	if d := os.Getenv("FIGARO_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "figaro")
}

// AriaRoot is the aria store's directory. ONE spelling, because the
// startup notice is only truthful while every caller agrees on it, and
// the notice is the one that would drift silently -- it degrades to
// "still starting" with no error.
func AriaRoot() string { return filepath.Join(StateDir(), "arias") }

func AngelusRuntimeDir() string {
	// FIGARO_RUNTIME_DIR is an explicit override used as-is (no
	// "figaro" suffix appended): lets dev shells point at an
	// isolated runtime without colliding with the user's daemon.
	if d := os.Getenv("FIGARO_RUNTIME_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "figaro")
	}
	return filepath.Join(os.TempDir(), "figaro")
}

func AngelusSocketPath() string {
	return filepath.Join(AngelusRuntimeDir(), "angelus.sock")
}
