package cli

import (
	"fmt"
	"testing"
	"unsafe"

	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// PHASE 4, ASKED BEFORE IT IS DESIGNED: what would tree buy the COMPOSED
// layer that the turn cache does not already provide?
//
// There is one candidate and Gluck named it generational: forks do not share
// composed prefixes -- two branches of one trunk each compose the same history
// into separate node structs. Measured by IDENTITY, as phase 3 was, because
// heap is the wrong ruler and S3's acquittal already turned on this exact
// distinction (nodes COPY, they do not alias).
//
// PERMANENT, NOT MIGRATION SCAFFOLDING.

// The fixture must carry BOTH node kinds. S3 established that compose
// rebuilds a tool node's text (Split -> Join, and a tailBound) while prose
// passes through, so a fixture of prose alone measures the easy case and
// reports sharing that most composed bytes do not get.
func composeFixture(n int) []store.Entry[message.Message] {
	long := ""
	for i := 0; i < 400; i++ {
		long += fmt.Sprintf("tool output line %d, long enough to be worth sharing\n", i)
	}
	var out []store.Entry[message.Message]
	lt := uint64(0)
	add := func(role message.Role, turn uint64, c ...message.Content) {
		lt++
		out = append(out, store.Entry[message.Message]{
			FigaroLT: lt, LT: lt,
			Payload: message.Message{Role: role, TurnID: turn, Content: c},
		})
	}
	for i := 1; i <= n; i++ {
		turn := uint64(i)
		add(message.RoleInput, turn, message.TextContent(fmt.Sprintf("question %d, long enough to be worth sharing between branches", i)))
		// A tool node needs the INVOCATION as well as the result; a result
		// alone composes to nothing, which is how this fixture measured only
		// prose the first time.
		call := fmt.Sprintf("call-%d", i)
		add(message.RoleOutput, turn,
			message.TextContent(fmt.Sprintf("answer %d, also long enough to be worth sharing", i)),
			message.Content{Type: message.ContentToolInvoke, ToolCallID: call, ToolName: "bash",
				Arguments: map[string]any{"command": "echo hi"}})
		add(message.RoleInput, turn, message.ToolResultContent(call, "bash", long, false))
	}
	return out
}

// composeTurns is the projection the DAEMON runs (compose.Turns). The CLI
// no longer composes anything -- it renders what the api hands it -- so
// this measurement calls the composer directly rather than through a CLI
// helper that no longer exists.
func composeTurns(entries []store.Entry[message.Message]) []aria.Turn {
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	return compose.Turns(msgs)
}

// nodeTexts returns every non-empty string a node carries, since a tool node
// keeps its text somewhere other than Markdown.
func nodeTexts(n livedoc.Node) []string {
	var out []string
	for _, s := range []string{n.Markdown, n.Input, n.Output, n.Summary} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func sameString(a, b string) bool {
	return len(a) == len(b) && unsafe.StringData(a) == unsafe.StringData(b)
}

// Two branches composing the same prefix: does the composition MINT strings,
// or share the decoded IR's?
func TestTwoBranchesComposingOneHistoryDuplicateTheNodes(t *testing.T) {
	entries := composeFixture(40)

	// Both branches read the SAME decoded entries -- the best case for
	// sharing, since after a seed-at-open they genuinely would.
	a := composeTurns(entries)
	b := composeTurns(entries)
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("composed nothing; the fixture cannot show duplication")
	}

	// PROVE THE FIXTURE CAN FAIL. Prose passes through compose and aliases;
	// a tool node is REBUILT (S3: nodes copy, they do not alias). A fixture
	// of prose alone reports sharing that most composed bytes never get, and
	// this test produced exactly that answer twice before the invocation was
	// added -- a tool_result with no tool_use composes to nothing.
	kinds := map[string]int{}
	for _, tn := range a {
		for _, n := range tn.Nodes {
			kinds[string(n.Type)]++
		}
	}
	if kinds["prose"] == 0 || kinds["tool"] == 0 {
		t.Fatalf("fixture is not representative: node kinds %v, need both prose and tool", kinds)
	}

	var shared, minted, compared int
	for i := range a {
		if i >= len(b) || len(a[i].Nodes) != len(b[i].Nodes) {
			t.Fatalf("the two compositions disagree on SHAPE at turn %d", i)
		}
		for j := range a[i].Nodes {
			at, bt := nodeTexts(a[i].Nodes[j]), nodeTexts(b[i].Nodes[j])
			for k := range at {
				if at[k] != bt[k] {
					t.Fatalf("compositions disagree on CONTENT, a different bug")
				}
				compared++
				if sameString(at[k], bt[k]) {
					shared++
				} else {
					minted++
				}
			}
		}
	}
	if compared == 0 {
		t.Fatal("compared nothing; the fixture cannot prove anything")
	}
	t.Logf("two compositions of one history: %d strings compared, %d SHARED, %d MINTED",
		compared, shared, minted)

	if minted == 0 {
		t.Fatal("composition shares every string across branches; tree has no " +
			"sharing job in this layer either")
	}
}

// The seeding question, one layer up: does a shallow copy of a Turn share its
// nodes' strings, the way []Entry did downstairs?
func TestShallowCopyOfTurnsSharesNodeStrings(t *testing.T) {
	turns := composeTurns(composeFixture(40))
	if len(turns) == 0 {
		t.Fatal("composed nothing")
	}

	seeded := make([]aria.Turn, len(turns))
	copy(seeded, turns)

	compared := 0
	for i := range turns {
		for j := range turns[i].Nodes {
			at, st := nodeTexts(turns[i].Nodes[j]), nodeTexts(seeded[i].Nodes[j])
			for k := range at {
				compared++
				if !sameString(at[k], st[k]) {
					t.Fatalf("turn %d node %d: a shallow copy did NOT share the node string; "+
						"seeding a child's turn cache would cost a second composition", i, j)
				}
			}
		}
	}
	if compared == 0 {
		t.Fatal("compared nothing; the fixture cannot prove sharing")
	}
	t.Logf("shallow copy of %d turns: %d node strings compared, every one shared", len(turns), compared)
}
