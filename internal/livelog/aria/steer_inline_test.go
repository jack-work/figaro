package aria

import (
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// A steer is an INLINE ANNOTATION, not a voice change.
//
// The user reported: "steering turns seems to wind up double printed, and they
// all get entire rows." A steering node carries Role input, so nodeVoice
// reported a voice change, so VoiceRunEnd closed the agent's run and opened an
// input run around the steer — giving it its own "❯ input" header AND its own
// pair of full-width rules, and cutting the agent's output into two blocks.
//
// Every surface that labels a unit derives its runs from VoiceRunEnd, so this
// one property governs incipit, the transcript pager and `fig show` alike.
func TestVoiceRunEnd_SteerDoesNotBreakTheAgentsRun(t *testing.T) {
	// The canonical shape from the user's own reference (/tmp/test): an
	// inquiry, then one agent run containing a tool, a steer, another tool
	// and the closing prose.
	nodes := []livedoc.Node{
		{Type: livedoc.NodeProse, Role: livedoc.RoleInput, Markdown: "do one, then sleep"},
		{Type: livedoc.NodeThinking, Markdown: "thinking"},
		{Type: livedoc.NodeTool, Name: "bash"},
		{Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "test"},
		{Type: livedoc.NodeTool, Name: "bash"},
		{Type: livedoc.NodeProse, Markdown: "Ecco fatto!"},
	}

	end, voice := VoiceRunEnd(nodes, 0)
	if end != 1 || voice != livedoc.RoleInput {
		t.Fatalf("inquiry run = (%d,%q), want (1,%q) — the inquiry IS a voice change",
			end, voice, livedoc.RoleInput)
	}

	end, voice = VoiceRunEnd(nodes, 1)
	if voice != livedoc.RoleOutput {
		t.Errorf("agent run voice = %q, want %q", voice, livedoc.RoleOutput)
	}
	if end != len(nodes) {
		t.Errorf("agent run ends at %d, want %d — the steer split the agent's run in two, "+
			"which is what gives it a header and a pair of rules it must not have",
			end, len(nodes))
	}
}

// A run that BEGINS with a steer must still be labelled sensibly rather than
// producing an empty or input-voiced run: the steer belongs to the agent's
// block, so the run speaks in the agent's voice.
func TestVoiceRunEnd_RunBeginningWithASteer(t *testing.T) {
	nodes := []livedoc.Node{
		{Type: livedoc.NodeSteering, Role: livedoc.RoleInput, Markdown: "steer first"},
		{Type: livedoc.NodeProse, Markdown: "reply"},
	}
	end, voice := VoiceRunEnd(nodes, 0)
	if end != 2 || voice != livedoc.RoleOutput {
		t.Fatalf("run = (%d,%q), want (2,%q)", end, voice, livedoc.RoleOutput)
	}
}
