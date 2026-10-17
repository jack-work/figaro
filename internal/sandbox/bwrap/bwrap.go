package bwrap

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

var allowEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "COLORTERM", "LANG", "LC_ALL", "TZ"}

var maskEnv = []string{"HOME", "FIGARO_CONFIG_DIR", "FIGARO_HUSH_DIR", "FIGARO_STATE_DIR", "CREDENTIALS_DIRECTORY"}

var maskPaths = []string{"/run/credentials"}

var apiArgs = sync.OnceValue(func() []string {
	fresh := []string{"--dev", "/dev", "--proc", "/proc"}
	probe := append([]string{"--unshare-all", "--share-net", "--ro-bind", "/", "/"}, fresh...)
	probe = append(probe, "sh", "-c", ": > /dev/null")
	if exec.Command("bwrap", probe...).Run() == nil {
		return fresh
	}
	return []string{"--dev-bind", "/dev", "/dev", "--ro-bind", "/proc", "/proc"}
})

type Provider struct {
	Host       *local.Host
	Scope      string
	RuntimeDir string
	SkillsDir  string
}

func (p Provider) Open(_ context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(spec.Workspace)
	if err != nil {
		return nil, err
	}
	var env []string
	for _, k := range allowEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if p.RuntimeDir != "" {
		env = append(env, "FIGARO_RUNTIME_DIR="+p.RuntimeDir)
	}
	args := []string{"bwrap", "--die-with-parent", "--unshare-all", "--share-net",
		"--ro-bind", "/", "/"}
	args = append(args, apiArgs()...)
	args = append(args, "--tmpfs", "/tmp")
	for _, d := range maskPaths {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			args = append(args, "--tmpfs", d)
		}
	}
	for _, k := range maskEnv {
		if d := os.Getenv(k); d != "" {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				args = append(args, "--tmpfs", d)
			}
		}
	}
	if p.SkillsDir != "" {
		args = append(args, "--ro-bind", p.SkillsDir, p.SkillsDir)
	}
	if p.RuntimeDir != "" {
		args = append(args, "--bind", p.RuntimeDir, p.RuntimeDir)
	}
	args = append(args, "--bind", root, root)
	return local.New(p.Host, p.Scope, &local.Sandbox{
		Workspace: root,
		Env:       local.MergeEnv(env, spec.Env),
		Argv: func(dir, command string) []string {
			return append(append([]string(nil), args...), "--chdir", dir, "--", "bash", "-c", command)
		},
		Files:  workspace{root: root},
		Claims: sandbox.Isolation{Provider: "bwrap", FS: true, Env: true, Secrets: true, Persistent: true},
	})
}

type workspace struct {
	root string
	local.FS
}

func (w workspace) resolve(p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.root, p)
	}
	real, rest := filepath.Clean(p), ""
	for {
		if r, err := filepath.EvalSymlinks(real); err == nil {
			real = filepath.Join(r, rest)
			break
		}
		parent := filepath.Dir(real)
		if parent == real {
			return "", sandbox.ErrOutsideWorkspace
		}
		rest, real = filepath.Join(filepath.Base(real), rest), parent
	}
	if real != w.root && !strings.HasPrefix(real, w.root+string(filepath.Separator)) {
		return "", sandbox.ErrOutsideWorkspace
	}
	return real, nil
}

func (w workspace) ReadFile(p string) ([]byte, error) {
	p, err := w.resolve(p)
	if err != nil {
		return nil, err
	}
	return w.FS.ReadFile(p)
}

func (w workspace) WriteFile(p string, data []byte, perm fs.FileMode) error {
	p, err := w.resolve(p)
	if err != nil {
		return err
	}
	return w.FS.WriteFile(p, data, perm)
}

func (w workspace) Stat(p string) (fs.FileInfo, error) {
	p, err := w.resolve(p)
	if err != nil {
		return nil, err
	}
	return w.FS.Stat(p)
}

func (w workspace) MkdirAll(p string, perm fs.FileMode) error {
	p, err := w.resolve(p)
	if err != nil {
		return err
	}
	return w.FS.MkdirAll(p, perm)
}

func (w workspace) Rename(oldpath, newpath string) error {
	from, err := w.resolve(oldpath)
	if err != nil {
		return err
	}
	to, err := w.resolve(newpath)
	if err != nil {
		return err
	}
	return w.FS.Rename(from, to)
}

func (w workspace) Update(p string, fn func([]byte) ([]byte, error)) error {
	p, err := w.resolve(p)
	if err != nil {
		return err
	}
	return w.FS.Update(p, fn)
}
