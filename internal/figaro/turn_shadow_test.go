package figaro

import (
	"reflect"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// THE EQUIVALENCE ORACLE FOR THE TURN SHADOW.
//
// Before stage 5 the agent kept the in-flight turn in two places: `asm`
// (local to driveOneRound) and `turnState.assistant` + `.tools`, refreshed by
// a whole-message copy on EVERY bus event. The copy existed only because
// repairTurnTail runs at 8 sites, including panic recovery, where the local
// asm is out of scope.
//
// Stage 5 hoists the assembly onto the turn and materializes the repair view
// once, on failure. These are the old derivations, kept permanently: the
// claim is that the merged structure produces the same repair input for the
// same event sequence, and that is a fact about every day after the change.

// oldNoteAssistant is turn_repair.go's noteAssistant before stage 5.
func oldNoteAssistant(t *turnState, m *message.Message) {
	t.assistantOracle = partialAssistant(m)
	t.toolsOracle = oldMergeTurnTools(toolsFromAssistant(t.assistantOracle), t.toolsOracle, t.states)
}

// oldMergeTurnTools is mergeTurnTools before stage 5.
func oldMergeTurnTools(current, previous []turnTool, states map[string]turnTool) []turnTool {
	byID := make(map[string]turnTool, len(previous))
	for _, tool := range previous {
		byID[tool.ToolCallID] = tool
	}
	for i := range current {
		if prior, ok := byID[current[i].ToolCallID]; ok {
			current[i] = prior
		}
		if state, ok := states[current[i].ToolCallID]; ok {
			current[i] = state
		}
	}
	return current
}

// busScript is one turn's worth of events, in the order the drain loop sees
// them. It carries the shapes that actually move the shadow.
type busScript struct {
	name  string
	steps []func(a *Agent, s *asm)
}

func text(kind message.ContentType, body string) func(*Agent, *asm) {
	return func(a *Agent, s *asm) { s.addText(kind, body) }
}

func toolOpen(id, name string) func(*Agent, *asm) {
	return func(a *Agent, s *asm) {
		s.toolOpen(id, name)
		a.noteTool(id, name, "running", false)
	}
}

func toolReady(id, name string) func(*Agent, *asm) {
	return func(a *Agent, s *asm) {
		s.toolReady(id, name, map[string]any{"command": "true"})
	}
}

func toolDone(id, name, status, out string, isErr bool) func(*Agent, *asm) {
	return func(a *Agent, s *asm) { a.noteTool(id, name, status, isErr, out) }
}

func busScripts() []busScript {
	return []busScript{
		{"prose only", []func(*Agent, *asm){
			text(message.ContentThinking, "hmm"),
			text(message.ContentProse, "hello"),
		}},
		{"one tool, never ready", []func(*Agent, *asm){
			text(message.ContentProse, "calling"),
			toolOpen("c1", "bash"),
		}},
		{"one tool, ready, never finished", []func(*Agent, *asm){
			toolOpen("c1", "bash"), toolReady("c1", "bash"),
		}},
		{"one tool, finished ok", []func(*Agent, *asm){
			toolOpen("c1", "bash"), toolReady("c1", "bash"),
			toolDone("c1", "bash", "ok", "output here", false),
		}},
		{"one tool, finished error", []func(*Agent, *asm){
			toolOpen("c1", "bash"), toolReady("c1", "bash"),
			toolDone("c1", "bash", "error", "boom", true),
		}},
		{"two tools, one done one open", []func(*Agent, *asm){
			text(message.ContentProse, "two"),
			toolOpen("c1", "bash"), toolReady("c1", "bash"),
			toolOpen("c2", "bash"), toolReady("c2", "bash"),
			toolDone("c1", "bash", "ok", "first done", false),
		}},
		{"tool opened but args never decoded, sibling ready", []func(*Agent, *asm){
			toolOpen("c1", "bash"),
			toolOpen("c2", "bash"), toolReady("c2", "bash"),
		}},
		{"interleaved prose and tools", []func(*Agent, *asm){
			text(message.ContentProse, "a"),
			toolOpen("c1", "bash"), toolReady("c1", "bash"),
			text(message.ContentProse, "b"),
			toolOpen("c2", "bash"), toolReady("c2", "bash"),
			toolDone("c2", "bash", "ok", "second", false),
			text(message.ContentProse, "c"),
		}},
	}
}

// TestTurnShadowEqualsTheDoubledStructure drives each script through BOTH
// paths and requires the repair input to match: the aborted assistant message
// and the tool list interruptedToolResults consumes.
func TestTurnShadowEqualsTheDoubledStructure(t *testing.T) {
	for _, sc := range busScripts() {
		t.Run(sc.name, func(t *testing.T) {
			a := &Agent{turn: newTurnState()}
			s := newAsm(message.RoleOutput)
			for _, step := range sc.steps {
				step(a, s)
				// The old path refreshed the shadow on EVERY event.
				oldNoteAssistant(a.turn, s.message())
			}
			a.turn.asm = s

			wantMsg, wantTools := a.turn.assistantOracle, a.turn.toolsOracle
			gotMsg, gotTools := a.turn.repairView()

			if !reflect.DeepEqual(wantMsg, gotMsg) {
				t.Fatalf("assistant diverged\n want %+v\n got  %+v", wantMsg, gotMsg)
			}
			if !reflect.DeepEqual(wantTools, gotTools) {
				t.Fatalf("tools diverged\n want %+v\n got  %+v", wantTools, gotTools)
			}
			// And what the repair actually writes.
			if w, g := interruptedToolResults(wantTools), interruptedToolResults(gotTools); !reflect.DeepEqual(w, g) {
				t.Fatalf("interrupted results diverged\n want %+v\n got  %+v", w, g)
			}
		})
	}
}

// The durable payload overrides the assembly: on evFigaro the drain notes the
// STAGED message, and that is what a failed append must preserve.
func TestTurnShadowPrefersTheDurablePayload(t *testing.T) {
	a := &Agent{turn: newTurnState()}
	s := newAsm(message.RoleOutput)
	s.addText(message.ContentProse, "streamed")
	a.turn.asm = s

	// Through the seam the drain loop uses, not by setting the field: a test
	// that assigns turn.final directly cannot see the wiring go missing, and
	// a canary that removed it PASSED against exactly that version.
	staged := store.Entry[message.Message]{Payload: message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{{Type: message.ContentProse, Text: "durable"}},
		StopReason: message.StopEnd}}
	a.stageAssistant(&staged)

	got, _ := a.turn.repairView()
	if len(got.Content) != 1 || got.Content[0].Text != "durable" {
		t.Fatalf("repair view used the assembly, not the staged payload: %+v", got)
	}
}

// The shadow must survive having no assembly at all -- repairTurnTail runs
// from panic recovery, where nothing streamed.
func TestTurnShadowWithNoAssembly(t *testing.T) {
	ts := newTurnState()
	msg, tools := ts.repairView()
	if len(msg.Content) != 0 || len(tools) != 0 {
		t.Fatalf("empty turn produced %+v / %+v", msg, tools)
	}
}
