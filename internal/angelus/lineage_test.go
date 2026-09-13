package angelus

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// The lineage read, against a real store with real forks.
//
// The number under test is the DIVERGENCE, and it is a turn: the tree records
// a fork base as an LT, and every client coordinate is a turn. Getting that
// conversion wrong by one is not a rounding error, it is one aria's answer
// rendered under another aria's question.

type lineageFixture struct {
	be     *store.XwalBackend
	a      *Angelus
	parent string
	branch string
	cousin string
}

// buildLineage makes a parent of six turns, a branch cut at turn 4 and a
// cousin cut at turn 6, which is the shape the cousin rule needs: two children
// of one parent that do NOT share the same amount of it.
func buildLineage(t *testing.T) lineageFixture {
	t.Helper()
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })

	parent, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"p"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	plog, err := be.OpenFigIR(parent)
	if err != nil {
		t.Fatal(err)
	}
	write := func(log store.Log[message.Message], role message.Role, text string) uint64 {
		e, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, Content: []message.Content{message.TextContent(text)}}})
		if err != nil {
			t.Fatal(err)
		}
		return e.LT
	}
	var opens []uint64
	for i := 1; i <= 6; i++ {
		opens = append(opens, write(plog, message.RoleInput, "question"))
		write(plog, message.RoleOutput, "answer")
	}

	_, branch, err := be.ForkAt(parent, opens[3]) // turn 4 is the branch's own
	if err != nil {
		t.Fatal(err)
	}
	blog, _ := be.OpenFigIR(branch)
	write(blog, message.RoleOutput, "branch answers four")
	write(blog, message.RoleInput, "branch question five")
	write(blog, message.RoleOutput, "branch answers five")

	_, cousin, err := be.ForkAt(parent, opens[5]) // turn 6 is the cousin's own
	if err != nil {
		t.Fatal(err)
	}
	clog, _ := be.OpenFigIR(cousin)
	write(clog, message.RoleOutput, "cousin answers six")

	return lineageFixture{be: be, a: &Angelus{Backend: be, uiProj: uiir.New(nil)}, parent: parent, branch: branch, cousin: cousin}
}

func (f lineageFixture) ask(t *testing.T, id, against string) rpc.LineageResponse {
	t.Helper()
	h := &handlers{angelus: f.a}
	params, _ := json.Marshal(rpc.LineageRequest{FigaroID: id, Against: against})
	out, err := h.lineage(t.Context(), params)
	if err != nil {
		t.Fatalf("lineage(%s, %s): %v", id, against, err)
	}
	return out.(rpc.LineageResponse)
}

// A branch and its parent diverge at the branch's first own turn.
func TestLineage_ParentAndBranchDivergeAtTheBranchsFirstTurn(t *testing.T) {
	f := buildLineage(t)
	got := f.ask(t, f.branch, f.parent)
	if got.Divergence != 4 {
		t.Fatalf("divergence %d, want turn 4: the branch was cut at the opening of turn 4", got.Divergence)
	}
	if got.Ancestor != f.parent {
		t.Fatalf("ancestor %q, want the parent %q", got.Ancestor, f.parent)
	}
	// The answer is symmetric: which one is on screen does not change where
	// they part.
	if back := f.ask(t, f.parent, f.branch); back.Divergence != 4 {
		t.Fatalf("the other way round says %d", back.Divergence)
	}
}

// TWO COUSINS SHARE THE SHALLOWER CUT. The branch owns from turn 4 and the
// cousin from turn 6, so they share turns 1 to 3 and nothing more: a client
// told 6 would paint the parent's turns 4 and 5 as the branch's.
func TestLineage_CousinsShareTheShallowerCut(t *testing.T) {
	f := buildLineage(t)
	got := f.ask(t, f.branch, f.cousin)
	if got.Divergence != 4 {
		t.Fatalf("cousins diverge at %d, want 4 (min of the two bases, not the deeper one)", got.Divergence)
	}
	if got.Ancestor != f.parent {
		t.Fatalf("ancestor %q, want the parent they were both cut from", got.Ancestor)
	}
}

// An aria against itself shares everything, and says so in the one way the
// client's rule can use without a special case.
func TestLineage_AnAriaAgainstItselfSharesEverything(t *testing.T) {
	f := buildLineage(t)
	got := f.ask(t, f.branch, f.branch)
	if got.Divergence != ^uint64(0) {
		t.Fatalf("divergence %d, want the maximum: everything is shared", got.Divergence)
	}
}

// The chain carries the conversations and nothing above them: the genesis root
// and the outfit stump hold no turns a reader can see.
func TestLineage_ChainIsConversationsOnly(t *testing.T) {
	f := buildLineage(t)
	got := f.ask(t, f.branch, "")
	if len(got.Chain) != 2 {
		t.Fatalf("chain is %v, want [parent, branch]", got.Chain)
	}
	if got.Chain[0].Node != f.parent || got.Chain[1].Node != f.branch {
		t.Fatalf("chain is %v, want [%s, %s]", got.Chain, f.parent, f.branch)
	}
	if got.Chain[0].Base != 0 {
		t.Fatalf("the root of the chain begins at turn %d, want 0: it owns everything", got.Chain[0].Base)
	}
	if got.Chain[1].Base != 4 {
		t.Fatalf("the branch owns from turn %d, want 4", got.Chain[1].Base)
	}
}

// Unrelated arias share nothing, and nothing is zero, which is the reload the
// switch did before any of this existed.
func TestLineage_UnrelatedAriasShareNothing(t *testing.T) {
	f := buildLineage(t)
	other, _, err := f.be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"q"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := f.ask(t, f.branch, other)
	if got.Divergence != 0 || got.Ancestor != "" {
		t.Fatalf("divergence %d ancestor %q, want 0 and none", got.Divergence, got.Ancestor)
	}
}

// THE FAST PATH IS THE STAMPED ONE. Every record a live agent writes carries
// the turn it belongs to, so the conversion is one record read; the walk above
// is the fallback for logs written before the stamp existed. Both must give
// the same answer, or the number depends on the age of the log.
func TestLineage_StampedLogsTakeTheOneRecordPath(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	parent, _, err := be.ForkWith("", 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"aria_id": json.RawMessage(`"p"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	plog, _ := be.OpenFigIR(parent)
	var opens []uint64
	for i := uint64(1); i <= 5; i++ {
		e, err := plog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, TurnID: i, Content: []message.Content{message.TextContent("q")}}})
		if err != nil {
			t.Fatal(err)
		}
		opens = append(opens, e.LT)
		if _, err := plog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleOutput, TurnID: i, Content: []message.Content{message.TextContent("a")}}}); err != nil {
			t.Fatal(err)
		}
	}
	_, branch, err := be.ForkAt(parent, opens[2])
	if err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Backend: be, uiProj: uiir.New(nil)}
	h := &handlers{angelus: a}
	params, _ := json.Marshal(rpc.LineageRequest{FigaroID: branch, Against: parent})
	out, err := h.lineage(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(rpc.LineageResponse).Divergence; got != 3 {
		t.Fatalf("divergence %d, want 3: the fork took the opening record of turn 3", got)
	}
}
