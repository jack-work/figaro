package form

import "testing"

func TestOwnerHeldCoversEachKeyAndWhatIsUnderIt(t *testing.T) {
	for path, want := range map[string]string{
		"system.provider":                    "system.provider",
		"system.model":                       "system.model",
		"system.max_tokens":                  "system.max_tokens",
		"system.max_context_tokens":          "system.max_context_tokens",
		"system.credo.content":               "system.credo",
		"system.tools":                       "system.tools",
		"system.tools[0]":                    "system.tools",
		"system.methods.pick.key":            "system.methods",
		"system.environment.figaro_wire_dir": "system.environment",
		"system.decide.base_url":             "system.decide.base_url",
		"system.model_hint":                  "",
		"system.thinking_effort":             "",
		"system.decide.provider":             "",
		"system.credos":                      "",
		"system.max_tokens_per_tool_out":     "",
		"system.ttl":                         "system.ttl",
		"system.reap_if_empty":               "system.reap_if_empty",
		"system.ttl_note":                    "",
		"system.keepalive":                   "system.keepalive",
		"system.retired":                     "system.retired",
		"system.keep":                        "system.keep",
		"system.keepalive_note":              "",
	} {
		got, held := OwnerHeld(path)
		if got != want || held != (want != "") {
			t.Errorf("OwnerHeld(%q) = %q, %v; want %q", path, got, held, want)
		}
	}
}
