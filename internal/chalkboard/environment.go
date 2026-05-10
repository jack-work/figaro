package chalkboard

import (
	"encoding/json"
	"os"
	"strings"
)

// EnvironmentAllowlist names the process env vars that get reflected
// into the chalkboard at aria bootstrap under
// `system.environment.<lower(name)>`. Values are captured as plain
// strings. Adding a name here makes the value available to providers
// and tools through the snapshot without further plumbing — opt-in
// keeps the chalkboard from picking up arbitrary process state.
//
// Bootstrap is one-shot: once an aria has its first patch, future
// env-var changes don't propagate. Override at runtime with
// `figaro set system.environment.<key> "<value>"`.
var EnvironmentAllowlist = []string{
	"FIGARO_WIRE_DIR",
}

// EnvironmentPatch reads the allowlisted env vars and returns a
// chalkboard patch that sets `system.environment.<lower>` for each
// non-empty value. Unset/empty vars are skipped (no Remove —
// bootstrap doesn't unset stale state that may have been set by the
// user explicitly).
func EnvironmentPatch() Patch {
	var set map[string]json.RawMessage
	for _, name := range EnvironmentAllowlist {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			continue
		}
		if set == nil {
			set = map[string]json.RawMessage{}
		}
		set["system.environment."+strings.ToLower(name)] = raw
	}
	if set == nil {
		return Patch{}
	}
	return Patch{Set: set}
}
