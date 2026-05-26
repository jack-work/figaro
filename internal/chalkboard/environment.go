package chalkboard

import (
	"encoding/json"
	"os"
	"strings"
)

// EnvironmentAllowlist is the set of env vars captured into the
// chalkboard. Read by the CLI on every prompt (so the agent sees
// the caller's current env) and also on the first turn in the agent
// (fallback for vars set before the daemon started).
var EnvironmentAllowlist = []string{
	"FIGARO_WIRE_DIR",
}

// EnvironmentSnapshot returns the allowlisted env vars as a
// chalkboard snapshot map (key = "system.environment.<lower_name>",
// value = JSON string). Used by the CLI to send env vars with
// each prompt so the agent always has the caller's current values.
func EnvironmentSnapshot() map[string]json.RawMessage {
	snap := map[string]json.RawMessage{}
	for _, name := range EnvironmentAllowlist {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			continue
		}
		snap["system.environment."+strings.ToLower(name)] = raw
	}
	return snap
}

// EnvironmentPatch returns a patch with allowlisted env vars.
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
