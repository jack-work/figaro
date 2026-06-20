package livedoc

import "testing"

// applyAll folds ops into a node list (consumer side).
func applyAll(nodes []Node, ops []Op) []Node {
	for _, op := range ops {
		nodes = ApplyOp(nodes, op)
	}
	return nodes
}

func eqNodes(a, b []Node) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Markdown != b[i].Markdown ||
			a[i].Output != b[i].Output || a[i].Status != b[i].Status || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func TestDiffNodes_OpenPatchSet(t *testing.T) {
	old := []Node{{Type: NodeProse, Markdown: "Da ca"}}
	next := []Node{
		{Type: NodeProse, Markdown: "Da capo!"},
		{Type: NodeTool, ID: "a", Name: "bash", Status: StatusRunning, Output: "line1\n"},
	}
	ops := DiffNodes(old, next)
	if len(ops) != 2 {
		t.Fatalf("want patch+open, got %d ops: %+v", len(ops), ops)
	}
	if ops[0].Kind != OpPatch || ops[0].Field != "markdown" {
		t.Errorf("op0 = %+v", ops[0])
	}
	if ops[1].Kind != OpOpen || ops[1].Node.Type != NodeTool {
		t.Errorf("op1 = %+v", ops[1])
	}
	if got := applyAll(append([]Node{}, old...), ops); !eqNodes(got, next) {
		t.Fatalf("apply mismatch:\n got %+v\nwant %+v", got, next)
	}
}

func TestDiffNodes_ToolOutputStreamThenComplete(t *testing.T) {
	old := []Node{{Type: NodeTool, ID: "a", Name: "bash", Status: StatusRunning, Output: "one\n"}}
	next := []Node{{Type: NodeTool, ID: "a", Name: "bash", Status: StatusOK, Output: "one\ntwo\n"}}
	ops := DiffNodes(old, next)
	// One output patch + one status set.
	var patch, set int
	for _, o := range ops {
		switch o.Kind {
		case OpPatch:
			patch++
			if o.Field != "output" {
				t.Errorf("expected output patch, got %+v", o)
			}
		case OpSet:
			set++
			if o.Status != StatusOK {
				t.Errorf("expected ok, got %+v", o)
			}
		}
	}
	if patch != 1 || set != 1 {
		t.Fatalf("want 1 patch + 1 set, got %d/%d: %+v", patch, set, ops)
	}
	if got := applyAll([]Node{{Type: NodeTool, ID: "a", Name: "bash", Status: StatusRunning, Output: "one\n"}}, ops); !eqNodes(got, next) {
		t.Fatalf("apply mismatch: %+v", got)
	}
}

func TestDiffNodes_NoChange(t *testing.T) {
	n := []Node{{Type: NodeProse, Markdown: "x"}, {Type: NodeTool, ID: "a", Status: StatusOK}}
	if ops := DiffNodes(n, n); ops != nil {
		t.Fatalf("expected no ops, got %+v", ops)
	}
}
