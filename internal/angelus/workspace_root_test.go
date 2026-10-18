package angelus_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func rigWithWorkspaceRoot(t *testing.T) (grantRig, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspaces")
	return newRigConfigured(t, grantPolicy, "\n[sandbox]\nworkspace_root = \""+root+"\"\n"), root
}

func (r grantRig) createIn(t *testing.T, who, outfit, cwd string, patch *form.Patch) (string, error) {
	t.Helper()
	parts := strings.SplitN(who, ":", 2)
	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{outfit}, Cwd: cwd, Patch: patch})
	out, err := r.h[rpc.MethodCreate](as(parts[0], parts[1]), raw)
	if err != nil {
		return "", err
	}
	return out.(rpc.CreateResponse).FigaroID, nil
}

func (r grantRig) bindIn(t *testing.T, who, outfit, cwd string, patch *form.Patch) (string, error) {
	t.Helper()
	parts := strings.SplitN(who, ":", 2)
	raw, _ := json.Marshal(rpc.FormBindRequest{Outfits: []string{outfit}, Cwd: cwd, Patch: patch})
	out, err := r.h[rpc.MethodFormBind](as(parts[0], parts[1]), raw)
	if err != nil {
		return "", err
	}
	return out.(rpc.FormBindResponse).FigaroID, nil
}

func (r grantRig) cwdOf(t *testing.T, id string) string {
	t.Helper()
	snap, err := r.backend.FormState(id)
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Lookup("system.cwd"); v != nil {
		return *v
	}
	return ""
}

func owner() string { return "user:" + strconv.Itoa(os.Getuid()) }

func needBwrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--unshare-all", "--ro-bind", "/", "/", "--", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot sandbox here: %v: %s", err, out)
	}
}

func requireOwnWorkspace(t *testing.T, r grantRig, root, id string) {
	t.Helper()
	got := r.cwdOf(t, id)
	if got != filepath.Join(root, id) {
		t.Fatalf("workspace %q, want %q", got, filepath.Join(root, id))
	}
	st, err := os.Stat(got)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("workspace %q: %v %v", got, st, err)
	}
}

// Under bwrap the workspace is the box's only read-write bind. A cwd this
// host cannot use (a web client's own path, say) must not fall back to the
// daemon's working directory, which in a container may be the store itself:
// the aria gets a directory of its own under the configured root. form.bind
// places it without opening the box, so this runs where bwrap cannot.
func TestAConfinedBirthWithAnUnusableCwdGetsAWorkspaceOfItsOwn(t *testing.T) {
	r, root := rigWithWorkspaceRoot(t)
	for _, cwd := range []string{"", "/nowhere/on/this/host"} {
		id, err := r.bindIn(t, owner(), "boxed", cwd, nil)
		if err != nil {
			t.Fatalf("cwd %q: %v", cwd, err)
		}
		requireOwnWorkspace(t, r, root, id)
	}
}

// create places the workspace the same way, then opens the box in it.
func TestACreateUnderBwrapOpensInItsOwnWorkspace(t *testing.T) {
	needBwrap(t)
	r, root := rigWithWorkspaceRoot(t)
	id, err := r.createIn(t, owner(), "boxed", "/nowhere/on/this/host", nil)
	if err != nil {
		t.Fatal(err)
	}
	requireOwnWorkspace(t, r, root, id)
}

// With no root configured there is nowhere safe to put the aria, so the
// birth is refused and nothing is left behind.
func TestAConfinedBirthWithAnUnusableCwdAndNoRootIsRefused(t *testing.T) {
	r := newGrantRig(t)
	if _, err := r.createIn(t, owner(), "boxed", "/nowhere/on/this/host", nil); err == nil || !strings.Contains(err.Error(), "workspace_root") {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.bindIn(t, owner(), "boxed", "/nowhere/on/this/host", nil); err == nil || !strings.Contains(err.Error(), "workspace_root") {
		t.Fatalf("form.bind: %v", err)
	}
	if n := len(r.backend.ConversationIDs()); n != 0 {
		t.Fatalf("a refused birth left %d aria(s) behind", n)
	}
}

// A caller allowed bwrap but not local cannot name a host directory, but a
// workspace the daemon assigns is not the caller reaching the host.
func TestARemoteCallerGetsAnAssignedWorkspaceButCannotNameAHostOne(t *testing.T) {
	r, root := rigWithWorkspaceRoot(t)
	id, err := r.bindIn(t, "user:remote", "boxed", "/nowhere/on/this/host", nil)
	if err != nil {
		t.Fatal(err)
	}
	requireOwnWorkspace(t, r, root, id)
	if _, err := r.bindIn(t, "user:remote", "boxed", r.cwd, nil); err == nil || !strings.Contains(err.Error(), "sandbox:local") {
		t.Fatalf("a usable host cwd: %v", err)
	}
	slash, _ := json.Marshal("/")
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.cwd": slash}, nil)
	if _, err := r.bindIn(t, "user:remote", "boxed", "", &patch); err == nil || !strings.Contains(err.Error(), "sandbox:local") {
		t.Fatalf("system.cwd in the patch: %v", err)
	}
}

// local keeps today's behaviour: an unusable cwd falls back to the daemon's.
func TestALocalBirthWithAnUnusableCwdStillTakesTheDaemonsCwd(t *testing.T) {
	r, _ := rigWithWorkspaceRoot(t)
	daemon, _ := os.Getwd()
	id, err := r.createIn(t, owner(), "mock", "/nowhere/on/this/host", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.cwdOf(t, id); got != daemon {
		t.Fatalf("local cwd %q, want the daemon's %q", got, daemon)
	}
}

// A cwd in the patch is overridden by the birth's own, so it cannot smuggle
// the daemon's working directory into a confined aria either.
func TestACwdInThePatchDoesNotKeepTheDaemonsCwdUnderBwrap(t *testing.T) {
	r, root := rigWithWorkspaceRoot(t)
	nowhere, _ := json.Marshal("/nowhere/on/this/host")
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.cwd": nowhere}, nil)
	id, err := r.bindIn(t, owner(), "boxed", "", &patch)
	if err != nil {
		t.Fatal(err)
	}
	requireOwnWorkspace(t, r, root, id)
}
