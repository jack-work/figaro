package store

import (
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// The memo never goes backward: Meta reads the sidecar with no lock held, so
// a reader can hold a file value a SetMeta has already superseded, and the
// CAS makes the late reader lose. The oracle is monotonicity, since a final
// equality passes for a cache that is always cold.
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

// The cold-read window is the one the CAS guards: between "nobody has
// published for this aria" and the first publish. A test that hammers one
// aria never reaches it, so this opens a fresh window per iteration.
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
