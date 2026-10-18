package config

import (
	"fmt"
	"path/filepath"
)

type SandboxConfig struct {
	WorkspaceRoot string `toml:"workspace_root"`
}

func (l *Loaded) WorkspaceRoot() (string, error) {
	root := l.cfg().Sandbox.WorkspaceRoot
	if root == "" {
		return "", nil
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("sandbox.workspace_root %q: want an absolute path", root)
	}
	return filepath.Clean(root), nil
}
