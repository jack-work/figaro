package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

type fakeGCDoor struct {
	plan    rpc.GCPlanResponse
	applied []string
	undone  []string
}

func (d *fakeGCDoor) GCPlan(context.Context, rpc.GCSelector) (*rpc.GCPlanResponse, error) {
	p := d.plan
	return &p, nil
}

func (d *fakeGCDoor) GCApply(_ context.Context, run string) (*rpc.GCApplyResponse, error) {
	d.applied = append(d.applied, run)
	return &rpc.GCApplyResponse{Run: run, Reaped: d.plan.Candidates}, nil
}

func (d *fakeGCDoor) GCUndo(_ context.Context, run string) (*rpc.GCUndoResponse, error) {
	d.undone = append(d.undone, run)
	return &rpc.GCUndoResponse{Run: run, Restored: []string{"a"}}, nil
}

func gcFake() *fakeGCDoor {
	return &fakeGCDoor{plan: rpc.GCPlanResponse{Run: "r1", Nodes: 1, Bytes: 2048,
		Candidates: []rpc.GCCandidate{{ID: "0123456789abcdef0123456789abcdef", Handle: "pewter-nuisance", IdleMS: 40 * 86400000, Messages: 3, Bytes: 2048}}}}
}

func runGCWith(t *testing.T, d *fakeGCDoor, o gcOpts, interactive bool, answer string) (string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	if o.sel == (rpc.GCSelector{}) {
		o.sel.IdleMS = 1
	}
	if err := runGC(context.Background(), d, o, gcTerm{out: &out, err: &errw, in: strings.NewReader(answer), interactive: interactive}); err != nil {
		t.Fatal(err)
	}
	return out.String(), errw.String()
}

func TestGCPipedWithoutYesChangesNothing(t *testing.T) {
	d := gcFake()
	out, errw := runGCWith(t, d, gcOpts{}, false, "y\n")
	if len(d.applied) != 0 {
		t.Fatal("a piped gc without --yes applied its plan")
	}
	if !strings.Contains(out, "pewter-nuisance") || !strings.Contains(errw, "--yes") {
		t.Fatalf("out %q err %q", out, errw)
	}
}

func TestGCAsksOnATerminal(t *testing.T) {
	d := gcFake()
	out, _ := runGCWith(t, d, gcOpts{}, true, "n\n")
	if len(d.applied) != 0 {
		t.Fatal("applied after n")
	}
	if !strings.Contains(out, "[y/N]") {
		t.Fatalf("no question: %q", out)
	}
	runGCWith(t, d, gcOpts{}, true, "y\n")
	if len(d.applied) != 1 || d.applied[0] != "r1" {
		t.Fatalf("applied %v after y", d.applied)
	}
}

func TestGCYesAppliesAndDryRunNever(t *testing.T) {
	d := gcFake()
	runGCWith(t, d, gcOpts{dryRun: true}, true, "y\n")
	if len(d.applied) != 0 {
		t.Fatal("--dry-run applied")
	}
	_, errw := runGCWith(t, d, gcOpts{yes: true}, false, "")
	if len(d.applied) != 1 {
		t.Fatal("--yes did not apply")
	}
	if !strings.Contains(errw, "figaro gc --undo r1") {
		t.Fatalf("the result does not say how to undo: %q", errw)
	}
}

func TestGCJSONPrintsThePlanAndNeverAsks(t *testing.T) {
	d := gcFake()
	out, _ := runGCWith(t, d, gcOpts{asJSON: true}, true, "y\n")
	if len(d.applied) != 0 || !strings.Contains(out, `"run": "r1"`) || strings.Contains(out, "[y/N]") {
		t.Fatalf("applied %v, out %q", d.applied, out)
	}
}

func TestGCUndoNamesTheRun(t *testing.T) {
	d := gcFake()
	_, errw := runGCWith(t, d, gcOpts{undo: "r1"}, false, "")
	if len(d.undone) != 1 || d.undone[0] != "r1" || !strings.Contains(errw, "restored 1") {
		t.Fatalf("undone %v, err %q", d.undone, errw)
	}
}
