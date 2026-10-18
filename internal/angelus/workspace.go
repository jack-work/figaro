package angelus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/api/form"
)

func (h *handlers) placeWorkspace(id string, chosen bool) error {
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	kind, cwd := "", ""
	if v := snap.Lookup("system.sandbox"); v != nil {
		kind = *v
	}
	if v := snap.Lookup("system.cwd"); v != nil {
		cwd = *v
	}
	if kind == "" || kind == "local" || chosen && isDir(cwd) {
		return nil
	}
	dir, err := h.confinedWorkspace(kind, id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(dir)
	if err != nil {
		return err
	}
	_, err = h.angelus.Backend.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.cwd": raw}, nil))
	return err
}

func (h *handlers) confinedWorkspace(kind, id string) (string, error) {
	loaded, _ := h.settings()
	root, err := loaded.WorkspaceRoot()
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", fmt.Errorf("sandbox %s: no usable system.cwd was asked for, and no [sandbox] workspace_root is set to give the aria one", kind)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("sandbox.workspace_root: %w", err)
	}
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("workspace for %s: %w", id, err)
	}
	return dir, nil
}
