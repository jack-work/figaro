package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/sandbox"
)

func (h *handlers) placeWorkspace(id string, chosen, dressedCwd bool) error {
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
	if loaded, _ := h.settings(); loaded != nil {
		if _, ok := loaded.Box(kind); ok {
			if dressedCwd || cwd == "" {
				return nil
			}
			_, err := h.angelus.Backend.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, nil, []string{"system.cwd"}))
			return err
		}
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

func (h *handlers) workspaceRoot() string {
	loaded, _ := h.settings()
	root, err := loaded.WorkspaceRoot()
	if err != nil {
		return ""
	}
	return root
}

func (h *handlers) cwdOf(id string) string {
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return ""
	}
	if v := snap.Lookup("system.cwd"); v != nil {
		return filepath.Clean(*v)
	}
	return ""
}

func ownWorkspace(root, cwd string) bool {
	return root != "" && cwd != "" && filepath.Dir(cwd) == root
}

func (h *handlers) ownWorkspaces(ids []string) []string {
	root := h.workspaceRoot()
	if root == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if cwd := h.cwdOf(id); ownWorkspace(root, cwd) && !seen[cwd] {
			seen[cwd] = true
			out = append(out, cwd)
		}
	}
	return out
}

func (h *handlers) releaseWorkspaces(dirs []string) {
	if len(dirs) == 0 {
		return
	}
	root := h.workspaceRoot()
	named := map[string]bool{}
	for _, id := range h.angelus.Backend.ConversationIDs() {
		named[h.cwdOf(id)] = true
	}
	for _, dir := range dirs {
		if named[dir] || !ownWorkspace(root, dir) {
			continue
		}
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			slog.Error("workspace left behind", "dir", dir, "err", err)
			continue
		}
		slog.Info("workspace removed with its last aria", "dir", dir)
	}
}

func (h *handlers) killSessions(id string) int {
	n := 0
	if h.angelus.Sessions != nil {
		n = h.angelus.Sessions.KillScope(id)
	}
	kind := h.sandboxOf(id)
	loaded, _ := h.settings()
	if _, ok := loaded.Box(kind); !ok {
		return n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := sandbox.KillBoxScope(ctx, loaded, kind, id)
	if err != nil {
		slog.Warn("sessions in the box may outlive the aria", "id", id, "box", kind, "err", err)
	}
	return n + m
}

func (h *handlers) sandboxOf(id string) string {
	if h.angelus.Backend == nil {
		return ""
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return ""
	}
	if v := snap.Lookup("system.sandbox"); v != nil {
		return *v
	}
	return ""
}
