package store

import (
	"sync"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// THE MEMO NEVER GOES BACKWARD. Meta reads the sidecar with NO LOCK HELD when
// nothing has been published yet, so a reader can be holding a file value that
// a SetMeta has already superseded. The CAS is what makes that safe: the late
// reader loses and discards its own. Without it the memo would serve a value
// older than one the same process had already returned -- the exact shape of
// the mirror race the cast fix fought (8bc497a1), one layer down.
//
// The oracle is MONOTONICITY rather than a final equality, because a final
// equality passes for a cache that is simply always cold.
func TestMetaMemoNeverGoesBackward(t *testing.T) {
	be, id := realAria(t, 2, 16)

	const writes = 200
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= writes; i++ {
			if err := be.SetMeta(id, &AriaMeta{TokensIn: i}); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()

	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen := 0
			for i := 0; i < 2000; i++ {
				got, err := be.Meta(id)
				if err != nil {
					t.Errorf("read: %v", err)
					return
				}
				if got == nil {
					continue
				}
				if got.TokensIn < seen {
					t.Errorf("the memo went backward: saw %d after %d", got.TokensIn, seen)
					return
				}
				seen = got.TokensIn
			}
		}()
	}
	wg.Wait()

	final, err := be.Meta(id)
	if err != nil || final == nil {
		t.Fatalf("final read: %v %v", final, err)
	}
	if final.TokensIn != writes {
		t.Fatalf("final memo = %d, want the last write %d", final.TokensIn, writes)
	}
}

// The observed set is published whole and read on every IR append. Declaring
// from several goroutines must not lose a declaration, which is the one thing
// a copy-on-write map can get wrong.
func TestObservedDeclarationsDoNotLoseEachOther(t *testing.T) {
	be, _ := realAria(t, 1, 16)
	st := be.Store()

	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			st.SetObservedForms(id, []string{"form-" + id})
		}(id)
	}
	wg.Wait()

	got := *st.observed.Load()
	if len(got) != len(ids) {
		t.Fatalf("declared %d arias, the published set holds %d: a successor overwrote a sibling",
			len(ids), len(got))
	}
	for _, id := range ids {
		if forms := got[id]; len(forms) != 1 || forms[0] != "form-"+id {
			t.Fatalf("aria %s observes %v", id, forms)
		}
	}
}

// THE COLD-READ WINDOW, WHICH IS THE ONE THE CAS ACTUALLY GUARDS. The window
// exists only between "nobody has published for this aria yet" and the first
// publish, so a test that hammers ONE aria never reaches it -- the monotonicity
// test above passes with the CAS replaced by a plain Store, and that passing
// canary is what sent me here.
//
// This opens a fresh window per iteration: a value on disk, a cold cache, and
// a reader racing a writer. If the reader may publish what it read, the memo
// ends holding the SUPERSEDED value.
func TestAColdReadCannotOverwriteAWriteThatBeatIt(t *testing.T) {
	be, _ := realAria(t, 1, 16)

	for i := 0; i < 300; i++ {
		outfit, err := be.CreateOutfit("l", message.Patch{})
		if err != nil {
			t.Fatal(err)
		}
		id, err := be.CreateConversation(outfit)
		if err != nil {
			t.Fatal(err)
		}
		// On disk, and NOT in the memo: written through a backend that is
		// then thrown away, so this process has never published for this id.
		if err := writeJSON(be.metaPath(id), &AriaMeta{TokensIn: 1}); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = be.Meta(id) }()
		go func() { defer wg.Done(); _ = be.SetMeta(id, &AriaMeta{TokensIn: 2}) }()
		wg.Wait()

		got, err := be.Meta(id)
		if err != nil || got == nil {
			t.Fatalf("iteration %d: %v %v", i, got, err)
		}
		if got.TokensIn != 2 {
			t.Fatalf("iteration %d: the memo holds %d, the write that finished last wrote 2",
				i, got.TokensIn)
		}
	}
}
