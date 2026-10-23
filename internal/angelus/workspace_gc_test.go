package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func (r grantRig) kill(t *testing.T, id string, recursive bool) {
	t.Helper()
	raw, _ := json.Marshal(rpc.KillRequest{FigaroID: id, Recursive: recursive, Now: true})
	if _, err := r.h[rpc.MethodKill](as("user", strings.TrimPrefix(owner(), "user:")), raw); err != nil {
		t.Fatalf("kill %s: %v", id, err)
	}
}

func (r grantRig) fork(t *testing.T, id string) string {
	t.Helper()
	raw, _ := json.Marshal(rpc.ForkRequest{FigaroID: id})
	out, err := r.h[rpc.MethodFork](as("user", strings.TrimPrefix(owner(), "user:")), raw)
	if err != nil {
		t.Fatalf("fork %s: %v", id, err)
	}
	return out.(rpc.ForkResponse).Alternative
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A workspace the daemon assigned goes with its aria, but a fork copies
// system.cwd, so it stays while any remaining aria's form still names it.
func TestAnAssignedWorkspaceGoesWithTheLastAriaThatNamesIt(t *testing.T) {
	r, root := rigWithWorkspaceRoot(t)
	id, err := r.bindIn(t, owner(), "boxed", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, id)
	if err := os.WriteFile(filepath.Join(ws, "notes"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	alt := r.fork(t, id)
	if got := r.cwdOf(t, alt); got != ws {
		t.Fatalf("the fork's cwd is %q, want the parent's %q", got, ws)
	}

	r.kill(t, alt, false)
	if !exists(ws) {
		t.Fatal("the workspace went with the fork while its parent still names it")
	}
	r.kill(t, id, false)
	if exists(ws) {
		t.Fatal("the workspace outlived the last aria that names it")
	}
}

func TestKillRecursiveTakesTheWorkspaceOnce(t *testing.T) {
	r, root := rigWithWorkspaceRoot(t)
	id, err := r.bindIn(t, owner(), "boxed", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.fork(t, id)
	r.kill(t, id, true)
	if exists(filepath.Join(root, id)) {
		t.Fatal("kill -r left the workspace behind")
	}
}

// Nothing outside workspace_root is ever removed: not a cwd the caller
// chose, and not the root itself.
func TestNothingOutsideTheRootIsRemoved(t *testing.T) {
	r, root := rigWithWorkspaceRoot(t)
	chosen, err := r.bindIn(t, owner(), "boxed", r.cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := r.createIn(t, owner(), "mock", r.cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.kill(t, chosen, false)
	r.kill(t, local, false)
	if !exists(r.cwd) {
		t.Fatal("a cwd the caller chose was removed")
	}

	nested := filepath.Join(root, "shared", "project")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	deep, err := r.bindIn(t, owner(), "boxed", nested, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.kill(t, deep, false)
	if !exists(nested) || !exists(root) {
		t.Fatal("a directory that is not one aria's own workspace was removed")
	}
}

// A birth whose box fails to open after its workspace was placed takes the
// workspace with it.
func TestAFailedOpenRemovesThePlacedWorkspace(t *testing.T) {
	r, root := rigWithWorkspaceRoot(t)
	_, err := r.createIn(t, "sandbox:dev-3", "mock", "/nowhere/on/this/host", nil)
	if err == nil || !strings.Contains(err.Error(), `unknown provider "dev-3"`) {
		t.Fatalf("create: %v", err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("the failed birth left %d workspace(s) under the root", len(entries))
	}
}
