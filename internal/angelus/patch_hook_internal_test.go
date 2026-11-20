package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authn"
)

// A rule in everyWriteRules runs before any owner shortcut: the owner's
// shape-only patch, the owner's open context and the judged list all meet
// it. Rules that hold for every writer, the owner included (a log's kind is
// fixed at birth), belong there.
func TestEveryWriteRuleRunsBeforeTheOwnerShortcut(t *testing.T) {
	hs, _, _ := demoteHandlers(t)
	refused := errors.New("refused for every writer")
	was := everyWriteRules
	everyWriteRules = []patchRule{{check: func(*patchCheck) error { return refused }}}
	t.Cleanup(func() { everyWriteRules = was })
	owner := authn.WithDoor(context.Background(), authn.DoorInProcess)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"m"`)}, nil)
	for _, mode := range []patchMode{patchJudged, patchOwnerShapes, patchOwnerContext} {
		if err := hs.h.judgeWrite(owner, formWrite{before: form.Snapshot{}, patch: &patch, mode: mode}); !errors.Is(err, refused) {
			t.Errorf("mode %d: the owner's write passed a rule for every writer: %v", mode, err)
		}
	}
	everyWriteRules = was
	for _, mode := range []patchMode{patchJudged, patchOwnerShapes, patchOwnerContext} {
		if err := hs.h.judgeWrite(owner, formWrite{before: form.Snapshot{}, patch: &patch, mode: mode}); err != nil {
			t.Errorf("canary, mode %d: the owner's plain write is refused without the rule: %v", mode, err)
		}
	}
}
