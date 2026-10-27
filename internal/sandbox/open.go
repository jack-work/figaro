package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/sandbox/box"
	"github.com/jack-work/figaro/internal/sandbox/bwrap"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

type Opened struct {
	api.Sandbox
	Workspace string
}

func Open(ctx context.Context, host *local.Host, loaded *config.Loaded, kind, aria, workspace, runtimeDir string) (Opened, error) {
	if b, ok := loaded.Box(kind); ok {
		return openBox(ctx, kind, b, aria, workspace)
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		if kind != "" && kind != "local" {
			return Opened{}, fmt.Errorf("sandbox %s: workspace %q is not a directory; set system.cwd to one", kind, workspace)
		}
		if workspace != "" {
			slog.Error("system.cwd is not a usable directory, falling back to the daemon's", "cwd", workspace)
		}
		workspace, _ = os.Getwd()
	}
	spec := api.Spec{Workspace: workspace, Env: ariaEnv(aria)}
	var prov api.Provider
	switch kind {
	case "", "local":
		prov = local.Provider{Host: host, Scope: aria}
	case "bwrap":
		prov = bwrap.Provider{Host: host, Scope: aria, RuntimeDir: runtimeDir, SkillsDir: outfit.BundledSkillsRoot(),
			Hide: []string{boot.StateDir(), config.DefaultConfigDir()}}
	default:
		return Opened{}, fmt.Errorf("sandbox: unknown provider %q", kind)
	}
	box, err := prov.Open(ctx, spec)
	if err != nil {
		return Opened{}, err
	}
	return Opened{Sandbox: box, Workspace: workspace}, nil
}

func openBox(ctx context.Context, name string, b config.BoxConfig, aria, workspace string) (Opened, error) {
	opened, err := box.Provider{Name: name, Socket: b.Exec, Scope: aria}.Open(ctx, api.Spec{Workspace: workspace, Env: ariaEnv(aria)})
	if err != nil {
		return Opened{}, err
	}
	return Opened{Sandbox: opened, Workspace: opened.(*box.Box).Workspace()}, nil
}

func KillBoxScope(ctx context.Context, loaded *config.Loaded, kind, aria string) (int, error) {
	b, ok := loaded.Box(kind)
	if !ok {
		return 0, nil
	}
	return box.Provider{Name: kind, Socket: b.Exec, Scope: aria}.KillScope(ctx)
}

func ariaEnv(aria string) map[string]string {
	env := map[string]string{
		"FIGARO_NO_BIND":      "1",
		"GIT_EDITOR":          "true",
		"GIT_SEQUENCE_EDITOR": "true",
		"EDITOR":              "true",
	}
	if aria != "" {
		env["FIGARO_ARIA"] = aria
	}
	return env
}

func HostPath(kind string) bool {
	switch kind {
	case "", "local", "bwrap":
		return true
	}
	return false
}
