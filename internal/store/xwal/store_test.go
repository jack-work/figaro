package xwal

import (
	"strings"
	"testing"
	"time"
)

func testStoreOptions() StoreOptions {
	return StoreOptions{
		Main:         "ir",
		SyncInterval: 20 * time.Millisecond,
		Reducers:     map[string]Reducer{"chalkboard": MapReducer()},
	}
}

func TestOpenStoreSecondWriterFails(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := OpenStore(dir, testStoreOptions()); err == nil ||
		!strings.Contains(err.Error(), "already has a writer") {
		t.Fatalf("second writer: got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreAppendRead(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	lt1, err := s.Append(tr, "ir", 0, []byte(`{"n":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	lt2, err := s.Append(tr, "ir", 0, []byte(`{"n":2}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if lt2 != lt1+1 {
		t.Fatalf("tail append: got %d after %d", lt2, lt1)
	}
	patch, err := MapSetPatch([]string{"k"}, []byte(`"v"`))
	if err != nil {
		t.Fatal(err)
	}
	clt, err := s.Append(tr, "chalkboard", 0, patch, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.StateAt(tr, "chalkboard", clt)
	if err != nil {
		t.Fatal(err)
	}
	if string(state) != `{"k":"v"}` {
		t.Fatalf("state: %s", state)
	}
	m, payload, err := s.Read(tr, "ir", lt2)
	if err != nil {
		t.Fatal(err)
	}
	if m != lt2 || string(payload) != `{"n":2}` {
		t.Fatalf("read: m=%d payload=%s", m, payload)
	}
	if got := s.ListLight(); len(got) != 1 || got[0].ID != tr {
		t.Fatalf("list: %+v", got)
	}
}

func TestStoreForkExplicit(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	var lts []uint64
	for i := 0; i < 3; i++ {
		lt, err := s.Append(tr, "ir", 0, []byte(`{"i":`+string(rune('0'+i))+`}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		lts = append(lts, lt)
	}
	alt, err := s.Fork(tr, lts[1])
	if err != nil {
		t.Fatal(err)
	}
	if alt == tr {
		t.Fatalf("fork returned same trunk")
	}
	altLT, err := s.Append(alt, "ir", 0, []byte(`{"alt":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if altLT != lts[1]+1 {
		t.Fatalf("alt append at %d, want %d", altLT, lts[1]+1)
	}
	if _, payload, err := s.Read(tr, "ir", lts[2]); err != nil || string(payload) != `{"i":2}` {
		t.Fatalf("original suffix: %s err=%v", payload, err)
	}
}

func TestStoreAutoCreatesUnknownChannel(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	lt, err := s.Append(tr, "ir", 0, []byte(`{"n":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	clt, err := s.Append(tr, "translations/newprov", lt, []byte(`{"wire":1}`), nil)
	if err != nil {
		t.Fatalf("auto-create append: %v", err)
	}
	if _, payload, err := s.Read(tr, "translations/newprov", clt); err != nil || string(payload) != `{"wire":1}` {
		t.Fatalf("read auto-created: %s err=%v", payload, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rec, ok, err := s2.Lookup(tr, "translations/newprov", lt)
	if err != nil || !ok || string(rec.Payload) != `{"wire":1}` {
		t.Fatalf("reopened auto-created channel: %+v ok=%v err=%v", rec, ok, err)
	}
}

func TestOpenStoreCreatesDeclaredReducerChannel(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, testStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(tr, "ir", 0, []byte(`{"n":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	opts := testStoreOptions()
	opts.Reducers["scratch"] = MapReducer()
	s2, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	patch, _ := MapSetPatch([]string{"k"}, []byte(`1`))
	clt, err := s2.Append(tr, "scratch", 0, patch, nil)
	if err != nil {
		t.Fatalf("append to late-declared reducible: %v", err)
	}
	state, err := s2.StateAt(tr, "scratch", clt)
	if err != nil || string(state) != `{"k":1}` {
		t.Fatalf("state: %s err=%v", state, err)
	}
}
