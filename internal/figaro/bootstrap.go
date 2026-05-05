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

// bootstrapIfNeeded runs the Scribe once on a fresh aria and emits
// a state-only tic with the system.* patch. Idempotent — restored
// arias whose chalkboard already has system.prompt skip.
func (a *Agent) bootstrapIfNeeded(model string) {
	if a.chalkboard == nil || a.scribe == nil {
		return
	}
	if _, ok := a.chalkboard.Snapshot()["system.prompt"]; ok {
		return
	}
	// agent:
	// credo should be assembled probably here.
	// client might have already assembled it.  Fine honestly.
	// We'll want to reinvoke the Outfitter (the loadout-loader) to get the config..
	// /home/gluck/.config/figaro/loadouts/config.toml <-- for more context
	// The system prompt would come out of that.  That property would be special.
	// The system prompt should also be templated with any of the chalkboard values.
	// all should be in scope.  In fact, it is only the values in the chalkboard that
	// should be tolerated in the credo at all.
	//
	// The credo is a special case chalkboard value that may have other chalkboard values
	// embedded in it.  It is the only one.  Prompt would be specified in system.credo value.
	// Build from that
	prompt, err := a.scribe.Build(credo.CurrentContext(a.prov.Name(), a.id))
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap credo build: %v\n", a.id, err)
		return
	}

	// the whole of the chalkboard should be assembled here from the loadout.
	// but then, the values passed through args should patched on top, and those only
	// known at runtime.

	patch := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	// credo can be set to credo for now.  anthropic provider can emit it as prompt
	// if need be.
	patch.Set2("system.prompt", prompt)
	// model should already be there, and if it's not the provider code can handle it.
	patch.Set2("system.model", model)
	// I'm not sure why the provider needs to be set.  Try removing it, see what happens.
	patch.Set2("system.provider", a.prov.Name())

	// Skills would be set on the loadout directly, but do, along with credo, have a default
	// These defaults I think should be specified with a global default config checked into
	// the codebase with a binary and set to the global defaults.  These are variably specified in
	// /home/gluck/.config/figaro/loadouts/config.toml
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
	// Agent:
	// We should reconsider whether we even need a bootstrap event, or whether we can just apply the patches
	// on the first user message sent. Keep it for now but raise this issue for me later.
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
