package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func emptyRowEncode(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
	if len(msg.Content) > 0 && msg.Content[0].Text == "silent" {
		return nil, nil
	}
	seen := ""
	if v := snap.Lookup("mantra"); v != nil {
		seen = *v
	}
	body, err := json.Marshal(map[string]any{
		"role": string(msg.Role), "lt": msg.LogicalTime, "form": seen, "patches": msg.Patches,
	})
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{body}, nil
}

// A user record that encodes to nothing commits no cursor: the form change
// pending on it rides the next user record. A derivation that stops after the
// assistant row behind it and is seeded again (a restart, a failed encode, a
// cancelled turn) must still deliver that change. Seeding from the log alone
// adopts the empty row's version, so the model is never told.
func TestASeedAfterAnEmptyRowKeepsItsPendingFormChange(t *testing.T) {
	bound := versionedForm{patches: []message.Patch{setKey("mantra", "one"), setKey("mantra", "two")}}
	said := func(role message.Role, text string, version uint64) store.Entry[message.Message] {
		return store.Entry[message.Message]{FormChannelVersion: version,
			Payload: message.Message{Role: role, Content: []message.Content{message.TextContent(text)}}}
	}
	head := []store.Entry[message.Message]{
		said(message.RoleInput, "ask", 1),
		said(message.RoleOutput, "answer", 1),
		said(message.RoleInput, "silent", 2),
		said(message.RoleOutput, "answer again", 2),
	}
	next := said(message.RoleInput, "ask again", 2)
	config := func(log store.Log[message.Message], rows store.Log[[]json.RawMessage]) CatchUpConfig {
		return CatchUpConfig{Log: log, Translator: rows, Form: bound, Fingerprint: "v1", Encode: emptyRowEncode}
	}
	appendAll := func(log store.Log[message.Message], entries ...store.Entry[message.Message]) {
		t.Helper()
		for _, e := range entries {
			if _, err := log.Append(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	rowsJSON := func(rows store.Log[[]json.RawMessage]) string {
		t.Helper()
		b, err := json.Marshal(rows.Read())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	whole, wholeRows := store.NewMemLog[message.Message](), store.NewMemLog[[]json.RawMessage]()
	appendAll(whole, append(append([]store.Entry[message.Message]{}, head...), next)...)
	stats, err := CatchUp(config(whole, wholeRows))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Unwritten != 1 {
		t.Fatalf("uninterrupted pass: %+v, want exactly one empty row", stats)
	}
	want := rowsJSON(wholeRows)

	log, rows := store.NewMemLog[message.Message](), store.NewMemLog[[]json.RawMessage]()
	appendAll(log, head...)
	if _, err := CatchUp(config(log, rows)); err != nil {
		t.Fatal(err)
	}
	appendAll(log, next)
	if _, err := CatchUp(config(log, rows)); err != nil {
		t.Fatal(err)
	}
	if got := rowsJSON(rows); got != want {
		t.Fatalf("rows after a stop behind the assistant row differ from an uninterrupted catch-up\n got %s\nwant %s", got, want)
	}
}
