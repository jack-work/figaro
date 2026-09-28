package angelus

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
)

// The composed half of the same invariant (issue #25): a real store, a real
// lineage, the process-shared composed cache wired the way the daemon wires it,
// and pages served by the AriaReader a DORMANT aria is read through.
//
// The parent is read WARM (so its turns are resident when the branch arrives),
// then after the fork, then again after reading the child: a cache that hands
// one aria's run to another shows it under that order.
func TestForkedComposedPagesDoNotContaminateEachOther(t *testing.T) {
	dir := t.TempDir()
	b, err := store.NewXwalBackend(dir, 0)
	require.NoError(t, err)
	defer b.Close()

	patch := func(kv map[string]string) message.Patch {
		set := map[string]json.RawMessage{}
		for k, v := range kv {
			raw, _ := json.Marshal(v)
			set[k] = raw
		}
		return form.Build(form.Snapshot{}, set, nil)
	}
	outfit, err := b.CreateOutfit("d", patch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	parent, err := b.CreateConversation(outfit)
	require.NoError(t, err)

	a := &Angelus{Registry: NewRegistry(), Backend: b, uiProj: uiir.New(nil)}
	a.UICache = aria.NewComposedCache(nil, a.composeTurns, a.uiLineage)
	reader := NewAriaReaderBounded(b, a.uiProj, a.UICache)

	say := func(id string, role message.Role, turn uint64, text string) {
		log, err := b.OpenFigIR(id)
		require.NoError(t, err)
		_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, TurnID: turn, Content: []message.Content{message.TextContent(text)},
		}})
		require.NoError(t, err)
	}
	// Turns with several records each, so an LT and a turn id are far apart.
	for turn := uint64(1); turn <= 3; turn++ {
		say(parent, message.RoleInput, turn, "PARENTQ")
		say(parent, message.RoleOutput, turn, "let me look")
		say(parent, message.RoleOutput, turn, "PARENT ANSWER")
	}

	// A read BEFORE the fork, so the parent's turns are resident in the shared
	// composed cache when the branch arrives. A cold cache cannot show a
	// contamination that lives in a cache.
	pageText := func(id string) string {
		page, err := reader.Page(id, aria.Anchor{}, aria.Anchor{}, 1<<20, true)
		require.NoError(t, err)
		var sb strings.Builder
		for _, p := range page.Parts {
			sb.WriteString("[")
			sb.WriteString(p.Inquiry)
			sb.WriteString("]")
			for _, n := range p.Nodes {
				sb.WriteString(n.Markdown)
				sb.WriteString(";")
			}
		}
		return sb.String()
	}
	warm := pageText(parent)
	t.Logf("parent, warm: %s", warm)

	for _, at := range []struct {
		name string
		lt   uint64
	}{
		{"head", 0},
		{"interior", 5}, // inside turn 2
	} {
		t.Run(at.name, func(t *testing.T) {
			child, _, err := b.ForkWith(parent, at.lt, patch(map[string]string{"aria_id": at.name}))
			require.NoError(t, err)
			say(child, message.RoleInput, 4, "CHILDQ-"+at.name)
			say(child, message.RoleOutput, 4, "CHILD ANSWER")
			// The parent is not frozen: it moves on too.
			say(parent, message.RoleInput, 4, "PARENTQ-AFTER")
			say(parent, message.RoleOutput, 4, "PARENT ANSWER AFTER")

			got := pageText(parent)
			t.Logf("parent after the fork: %s", got)
			t.Logf("child:                 %s", pageText(child))
			if strings.Contains(got, "CHILDQ") {
				t.Fatalf("the parent's composed page carries the branch's question: %s", got)
			}
			// And re-read the child first, then the parent: a cache that
			// serves one aria's run to another shows it under this order.
			_ = pageText(child)
			again := pageText(parent)
			if strings.Contains(again, "CHILDQ") {
				t.Fatalf("after reading the child, the parent's page carries its question: %s", again)
			}
		})
	}
}
