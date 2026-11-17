package figaro

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

func invoke(id, name string, args map[string]interface{}) message.Content {
	return message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name, Arguments: args}
}

func outcomeText(oc toolOutcome) string { return toolOutcomeText(oc) }

func TestARecordedCallGetsItsRecordedAnswerInOrder(t *testing.T) {
	status := map[string]interface{}{"command": "git status"}
	turn := []message.Message{
		{Role: message.RoleOutput, Content: []message.Content{invoke("a", "bash", status), invoke("b", "read", map[string]interface{}{"path": "x", "limit": 5.0})}},
		{Role: message.RoleInput, Content: []message.Content{
			message.ToolResultContent("a", "bash", "clean", false),
			message.ToolResultContent("b", "read", "no such file", true),
		}},
		{Role: message.RoleOutput, Content: []message.Content{invoke("c", "bash", status)}},
		{Role: message.RoleInput, Content: []message.Content{message.ToolResultContent("c", "bash", "one change", false)}},
	}
	tape := recordTape(turn)

	for _, want := range []string{"clean", "one change", "one change"} {
		oc, ok := tape.take(invoke("z", "bash", map[string]interface{}{"command": "git status"}))
		if !ok || outcomeText(oc) != want {
			t.Fatalf("got %q (%v), want %q", outcomeText(oc), ok, want)
		}
	}
	oc, ok := tape.take(invoke("y", "read", map[string]interface{}{"limit": 5.0, "path": "x"}))
	if !ok || !oc.isErr || outcomeText(oc) != "no such file" {
		t.Fatalf("argument order changed the match: %+v %v", oc, ok)
	}
	if _, ok := tape.take(invoke("w", "read", map[string]interface{}{"path": "x", "limit": 6.0})); ok {
		t.Fatal("different arguments matched")
	}
}

func TestAnUnrecordedCallIsStubbedNeverRun(t *testing.T) {
	for _, tc := range []struct {
		call  message.Content
		want  string
		isErr bool
	}{
		{invoke("1", "bash", map[string]interface{}{"command": "env -u X figaro send -f @someone -- done"}), "(no output)", false},
		{invoke("2", "bash", map[string]interface{}{"command": "echo hi > notes.md"}), "(no output)", false},
		{invoke("3", "bash", map[string]interface{}{"command": "git -c a=b push keel main"}), "(no output)", false},
		{invoke("4", "bash", map[string]interface{}{"command": "ls -la 2>&1 | head"}), "(no output)\n\nCommand exited with code 1", false},
		{invoke("5", "write", map[string]interface{}{"path": "a.go", "content": "x"}), "a.go", false},
		{invoke("6", "edit", map[string]interface{}{"path": "b.go"}), "b.go", false},
		{invoke("7", "read", map[string]interface{}{"path": "c.go"}), "Error: resource temporarily unavailable", true},
	} {
		oc := declaredStub(tc.call)
		if outcomeText(oc) != tc.want || oc.isErr != tc.isErr {
			t.Errorf("%v: got %q err=%v, want %q err=%v", tc.call.Arguments, outcomeText(oc), oc.isErr, tc.want, tc.isErr)
		}
	}
}

func TestOutwardCommands(t *testing.T) {
	for cmd, want := range map[string]bool{
		"figaro set --id x mantra y":     true,
		"/usr/bin/fig new -f -- go":      true,
		"cd /tmp && rm -rf build":        true,
		"git commit -m wip":              true,
		"git log --oneline -3":           false,
		"sed -i s/a/b/ f":                true,
		"grep -rn foo . >/dev/null":      false,
		"go test ./... 2>&1 | tail":      false,
		"cat go.mod":                     false,
		"curl -s https://example.com":    true,
		"printf '%s' x >> log.txt":       true,
		"rg -n 'figaro' internal | head": false,
	} {
		if got := outwardCommand(cmd); got != want {
			t.Errorf("%q: outward=%v, want %v", cmd, got, want)
		}
	}
}
