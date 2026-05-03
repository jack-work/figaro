package figaro

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/credo"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// bootstrapIfNeeded runs the Scribe once on a fresh aria and emits a
// state-only tic carrying the system.* patch. Idempotent on restored
// arias whose chalkboard already has system.prompt.
func (a *Agent) bootstrapIfNeeded(model string) {
	if a.chalkboard == nil || a.scribe == nil {
		return
	}
	if _, ok := a.chalkboard.Snapshot()["system.prompt"]; ok {
		return
	}
	prompt, err := a.scribe.Build(credo.CurrentContext(a.prov.Name(), a.id))
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap credo build: %v\n", a.id, err)
		return
	}

	patch := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	patch.Set2("system.prompt", prompt)
	patch.Set2("system.model", model)
	patch.Set2("system.provider", a.prov.Name())

	if skills, err := a.scribe.Skills(); err == nil && len(skills) > 0 {
		if b, err := json.Marshal(skillCatalog(skills)); err == nil {
			patch.Set["system.skills"] = b
		}
	}

	tic := message.Message{
		Role:      message.RoleUser,
		Patches:   []message.Patch{patch},
		Timestamp: time.Now().UnixMilli(),
	}
	if _, err := a.figStream.Append(store.Entry[message.Message]{Payload: tic}, true); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap append tic: %v\n", a.id, err)
		return
	}
	a.chalkboard.Apply(patch)
	if err := a.chalkboard.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap chalkboard save: %v\n", a.id, err)
	}
}

func skillCatalog(skills []credo.Skill) []credo.SkillCatalogEntry {
	out := make([]credo.SkillCatalogEntry, len(skills))
	for i, s := range skills {
		out[i] = credo.SkillCatalogEntry{
			Name: s.Name, Description: s.Description, FilePath: s.FilePath,
		}
	}
	return out
}
