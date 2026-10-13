package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/api/sandbox"
)

var DaemonEnvDenylist = []string{
	"_FIGARO_DAEMON",
	"HUSH_AGENT_CHILD",
	"HUSH_MANAGED_CONFIG_DIR",
	"HUSH_MANAGED_STATE_DIR",
	"HUSH_MANAGED_RUNTIME_DIR",
}

func Environ() []string {
	denied := map[string]bool{}
	for _, k := range DaemonEnvDenylist {
		denied[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !denied[k] {
			out = append(out, kv)
		}
	}
	return out
}

func MergeEnv(base []string, over map[string]string) []string {
	out := append([]string(nil), base...)
	for k, v := range over {
		i := 0
		for i < len(out) && !strings.HasPrefix(out[i], k+"=") {
			i++
		}
		if i == len(out) {
			out = append(out, "")
		}
		out[i] = k + "=" + v
	}
	return out
}

type Provider struct {
	Host  *Host
	Scope string
}

func (p Provider) Open(_ context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	return New(p.Host, p.Scope, &Sandbox{
		Workspace: spec.Workspace,
		Env:       MergeEnv(Environ(), spec.Env),
		Argv:      func(_, command string) []string { return []string{bashPath(), "-c", command} },
		Files:     FS{},
		Claims:    sandbox.Isolation{Provider: "local", Persistent: true},
	})
}

type Sandbox struct {
	Workspace string
	Env       []string
	Argv      func(dir, command string) []string
	Files     sandbox.FS
	Claims    sandbox.Isolation
	sessions  sandbox.Sessions
}

func New(host *Host, scope string, s *Sandbox) (*Sandbox, error) {
	if info, err := s.Files.Stat(s.Workspace); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("sandbox: workspace %q is not a directory", s.Workspace)
	}
	s.sessions = host.Scoped(scope, func(req sandbox.ExecRequest) (sandbox.Process, error) {
		return s.Exec(context.Background(), req)
	})
	return s, nil
}

func (s *Sandbox) Exec(ctx context.Context, req sandbox.ExecRequest) (sandbox.Process, error) {
	if req.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	dir := req.Cwd
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.Workspace, dir)
	}
	if info, err := s.Files.Stat(dir); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", dir)
	}
	return Start(ctx, s.Argv(dir, req.Command), dir, MergeEnv(s.Env, req.Env), req)
}

func (s *Sandbox) Sessions() sandbox.Sessions   { return s.sessions }
func (s *Sandbox) FS() sandbox.FS               { return s.Files }
func (s *Sandbox) Isolation() sandbox.Isolation { return s.Claims }
func (s *Sandbox) Close() error                 { return nil }
