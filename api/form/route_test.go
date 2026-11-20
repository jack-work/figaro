package form

import "testing"

func TestARouteKeyIsTheKeyOrBelowItNeverItsParent(t *testing.T) {
	for key, want := range map[string]bool{
		"system.model":           true,
		"system.model.x":         true,
		"system":                 false,
		"system.models":          false,
		"system.thinking_effort": true,
	} {
		if got := IsRouteKey(key); got != want {
			t.Errorf("IsRouteKey(%q) = %v, want %v", key, got, want)
		}
	}
}
