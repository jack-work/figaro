package xwal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A store written before a channel became unkeyed kept it keyed forever:
// the manifest is authoritative once a store exists, and nothing
// reconciled it. So the unkeyed design reached stores created by this
// build and no store anyone already had.
//
// The symptom, which is how a fuzz worker found it from the outside: a
// board patch written with no turn in flight is keyed ABOVE the tail, so a
// tail fork's child does not inherit it although the parent has it. The
// contract in channelBases says the opposite, in as many words.
func TestAKeyedChannelBecomesUnkeyedAndATailForkInheritsAgain(t *testing.T) {
	dir := t.TempDir()
	// A store from before: nothing declared unkeyed.
	old := testStoreOptions()
	s, err := OpenStore(dir, old)
	if err != nil {
		t.Fatal(err)
	}
	trunk, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(string(trunk), "ir", 0, []byte(`{"turn":1}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if m, err := readManifestFile(dir); err != nil {
		t.Fatal(err)
	} else if chanSpec(m, "chalkboard").Unkeyed {
		t.Fatal("fixture is not an old store: its chalkboard is already unkeyed")
	}

	// This build declares it unkeyed.
	now := testStoreOptions()
	now.Unkeyed = []string{"chalkboard"}
	s2, err := OpenStore(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if m, err := readManifestFile(dir); err != nil {
		t.Fatal(err)
	} else if !chanSpec(m, "chalkboard").Unkeyed {
		t.Fatal("opening with the new declaration left the channel keyed on disk")
	}

	// A patch with NO turn in flight, then a tail fork. The child must
	// start from the board the parent declared, patch included.
	if _, err := s2.Append(string(trunk), "chalkboard", 0, []byte(`{"k":"v"}`), nil); err != nil {
		t.Fatal(err)
	}
	child, err := s2.Fork(string(trunk), 0) // 0 == at the tail
	if err != nil {
		t.Fatal(err)
	}
	recs, err := s2.RecordsFrom(child, "chalkboard", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("the child inherited NO board records")
	}
	found := false
	for _, r := range recs {
		if strings.Contains(string(r.Payload), `"k"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("the child inherited %d board records but not the one written since the last turn", len(recs))
	}
}

// The other direction has no honest answer, so it is refused rather than
// guessed: records already written carry no key, and reading them as keyed
// reads every one of them as key 0.
func TestAnUnkeyedChannelIsNotSilentlyMadeKeyedAgain(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.Unkeyed = []string{"chalkboard"}
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, testStoreOptions()); err == nil ||
		!strings.Contains(err.Error(), "unkeyed on disk") {
		t.Fatalf("re-keying an unkeyed channel: got %v, want a refusal", err)
	}
}

func chanSpec(m manifest, name string) manifestChannel {
	for _, c := range m.Channels {
		if c.Name == name {
			return c
		}
	}
	return manifestChannel{}
}

