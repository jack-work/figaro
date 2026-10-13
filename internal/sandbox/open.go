package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	api "github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/sandbox/bwrap"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

type Opened struct {
	api.Sandbox
	Workspace string
}

func Open(ctx context.Context, host *local.Host, kind, aria, workspace, runtimeDir string) (Opened, error) {
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
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
		prov = bwrap.Provider{Host: host, Scope: aria, RuntimeDir: runtimeDir, SkillsDir: outfit.BundledSkillsRoot()}
	default:
		return Opened{}, fmt.Errorf("sandbox: unknown provider %q", kind)
	}
	box, err := prov.Open(ctx, spec)
	if err != nil {
		return Opened{}, err
	}
	return Opened{Sandbox: box, Workspace: workspace}, nil
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
