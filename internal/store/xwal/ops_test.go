package xwal

import "testing"

func TestDecodeFrameFastPathMatchesJSON(t *testing.T) {
	tests := []struct {
		payload string
		meta    string
	}{
		{`{"nested":{"text":"x},\\\"y"},"items":[1,true,null]}`, `{"provider":"anthropic"}`},
		{`"quoted payload"`, `"fingerprint"`},
		{`42`, ``},
		{`true`, `["meta",{"x":1}]`},
	}
	for _, tt := range tests {
		frame := encodeFrame(184467, []byte(tt.payload), []byte(tt.meta), 1754850000000)
		mainLT, payload, meta, ts, ok := fastDecodeFrame(frame)
		if !ok {
			t.Fatalf("fastDecodeFrame rejected %s", frame)
		}
		if mainLT != 184467 || string(payload) != tt.payload || string(meta) != tt.meta {
			t.Fatalf("decoded (%d, %s, %s), want (184467, %s, %s)",
				mainLT, payload, meta, tt.payload, tt.meta)
		}
		if ts != 1754850000000 {
			t.Fatalf("decoded ts = %d, want 1754850000000", ts)
		}
	}
}

func TestDecodeFrameFallsBackForNonCanonicalOrder(t *testing.T) {
	frame := []byte(`{"p":{"ok":true},"m":7,"x":"meta"}`)
	if _, _, _, _, ok := fastDecodeFrame(frame); ok {
		t.Fatal("non-canonical frame took fast path")
	}
	r, err := decodeRecord(3, frame)
	if err != nil {
		t.Fatal(err)
	}
	if r.MainLT != 7 || string(r.Payload) != `{"ok":true}` || string(r.Meta) != `"meta"` {
		t.Fatalf("record = %+v", r)
	}
}

// Covers the figaro-driven additions: per-entry meta, FK lookup,
// per-channel clear, and dynamic add-channel.
func TestXWAL_MetaAndLookup(t *testing.T) {
	dir := t.TempDir()
	x, _ := triune(t, dir)
	defer x.Close()

	m1, err := x.AppendMain([]byte(`{"role":"user"}`), []byte(`{"fp":"v0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Append("translations", m1, []byte(`["wire"]`), []byte(`"anthropic/v0"`)); err != nil {
		t.Fatal(err)
	}

	// Meta round-trips on the main channel.
	r, err := x.ReadAt("ir", m1)
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Meta) != `{"fp":"v0"}` {
		t.Fatalf("ir meta = %s, want {\"fp\":\"v0\"}", r.Meta)
	}

	// FK lookup on translations by main LT, with its (string) meta.
	got, ok, err := x.Lookup("translations", m1)
	if err != nil || !ok {
		t.Fatalf("Lookup miss: ok=%v err=%v", ok, err)
	}
	if string(got.Meta) != `"anthropic/v0"` {
		t.Fatalf("translation meta = %s, want quoted anthropic/v0", got.Meta)
	}

	// FK index survives reopen.
	x.Close()
	x2, _ := triune(t, dir)
	defer x2.Close()
	if _, ok, _ := x2.Lookup("translations", m1); !ok {
		t.Fatal("Lookup miss after reopen")
	}
}

func TestXWAL_LookupIndexesFromTail(t *testing.T) {
	dir := t.TempDir()
	x, _ := triune(t, dir)
	defer x.Close()
	for i := uint64(1); i <= 10; i++ {
		if _, err := x.AppendMain([]byte(`{"event":true}`), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := x.Append("translations", i, []byte(`{"wire":true}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := x.Lookup("translations", 10); err != nil || !ok {
		t.Fatalf("tail lookup: ok=%v err=%v", ok, err)
	}
	ch := x.chans["translations"]
	if len(ch.fk) != 1 || ch.fkBuilt {
		t.Fatalf("tail lookup indexed %d entries, complete=%v", len(ch.fk), ch.fkBuilt)
	}
	if _, ok, err := x.Lookup("translations", 5); err != nil || !ok {
		t.Fatalf("middle lookup: ok=%v err=%v", ok, err)
	}
	if len(ch.fk) != 6 {
		t.Fatalf("middle lookup indexed %d entries, want 6", len(ch.fk))
	}
	if _, ok, err := x.Lookup("translations", 11); err != nil || ok {
		t.Fatalf("missing lookup: ok=%v err=%v", ok, err)
	}
	if _, err := x.Append("translations", 10, []byte(`{"wire":"new-last"}`), nil); err != nil {
		t.Fatal(err)
	}
	r, ok, err := x.Lookup("translations", 10)
	if err != nil || !ok || string(r.Payload) != `{"wire":"new-last"}` {
		t.Fatalf("lookup after partial-index append: payload=%s ok=%v err=%v", r.Payload, ok, err)
	}
}

func TestXWAL_Clear(t *testing.T) {
	dir := t.TempDir()
	x, _ := triune(t, dir)
	defer x.Close()
	m1, _ := x.AppendMain([]byte(`{"a":1}`), nil)
	x.Append("translations", m1, []byte(`["x"]`), []byte(`"fp1"`))

	if err := x.Clear("translations"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok, _ := x.Lookup("translations", m1); ok {
		t.Fatal("translation survived Clear")
	}
	// Channel is reusable after clear.
	if _, err := x.Append("translations", m1, []byte(`["y"]`), []byte(`"fp2"`)); err != nil {
		t.Fatalf("append after clear: %v", err)
	}
	if got, ok, _ := x.Lookup("translations", m1); !ok || string(got.Meta) != `"fp2"` {
		t.Fatalf("post-clear lookup = (%q,%v)", got.Meta, ok)
	}
}

func TestXWAL_AddChannel(t *testing.T) {
	dir := t.TempDir()
	x, _ := triune(t, dir)
	m1, _ := x.AppendMain([]byte(`{"a":1}`), nil)

	// Add a translation channel for a newly-seen provider.
	if err := x.addChannel(ChannelSpec{Name: "translations/openai", Kind: ChannelLog}); err != nil {
		t.Fatalf("AddChannel: %v", err)
	}
	if _, err := x.Append("translations/openai", m1, []byte(`["wire"]`), nil); err != nil {
		t.Fatalf("append to new channel: %v", err)
	}
	x.Close()

	// New channel persisted in the manifest and reopens.
	x2, _ := triune(t, dir)
	defer x2.Close()
	if _, ok, _ := x2.Lookup("translations/openai", m1); !ok {
		t.Fatal("added channel not found after reopen")
	}
}

func TestTrunksHeadDetachesBeforeChannelMutation(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Main: "ir",
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "translations", Kind: ChannelLog},
		},
	}
	trunks, err := createTrunks(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, trunks)
	trunk, err := trunks.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	x, err := trunks.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	mainLT, err := x.AppendMain([]byte(`{"event":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.addChannel(ChannelSpec{Name: "translations/openai", Kind: ChannelLog}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Append("translations/openai", mainLT, []byte(`{"wire":1}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}

	x, err = trunks.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := x.Lookup("translations/openai", mainLT); err != nil || !ok {
		t.Fatalf("lookup after AddChannel: ok=%v err=%v", ok, err)
	}
	if err := x.Clear("translations/openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Append("translations/openai", mainLT, []byte(`{"wire":2}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}

	x, err = trunks.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	r, ok, err := x.Lookup("translations/openai", mainLT)
	if err != nil || !ok || string(r.Payload) != `{"wire":2}` {
		t.Fatalf("lookup after Clear: payload=%s ok=%v err=%v", r.Payload, ok, err)
	}
}
