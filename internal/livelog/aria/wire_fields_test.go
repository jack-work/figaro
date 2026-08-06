package aria

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// notOnTheWire are Node fields the live delta deliberately does not carry, each
// with a reason. Anything NOT listed here must survive a round trip.
var notOnTheWire = map[string]string{
	"Version": "the record version lives on the Live envelope, not the node",
	"ID":      "node ids are positional (Nodes[i] is From+i); the string id is vestigial",
}

// A field added to livedoc.Node that nobody wires into fullSet/setField is
// silently dropped on the LIVE path while surviving on the committed path —
// a live-vs-committed divergence, which is precisely what the purity invariant
// forbids. Three had already accumulated: role and tool_call_id were emitted
// and dropped; lts was emitted and dropped; at and src were never emitted at
// all, so a Ctrl-O timestamp was blank until the turn sealed.
//
// This test fails on the NEXT one, without anybody having to notice.
func TestLiveDeltaCarriesEveryNodeField(t *testing.T) {
	full := livedoc.Node{
		Type:       livedoc.NodeTool,
		Role:       livedoc.RoleInput,
		LTs:        []uint64{7, 8},
		Src:        []livedoc.Src{{LT: 7, Block: 1}, {LT: 8, Block: 0}},
		ToolCallID: "toolu_x",
		At:         1785019500072,
		Markdown:   "body",
		Name:       "bash",
		Args:       map[string]any{"command": "ls"},
		Status:     livedoc.StatusOK,
		Output:     "out",
		Input:      `{"command":"ls`,
		Summary:    "ls",
		Sender:     "aria 76062b18",
		OpenedAt:   99,
		StartedAt:  111,
		FinishedAt: 222,
	}

	// Every settable field must actually be set, or the test proves nothing.
	rt := reflect.TypeOf(full)
	rv := reflect.ValueOf(full)
	for i := 0; i < rt.NumField(); i++ {
		if _, skip := notOnTheWire[rt.Field(i).Name]; skip {
			continue
		}
		if rv.Field(i).IsZero() {
			t.Fatalf("fixture leaves %s zero — extend it or the round trip is vacuous", rt.Field(i).Name)
		}
	}

	// Through the wire as JSON, exactly as a client receives it.
	raw, err := json.Marshal(fullSet(0, full))
	if err != nil {
		t.Fatal(err)
	}
	var nd NodeDelta
	if err := json.Unmarshal(raw, &nd); err != nil {
		t.Fatal(err)
	}
	got := foldDelta(livedoc.Node{}, nd)

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if why, skip := notOnTheWire[f.Name]; skip {
			_ = why
			continue
		}
		want := rv.Field(i).Interface()
		have := reflect.ValueOf(got).Field(i).Interface()
		if !reflect.DeepEqual(want, have) {
			t.Errorf("%s lost on the live path: sent %v, folded %v", f.Name, want, have)
		}
	}
}

// delta() is the other half: an incremental update must carry the same fields a
// creation does, or a value that arrives late never lands.
func TestLiveDiffCarriesLateArrivingFields(t *testing.T) {
	old := livedoc.Node{Type: livedoc.NodeProse}
	n := old
	n.Role = livedoc.RoleInput
	n.At = 1785019500072
	n.LTs = []uint64{3}
	n.Src = []livedoc.Src{{LT: 3, Block: 0}}
	n.ToolCallID = "toolu_y"

	raw, _ := json.Marshal(delta(0, old, n))
	var nd NodeDelta
	if err := json.Unmarshal(raw, &nd); err != nil {
		t.Fatal(err)
	}
	got := foldDelta(old, nd)

	if got.Role != n.Role || got.At != n.At || got.ToolCallID != n.ToolCallID {
		t.Errorf("scalar lost in diff: role=%q at=%d tool=%q", got.Role, got.At, got.ToolCallID)
	}
	if !reflect.DeepEqual(got.LTs, n.LTs) || !reflect.DeepEqual(got.Src, n.Src) {
		t.Errorf("provenance lost in diff: lts=%v src=%v", got.LTs, got.Src)
	}
}

// TestStreamedInputSplicesAndShrinks proves the streamed `input` field carries
// DELETIONS, not just appends.
//
// A tool's arguments arrive as a growing JSON prefix, so the common case is an
// append. Two ordinary cases are not: a bounded tail drops leading bytes as it
// slides, and the field is cleared once the decoded Args land. If the wire
// could only append, both would need a whole-value `set` — the thing delta
// compression exists to avoid — so this pins that livedoc.Diff's Del reaches
// the client intact.
func TestStreamedInputSplicesAndShrinks(t *testing.T) {
	steps := []struct {
		name     string
		input    string
		wantKind string // "set", "patch" or "unset"
	}{
		{"first appearance", `{"path":"/x`, "set"},
		{"append", `{"path":"/x.md","content":"aaa`, "patch"},
		{"tail slides — leading bytes DELETED", `content":"aaabbb`, "patch"},
		{"cleared when Args land", "", "unset"},
	}
	var prev livedoc.Node
	prev.Type = livedoc.NodeTool
	client := livedoc.Node{Type: livedoc.NodeTool}
	for _, st := range steps {
		next := prev
		next.Input = st.input
		d := delta(0, prev, next)
		var kind string
		switch {
		case d.Patch["input"] != (livedoc.Delta{}):
			kind = "patch"
		case d.Set["input"] != nil:
			kind = "set"
		default:
			for _, f := range d.Unset {
				if f == "input" {
					kind = "unset"
				}
			}
		}
		if kind != st.wantKind {
			t.Errorf("%s: got %q delta, want %q (%+v)", st.name, kind, st.wantKind, d)
		}
		client = foldDelta(client, d)
		if client.Input != st.input {
			t.Fatalf("%s: client folded to %q, want %q", st.name, client.Input, st.input)
		}
		prev = next
	}
}