// A channel named ONLY in Unkeyed -- not a reducer, not opaque -- was
// declared in no ChannelSpec at all, so reconciliation could not see it and
// an existing store kept it keyed forever. The new-store path hid it:
// autoCreateChannel reads the options directly.
func TestAnUnkeyedOnlyChannelIsDeclaredAndReconciled(t *testing.T) {
	dir := t.TempDir()
	old := testStoreOptions()
	s, err := OpenStore(dir, old)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "ir", 0, []byte(`{"turn":1}`), nil); err != nil {
		t.Fatal(err)
	}
	// A plain log channel, born keyed because nothing said otherwise.
	if _, err := s.Append(string(tr), "notes", 1, []byte(`{"n":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	now := testStoreOptions()
	now.Unkeyed = []string{"notes"} // neither a reducer nor opaque
	s2, err := OpenStore(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	m, err := readManifestFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !chanSpec(m, "notes").Unkeyed {
		t.Error("an unkeyed-only channel stayed keyed on disk: it was declared nowhere")
	}
}

// Opaque drifts too, and safely: the decoder keys off the frame, so a
// channel that changes its mind reads back mixed records correctly.
func TestOpaqueReconcilesAndMixedRecordsStillRead(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "ir", 0, []byte(`{"turn":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "notes", 1, []byte(`{"plain":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	now := testStoreOptions()
	now.Opaque = []string{"notes"}
	s2, err := OpenStore(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if m, err := readManifestFile(dir); err != nil {
		t.Fatal(err)
	} else if !chanSpec(m, "notes").Opaque {
		t.Fatal("the channel did not become opaque on disk")
	}
	if _, err := s2.Append(string(tr), "notes", 1, []byte(`{"encoded":1}`), nil); err != nil {
		t.Fatal(err)
	}
	recs, err := s2.RecordsFrom(string(tr), "notes", 0, 0)
	if err != nil {
		t.Fatalf("reading a channel with records from both sides of the flip: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("read %d records, want 2", len(recs))
	}
	for _, r := range recs {
		if !strings.Contains(string(r.Payload), `1}`) {
			t.Errorf("record decoded wrong across the flip: %q", r.Payload)
		}
	}
}

// Kind and Reducer drift the same way as Unkeyed and Opaque, and are the
// one case that must REFUSE rather than adopt: folding records that were
// never patches is not a reconciliation, and dropping a fold abandons
// state readers depend on. Neither has a converter.
func TestAChannelsKindIsRefusedRatherThanReinterpreted(t *testing.T) {
	dir := t.TempDir()
	plain := testStoreOptions()
	delete(plain.Reducers, "chalkboard") // born a plain log
	s, err := OpenStore(dir, plain)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "ir", 0, []byte(`{"turn":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "chalkboard", 1, []byte(`{"not":"a patch"}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if m, err := readManifestFile(dir); err != nil {
		t.Fatal(err)
	} else if chanSpec(m, "chalkboard").Kind != "log" {
		t.Fatalf("fixture channel is %q, want log", chanSpec(m, "chalkboard").Kind)
	}

	// Now the caller calls it reducible. Silently adopting that would run
	// a fold over records that were never patches.
	_, err = OpenStore(dir, testStoreOptions())
	if err == nil || !strings.Contains(err.Error(), "kind cannot be reinterpreted") {
		t.Fatalf("reinterpreting a channel's kind: got %v, want a refusal", err)
	}
}

// Deleting the manifest from a store that still holds data used to be
// TOTAL SILENT LOSS, and by the shortest possible route: the failing open
// wrote a fresh manifest on its way out, stamped with this build's layout.
// NeedsFlatten then answered no on a NESTED store, the migration never
// ran, and the next open reported zero arias at exit 0 with every node
// untouched on disk. The layout gate cannot help when the stamp is written
// before anything inspects the shape.
func TestAManifestIsNotInventedForAStoreThatHasData(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "ir", 0, []byte(`{"turn":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, manifestName)); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := OpenStore(dir, testStoreOptions())
		if err == nil {
			t.Fatalf("open %d succeeded on a store with no manifest", attempt)
		}
		if !strings.Contains(err.Error(), "refusing to invent") {
			t.Fatalf("open %d: %v", attempt, err)
		}
		// And it must not have written one, or the SECOND open would take
		// the fresh-store path and report an empty forest.
		if _, statErr := os.Stat(filepath.Join(dir, manifestName)); !os.IsNotExist(statErr) {
			t.Fatalf("open %d wrote a manifest while refusing (err %v)", attempt, statErr)
		}
	}
}

// The refusal above must test EMPTINESS, not existence. A bare main
// directory -- a botched copy, an interrupted restore, a caller that
// mkdir'd ahead -- has nothing to protect, and refusing it bricks a store
// that holds nothing while claiming the user's data is at stake.
func TestAnEmptyChannelDirectoryStillCreatesAStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "ir"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("an empty %q directory must not block creation: %v", "ir", err)
	}
	defer s.Close()
	if _, err := s.SpawnUnderRoot(); err != nil {
		t.Fatalf("the created store does not work: %v", err)
	}
}

// The claim the dotfile exception rests on: a store this build writes
// always has a segment in the main channel's root, because that is where
// the genesis record goes. If that stops being true, the exception stops
// being safe and hasChannelContent must count any entry at all.
func TestAStoresMainChannelRootAlwaysHoldsASegment(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") && !e.IsDir() {
			return // a segment: the guard can never judge this store empty
		}
	}
	t.Fatalf("a fresh store's main channel root holds no segment (%d entries); "+
		"hasChannelContent's dotfile exception is no longer safe", len(ents))
}

// The reducer NAME is a registry key, not a property of the records. A real
// store carries reducer "jsonmerge" while figaro registers the same fold
// under "chalkboard"; refusing on that difference rejected a store whose
// records are exactly what the caller expects, and it would have met every
// existing user on their first run.
func TestAReducerRenameDoesNotRefuseAStore(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "ir", 0, []byte(`{"turn":1}`), nil); err != nil {
		t.Fatal(err)
	}
	patch, err := MapSetPatch([]string{"k"}, []byte(`"v"`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(string(tr), "chalkboard", 0, patch, nil); err != nil {
		t.Fatal(err)
	}
	before, err := chalkboardState(t, s, string(tr))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, `"k"`) {
		t.Fatalf("fixture board is %q; the test cannot show a fold surviving anything", before)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewrite the manifest the way an older build left it: the same fold,
	// registered under a different key.
	m, err := readManifestFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range m.Channels {
		if c.Name == "chalkboard" {
			m.Channels[i].Reducer = "jsonmerge"
		}
	}
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("a store whose reducer is registered under another name must open: %v", err)
	}
	defer s2.Close()
	after, err := chalkboardState(t, s2, string(tr))
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("board across the reducer rename: got %q want %q", after, before)
	}
}
