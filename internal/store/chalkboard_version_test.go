package store

import "testing"

// The chalkboard VERSION is the append index of the aria's chalkboard
// channel. Two properties are the whole contract a subscriber's resume
// cursor rests on, so they are asserted rather than assumed:
//
//   - strictly monotonic per aria, INCLUDING for a semantically-empty write.
//     The question a version answers is "did my patch reach the log", not
//     "did the state change" — a set that writes the same value it already
//     had still has to be acknowledgeable, or a caller waiting on it hangs.
//   - durable across reopen. A session-scoped counter would restart at zero
//     and silently rewind every cursor held by a reconnecting client.
//
// Before this, ApplyChalkboard discarded the index: there was no cursor.
func TestApplyChalkboardVersionIsMonotonicAndDurable(t *testing.T) {
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	l, err := b.CreateLoadout("default", patchSet(map[string]string{"system.credo": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(l)
	if err != nil {
		t.Fatal(err)
	}

	var last uint64
	for _, v := range []string{"a", "b", "c"} {
		got, err := b.ApplyChalkboard(conv, patchSet(map[string]string{"k": v}))
		if err != nil {
			t.Fatalf("apply %q: %v", v, err)
		}
		if got <= last {
			t.Fatalf("version not monotonic: got %d after %d", got, last)
		}
		last = got
	}

	// Re-writing the value it already holds is a semantic no-op for the
	// snapshot, but it is still an append, so it still earns a version.
	same, err := b.ApplyChalkboard(conv, patchSet(map[string]string{"k": "c"}))
	if err != nil {
		t.Fatalf("no-op apply: %v", err)
	}
	if same <= last {
		t.Fatalf("no-op write got version %d, want > %d", same, last)
	}
	last = same
	b.Close()

	b2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	next, err := b2.ApplyChalkboard(conv, patchSet(map[string]string{"k": "z"}))
	if err != nil {
		t.Fatalf("apply after reopen: %v", err)
	}
	if next <= last {
		t.Fatalf("version restarted across reopen: got %d, want > %d", next, last)
	}
}
