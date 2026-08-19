package xwal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func renameStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	x, err := Open(dir, Config{
		Main: "ir",
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "board", Kind: ChannelReducible, Reducer: "board", Unkeyed: true},
		},
		Registry: map[string]Reducer{"board": {Reduce: lastWins, Initial: []byte("{}")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := x.AppendMain([]byte(`{"n":1}`), nil); err != nil {
		t.Fatalf("append main: %v", err)
	}
	if _, err := x.Append("board", 0, []byte(`{"k":"v"}`), nil); err != nil {
		t.Fatalf("append board: %v", err)
	}
	if err := x.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dir
}

func lastWins(_, patch []byte) ([]byte, error) { return patch, nil }

// The point of the whole exercise: a store renamed on disk opens under the new
// name, with the records it had, and the reducer resolves — the failure this
// replaces was "no reducer registered", from a consumer that renamed a channel
// and could no longer open its own store.
func TestRenameChannelMovesDataAndManifest(t *testing.T) {
	dir := renameStore(t)
	if err := RenameChannel(dir, "board", "form"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "board")); !os.IsNotExist(err) {
		t.Fatalf("old channel dir survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "form")); err != nil {
		t.Fatalf("new channel dir: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Channels {
		if c.Name == "board" {
			t.Fatal("manifest still names the old channel")
		}
		if c.Name == "form" && c.Reducer != "form" {
			// The reducer name travelled because it matched the channel name;
			// resolveReducer looks by reducer first, channel second, so leaving
			// it as "board" would demand a registration nobody will make.
			t.Fatalf("reducer = %q, want form", c.Reducer)
		}
	}

	x, err := Open(dir, Config{
		Main: "ir",
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "form", Kind: ChannelReducible, Reducer: "form", Unkeyed: true},
		},
		Registry: map[string]Reducer{"form": {Reduce: lastWins, Initial: []byte("{}")}},
	})
	if err != nil {
		t.Fatalf("reopen after rename: %v", err)
	}
	defer x.Close()
	rec, err := x.ReadAt("form", 1)
	if err != nil {
		t.Fatalf("read renamed channel: %v", err)
	}
	if string(rec.Payload) != `{"k":"v"}` {
		t.Fatalf("payload = %s", rec.Payload)
	}
}

// A migration that dies between the directory move and the manifest write must
// be re-runnable: the move is recognised as already done and the manifest is
// repaired.
func TestRenameChannelResumesAfterAHalfMove(t *testing.T) {
	dir := renameStore(t)
	if err := os.Rename(filepath.Join(dir, "board"), filepath.Join(dir, "form")); err != nil {
		t.Fatal(err)
	}
	if err := RenameChannel(dir, "board", "form"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	names, err := channelNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == "board" {
			t.Fatal("manifest not repaired")
		}
	}
}

// Already done is not an error: a consumer runs its migration on every open.
func TestRenameChannelIsIdempotent(t *testing.T) {
	dir := renameStore(t)
	if err := RenameChannel(dir, "board", "form"); err != nil {
		t.Fatal(err)
	}
	if err := RenameChannel(dir, "board", "form"); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestRenameChannelRefusesEscapes(t *testing.T) {
	dir := renameStore(t)
	for _, to := range []string{"../escape", "/abs", ".hidden", ""} {
		if err := RenameChannel(dir, "board", to); err == nil {
			t.Fatalf("rename to %q was allowed", to)
		}
	}
	if err := RenameChannel(dir, "nope", "form"); err == nil {
		t.Fatal("renaming a channel that does not exist was allowed")
	}
}
