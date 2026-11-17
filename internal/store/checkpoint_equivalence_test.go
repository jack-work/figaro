package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

// Everything a derivation reads from a form (each window's patches, the
// state at each stamp, Since) answers the same from a cold open off a
// checkpoint as from a replay of every record: the Deriver, formdelta and a
// retranslation of an old turn see one history either way.
func TestACheckpointedFormAnswersEveryWindowLikeAReplay(t *testing.T) {
	checkpointEvery(t, 64, 3)
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	id := mustNewAriaTB(t, b)
	for i := 0; i < 700; i++ {
		set := map[string]json.RawMessage{fmt.Sprintf("k%d", i%37): json.RawMessage(fmt.Sprint(i))}
		var drop []string
		if i%11 == 0 {
			drop = []string{fmt.Sprintf("k%d", (i+5)%37)}
		}
		if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, set, drop)); err != nil {
			t.Fatal(err)
		}
	}
	settleCheckpoints(t, b, id)
	b.Close()

	type answers struct{ windows, states, since []string }
	read := func(every int) answers {
		checkpointEvery(t, every, 3)
		b, err := NewXwalBackend(dir, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		f, err := b.form(id)
		if err != nil {
			t.Fatal(err)
		}
		_, head := f.Snapshot()
		var a answers
		for after := uint64(0); after < head; after += 13 {
			upTo := min(after+29, head)
			ps, err := f.PatchesBetween(after, upTo)
			raw, _ := json.Marshal(ps)
			a.windows = append(a.windows, fmt.Sprintf("%d-%d %v %s", after, upTo, err, raw))
			st, err := f.StateAt(upTo)
			a.states = append(a.states, fmt.Sprintf("%d %v %s", upTo, err, snapJSON(t, st)))
			s := f.Since(after)
			raw, _ = json.Marshal(s.Patches)
			a.since = append(a.since, fmt.Sprintf("%d %v %s", after, s.Delta, raw))
		}
		if every > 0 && f.Base().Version == 0 {
			t.Fatal("the checkpointed open did not start from a checkpoint")
		}
		return a
	}
	replay, ckpt := read(0), read(64)
	for i := range replay.windows {
		if replay.windows[i] != ckpt.windows[i] {
			t.Fatalf("window differs:\nreplay %.200s\nckpt   %.200s", replay.windows[i], ckpt.windows[i])
		}
		if replay.states[i] != ckpt.states[i] {
			t.Fatalf("state differs:\nreplay %.200s\nckpt   %.200s", replay.states[i], ckpt.states[i])
		}
		if replay.since[i] != ckpt.since[i] {
			t.Fatalf("since differs at %d", i)
		}
	}
}
