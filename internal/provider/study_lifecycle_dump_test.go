package provider_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// TestStudyLifecycleDump prints, verbatim, what a figaro is shown at every
// lifecycle event of a studied form: the moment observation begins, each
// window in which the form moved, a window in which it did not, the form
// being deleted while observed, and the drop.
//
// It is a dump, not an assertion: the assertions live elsewhere. This exists
// so the rendered surface can be read as a whole, which is the only way to
// see the traps in it (two blocks that differ only by a version number, an
// intermediate value inside one window, a baseline rendered as a change).
func TestStudyLifecycleDump(t *testing.T) {
	f := store.NewMemForm()
	defer f.Close()

	set := func(kv map[string]string) {
		p := message.Patch{Set: map[string]json.RawMessage{}}
		for k, v := range kv {
			b, _ := json.Marshal(v)
			p.Set[k] = b
		}
		if _, err := f.Apply(p, 0); err != nil {
			t.Fatal(err)
		}
	}
	// The form's life before anyone watches it.
	set(map[string]string{"brief": "ship the thing", "owner": "gluck"})
	set(map[string]string{"status": "open"})
	v0 := f.Read().Version

	fid := "@abc123"
	acc := &memAccessor{f: f}
	studies := map[string]provider.Form{fid: acc}

	log := store.NewMemLog[message.Message]()

	// 1. STUDY BEGINS. The mark, stamped at the form's current version.
	_, _ = log.Append(store.Entry[message.Message]{
		Payload:       message.Message{Role: message.RoleInput, Study: &message.StudyMark{FormID: fid, Began: true}},
		StudyVersions: map[string]uint64{fid: v0},
	})

	// 2. THE FORM MOVES, twice inside one window.
	set(map[string]string{"status": "in review"})
	set(map[string]string{"status": "merged", "sha": "8b12f128"})
	_, _ = log.Append(store.Entry[message.Message]{
		Payload:       message.Message{Role: message.RoleInput},
		StudyVersions: map[string]uint64{fid: f.Read().Version},
	})

	// 3. THE FORM DOES NOT MOVE.
	_, _ = log.Append(store.Entry[message.Message]{
		Payload:       message.Message{Role: message.RoleInput},
		StudyVersions: map[string]uint64{fid: f.Read().Version},
	})

	// 4. A KEY IS REMOVED.
	if _, err := f.Apply(message.Patch{Remove: []string{"owner"}}, 0); err != nil {
		t.Fatal(err)
	}
	_, _ = log.Append(store.Entry[message.Message]{
		Payload:       message.Message{Role: message.RoleInput},
		StudyVersions: map[string]uint64{fid: f.Read().Version},
	})

	// 5. THE FORM CEASES TO EXIST while observed: stamped, no accessor.
	_, _ = log.Append(store.Entry[message.Message]{
		Payload:       message.Message{Role: message.RoleInput},
		StudyVersions: map[string]uint64{"@gone99": 7},
	})

	// 6. DROP.
	_, _ = log.Append(store.Entry[message.Message]{
		Payload: message.Message{Role: message.RoleInput, Study: &message.StudyMark{FormID: fid, Began: false}},
	})

	labels := []string{
		"1. STUDY BEGINS",
		"2. THE FORM MOVED (two patches in one window)",
		"3. THE FORM DID NOT MOVE",
		"4. A KEY WAS REMOVED",
		"5. THE FORM WAS DELETED WHILE OBSERVED",
		"6. DROP",
	}
	i := 0
	cfg := provider.CatchUpConfig{
		Log:     log,
		Rows:    store.NewMemLog[[]json.RawMessage](),
		Studies: studies,
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			texts := provider.StudyReminderTexts(m, form.Snapshot{})
			fmt.Printf("\n──── %s ────\n", labels[i])
			i++
			if len(texts) == 0 {
				fmt.Println("(nothing rendered)")
			}
			for _, s := range texts {
				fmt.Println(s)
			}
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
	}
	if _, err := provider.CatchUp(cfg); err != nil {
		t.Fatal(err)
	}
	fmt.Println()
}

type memAccessor struct{ f *store.Form }

func (a *memAccessor) PatchesBetween(after, upTo uint64) []message.Patch {
	ps := a.f.PatchesBetween(after, upTo)
	out := make([]message.Patch, len(ps))
	for i := range ps {
		out[i] = ps[i].Patch
	}
	return out
}
