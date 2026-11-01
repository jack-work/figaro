package form

import "testing"

func TestOwnerHeldCoversEachKeyAndWhatIsUnderIt(t *testing.T) {
	for path, want := range map[string]string{
		"system.provider":                "system.provider",
		"system.model":                   "system.model",
		"system.max_tokens":              "system.max_tokens",
		"system.max_context_tokens":      "system.max_context_tokens",
		"system.credo.content":           "system.credo",
		"system.tools":                   "system.tools",
		"system.tools[0]":                "system.tools",
		"system.methods.pick.key":        "system.methods",
		"system.model_hint":              "",
		"system.thinking_effort":         "",
		"system.decide.provider":         "",
		"system.credos":                  "",
		"system.max_tokens_per_tool_out": "",
	} {
		got, held := OwnerHeld(path)
		if got != want || held != (want != "") {
			t.Errorf("OwnerHeld(%q) = %q, %v; want %q", path, got, held, want)
		}
	}
}
