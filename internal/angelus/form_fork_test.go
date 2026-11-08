package angelus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

func forkForm(t *testing.T, hs *Handlers, parent string, kv ...string) (string, error) {
	t.Helper()
	keys := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		keys[kv[i]] = json.RawMessage(kv[i+1])
	}
	patch := form.Build(form.Snapshot{}, keys, nil)
	raw, _ := json.Marshal(rpc.FormCreateRequest{Parent: parent, Patch: &patch})
	out, err := hs.Map[rpc.MethodFormCreate](inProcess(context.Background()), raw)
	if err != nil {
		return "", err
	}
	return out.(rpc.FormCreateResponse).FormID, nil
}

func mintForm(t *testing.T, h *handlers, kv ...string) string {
	t.Helper()
	keys := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		keys[kv[i]] = json.RawMessage(kv[i+1])
	}
	id, _, err := h.angelus.Backend.CreateForm("", form.Build(form.Snapshot{}, keys, nil))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func leaf(t *testing.T, h *handlers, id, key string) string {
	t.Helper()
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		t.Fatalf("state of %s: %v", id, err)
	}
	v, _ := snap.Get(key)
	return string(v)
}

func stemsOf(words string) string {
	sigil, rest := "", words
	if strings.HasPrefix(rest, "@") {
		sigil, rest = "@", rest[1:]
	}
	parts := strings.Split(rest, "-")
	for i, p := range parts {
		parts[i] = p[:min(3, len(p))]
	}
	return sigil + strings.Join(parts, "-")
}

// Every spelling of a form's id that `send` takes, `form fork` takes, and
// each fork is a copy of that one parent.
func TestAFormForkTakesEveryNameItsParentHas(t *testing.T) {
	h, hs := resolveHandlers(t)
	parent := mintForm(t, h, "brief", `"kept"`)
	mintForm(t, h, "brief", `"a neighbour"`)
	words := handle.Words(parent, handle.MinWords)
	names := map[string]string{
		"full id":         parent,
		"handle":          words,
		"stems":           stemsOf(words),
		"hex prefix":      parent[:9],
		"this node's own": h.nodeID() + rpc.NodeSeparator + parent,
	}
	for what, ref := range names {
		child, err := forkForm(t, hs, ref, "who", `"`+what+`"`)
		if err != nil {
			t.Errorf("fork by %s %q: %v", what, ref, err)
			continue
		}
		if got := leaf(t, h, child, "brief"); got != `"kept"` {
			t.Errorf("fork by %s %q copied brief=%s", what, ref, got)
		}
		if n, ok := h.angelus.Backend.Node(child); !ok || n.Parent != parent {
			t.Errorf("fork by %s %q hangs under %q", what, ref, n.Parent)
		}
	}
}

// A fork of a role carries target-aria and name, so it is a role too: the
// role's name then names two forms and is refused as ambiguous rather than
// sent to either; renamed at the fork, each answers to its own name.
func TestAForkOfARoleIsARole(t *testing.T) {
	h, hs := resolveHandlers(t)
	holder := mintConversation(t, h)
	role := mintForm(t, h, "name", `"stage"`, "target-aria", `"`+holder+`"`)

	understudy, err := forkForm(t, hs, role, "name", `"understudy"`)
	if err != nil {
		t.Fatal(err)
	}
	if got := leaf(t, h, understudy, "target-aria"); got != `"`+holder+`"` {
		t.Fatalf("the fork lost target-aria: %s", got)
	}
	ctx := inProcess(context.Background())
	for name, want := range map[string]string{"@stage": role, "@understudy": understudy} {
		if got, err := h.canonicalID(ctx, name); err != nil || got != want {
			t.Errorf("%s resolves to %q (%v), want %s", name, got, err, want)
		}
	}

	if _, err := forkForm(t, hs, role, "who", `"twin"`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.canonicalID(ctx, "@stage"); err == nil || !strings.Contains(err.Error(), "names 2 roles") {
		t.Errorf("@stage with two roles of that name: %v, want it refused as ambiguous", err)
	}
}

// form fork copies only an unbound form; anything else is refused, says
// why, and leaves nothing behind.
func TestAFormForkRefusesWhatIsNotAnUnboundForm(t *testing.T) {
	h, hs := resolveHandlers(t)
	parent := mintForm(t, h, "brief", `"kept"`)
	aria := mintConversation(t, h)
	before := len(h.angelus.Backend.Forms())
	for what, c := range map[string]struct {
		parent string
		kv     []string
		says   string
	}{
		"an aria":          {aria, []string{"x", `1`}, "not an unbound form"},
		"an empty patch":   {parent, nil, "names nothing"},
		"a name not there": {"@nothing-here", []string{"x", `1`}, ""},
	} {
		_, err := forkForm(t, hs, c.parent, c.kv...)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("fork of %s: %v, want a refusal saying %q", what, err, c.says)
		}
	}
	if after := len(h.angelus.Backend.Forms()); after != before {
		t.Errorf("refused forks left %d form(s) behind", after-before)
	}
	if got := leaf(t, h, parent, "brief"); got != `"kept"` {
		t.Errorf("a refused fork touched its parent: brief=%s", got)
	}
}
