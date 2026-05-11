package chalkboard

import (
	"encoding/json"
	"os"
	"strings"
)

// EnvironmentAllowlist is the set of env vars captured into the
// chalkboard at bootstrap (one-shot).
var EnvironmentAllowlist = []string{
	"FIGARO_WIRE_DIR",
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
