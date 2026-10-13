package form

import (
	"encoding/json"
	"os"
	"strings"
)

// EnvironmentAllowlist is the env vars captured into the form on every prompt.
var EnvironmentAllowlist = []string{
	"FIGARO_WIRE_DIR",
}

// EnvironmentSnapshot keys are "system.environment.<lower_name>".
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
	return Build(Snapshot{}, set, nil)
}
