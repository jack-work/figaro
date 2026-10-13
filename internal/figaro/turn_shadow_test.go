package figaro

import (
	"reflect"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// The old whole-message-copy derivations of the repair view, kept as an
// oracle: the hoisted assembly must produce the same repair input for the
// same event sequence.

// oldNoteAssistant is the former noteAssistant.
func oldNoteAssistant(t *turnState, m *message.Message) {
	t.assistantOracle = partialAssistant(m)
	t.toolsOracle = oldMergeTurnTools(toolsFromAssistant(t.assistantOracle), t.toolsOracle, t.states)
}

// oldMergeTurnTools is the former mergeTurnTools.
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

// busScript is one turn's events in drain-loop order.
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

// Each script runs through both paths; the aborted assistant and the tool list must match.
func TestTurnShadowEqualsTheDoubledStructure(t *testing.T) {
	for _, sc := range busScripts() {
		t.Run(sc.name, func(t *testing.T) {
			a := &Agent{turn: newTurnState()}
			s := newAsm(message.RoleOutput)
			for _, step := range sc.steps {
				step(a, s)
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
			if w, g := interruptedToolResults(wantTools), interruptedToolResults(gotTools); !reflect.DeepEqual(w, g) {
				t.Fatalf("interrupted results diverged\n want %+v\n got  %+v", w, g)
			}
		})
	}
}

// On evFigaro the drain notes the staged message, and that is what a failed append must preserve.
func TestTurnShadowPrefersTheDurablePayload(t *testing.T) {
	a := &Agent{turn: newTurnState()}
	s := newAsm(message.RoleOutput)
	s.addText(message.ContentProse, "streamed")
	a.turn.asm = s

	// Through the seam the drain loop uses: setting the field directly cannot see the wiring go missing.
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

// repairTurnTail runs from panic recovery, where nothing streamed.
func TestTurnShadowWithNoAssembly(t *testing.T) {
	ts := newTurnState()
	msg, tools := ts.repairView()
	if len(msg.Content) != 0 || len(tools) != 0 {
		t.Fatalf("empty turn produced %+v / %+v", msg, tools)
	}
}
