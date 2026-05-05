package figaro

import (
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/store"
)

// bootstrapIfNeeded runs the Outfitter's second-phase outfit on a
// fresh aria — templates the credo into system.prompt, builds the
// skill catalog at system.skills — and emits a state-only tic with
// the resulting patch. Idempotent: when system.prompt is already on
// the chalkboard the Outfitter returns an empty patch and no tic is
// written.
func (a *Agent) bootstrapIfNeeded() {
	if a.chalkboard == nil || a.outfitter == nil {
		return
	}
	patch, err := a.outfitter.Bootstrap(a.chalkboard.Snapshot(),
		outfit.CurrentBootCtx(a.prov.Name(), a.id))
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: bootstrap: %v\n", a.id, err)
		return
	}
	if patch.IsEmpty() {
		return
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
