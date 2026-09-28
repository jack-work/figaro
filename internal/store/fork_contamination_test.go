package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// A fork shares a prefix and nothing else: neither log may hold the other's
// records. This is the store half of the invariant issue #25 reports as
// violated on screen, pinned here where it is cheap to check.
//
// Head fork and interior fork, the child writes, the parent writes on, and
// each log is read back whole.
func TestForkedLogsDoNotContaminateEachOther(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	l, err := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := b.CreateConversation(l)
	if err != nil {
		t.Fatal(err)
	}
	ir, err := b.OpenFigIR(parent)
	if err != nil {
		t.Fatal(err)
	}
	say := func(log Log[message.Message], role message.Role, turn uint64, text string) {
		if _, err := log.Append(Entry[message.Message]{Payload: message.Message{
			Role: role, TurnID: turn, Content: []message.Content{message.TextContent(text)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// Two whole turns, each several records: prose, a tool round, more prose,
	// so a turn id and an LT are nowhere near each other.
	for turn := uint64(1); turn <= 2; turn++ {
		say(ir, message.RoleInput, turn, "PARENTQ")
		say(ir, message.RoleOutput, turn, "thinking about it")
		say(ir, message.RoleOutput, turn, "PARENT ANSWER")
	}

	for _, at := range []struct {
		name string
		lt   uint64
	}{
		{"head", 0},
		{"interior-turn2", 3}, // share turn 1, replace turn 2
	} {
		t.Run(at.name, func(t *testing.T) {
			child, _, err := b.ForkWith(parent, at.lt, patchSet(map[string]string{"aria_id": at.name}))
			if err != nil {
				t.Fatal(err)
			}
			cir, err := b.OpenFigIR(child)
			if err != nil {
				t.Fatal(err)
			}
			say(cir, message.RoleInput, 3, "CHILDQ-THE-BRANCH-QUESTION")
			say(cir, message.RoleOutput, 3, "CHILD ANSWER")

			// And the parent moves on, as it may: it is not frozen.
			say(ir, message.RoleInput, 3, "PARENTQ-AFTER-THE-FORK")
			say(ir, message.RoleOutput, 3, "PARENT ANSWER AFTER")

			// THE ASSERTION: nothing the child wrote is in the parent's log.
			pl, err := b.OpenFigIR(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range pl.Read() {
				for _, c := range e.Payload.Content {
					if strings.Contains(c.Text, "CHILDQ") {
						t.Fatalf("the parent's log holds the child's question at LT %d", e.LT)
					}
				}
			}
			// And the child holds the shared prefix plus its own, never the
			// parent's post-fork records.
			var childText []string
			for _, e := range cir.Read() {
				for _, c := range e.Payload.Content {
					childText = append(childText, fmt.Sprintf("%d:%s", e.LT, c.Text))
				}
			}
			var parentText []string
			for _, e := range pl.Read() {
				for _, c := range e.Payload.Content {
					parentText = append(parentText, fmt.Sprintf("%d:%s", e.LT, c.Text))
				}
			}
			t.Logf("parent log: %s", strings.Join(parentText, "|"))
			joined := strings.Join(childText, "|")
			if strings.Contains(joined, "PARENTQ-AFTER-THE-FORK") {
				t.Errorf("the child's log holds the parent's post-fork question: %s", joined)
			}
			t.Logf("child log: %s", joined)
		})
	}
}
