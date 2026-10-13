// Package boot holds what the CLI and the daemon share before they diverge.
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

	// PromptPassphrase is nil in the daemon, which has no terminal to ask on.
	PromptPassphrase func(h *managed.Hush, appName string) ([]byte, error)
)

// HushAgentTTL overrides hush's 30m default, which would kill the credential
// mid-run with no CLI activity to respawn the agent.
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
		appName := os.Getenv("FIGARO_HUSH_APP")
		if appName == "" {
			appName = "figaro"
		}
		ttl := HushAgentTTL()
		opts := managed.Options{
			AppName: appName,
			TTL:     ttl,
			// The re-exec'd agent reloads its own config; only
			// HUSH_TTL in its env carries the TTL.
			AgentEnv: []string{"HUSH_TTL=" + ttl.String()},
			PromptPassphrase: func() ([]byte, error) {
				// A preset passphrase is honoured only for an
				// isolated embedded hush, never the user's
				// keystore.
				if pass := os.Getenv("FIGARO_HUSH_PASSPHRASE"); pass != "" && os.Getenv("FIGARO_HUSH_DIR") != "" {
					return []byte(pass), nil
				}
				if PromptPassphrase == nil {
					return nil, fmt.Errorf("vault locked and no terminal to ask on")
				}
				// The callback fires from EnsureReady, after
				// hushInstance is assigned.
				return PromptPassphrase(hushInstance, appName)
			},
		}
		// FIGARO_HUSH_DIR roots an embedded hush at one directory; a
		// sandbox cannot see the shared agent's socket.
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

// AriaRoot is the one spelling of the store directory; the startup notice silently
// degrades if a caller drifts.
func AriaRoot() string { return filepath.Join(StateDir(), "arias") }

func AngelusRuntimeDir() string {
	// FIGARO_RUNTIME_DIR is used as-is, with no "figaro" suffix.
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
