package cli

import "testing"

func TestSetFlagKeepsARouteQualifiedModelWhole(t *testing.T) {
	for _, model := range []string{"anthropic-kic/claude-opus-5-5", "explabs/glm-5.3", "openrouter/x-ai/grok-4.1"} {
		d, err := parseDress("", "system.provider=rou2,system.model="+model, "")
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		e, ok := d.patch.Entry("system.model")
		if !ok || string(e.New) != `"`+model+`"` {
			t.Errorf("-S system.model=%s gave %s", model, e.New)
		}
	}
}
