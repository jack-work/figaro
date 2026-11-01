package figaro

import (
	"encoding/json"
	"log/slog"

	"github.com/jack-work/figaro/api/form"
)

const toolsKey = "system.tools"

type toolAllowance struct {
	all   bool
	names map[string]bool
}

func (t toolAllowance) allows(name string) bool { return t.all || t.names[name] }

func toolsAllowed(snap form.Snapshot) toolAllowance {
	raw, ok := snap.Get(toolsKey)
	if !ok {
		return toolAllowance{all: true}
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		slog.Warn("system.tools is not a list of tool names: no tools are allowed", "value", string(raw), "err", err)
		return toolAllowance{}
	}
	out := toolAllowance{names: make(map[string]bool, len(names))}
	for _, n := range names {
		out.names[n] = true
	}
	return out
}
