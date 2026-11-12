package provider

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// The seed asks the rows whether the last user record's row was written, and
// reads them backwards a few at a time, starting with one. When many
// assistant rows stand between the newest row and that user record, it has
// to keep reading until it reaches the record's own row: giving up early
// seeds from an older record, and the next user row repeats a form change
// the model has already been told.
func TestASeedReadsBackToTheLastUserRowThroughManyAssistantRows(t *testing.T) {
	bound := versionedForm{patches: []message.Patch{setKey("mantra", "one"), setKey("mantra", "two"), setKey("mantra", "three")}}
	said := func(role message.Role, text string, version uint64) store.Entry[message.Message] {
		return store.Entry[message.Message]{FormChannelVersion: version,
			Payload: message.Message{Role: role, Content: []message.Content{message.TextContent(text)}}}
	}
	head := []store.Entry[message.Message]{
		said(message.RoleInput, "ask", 1),
		said(message.RoleOutput, "answer", 1),
		said(message.RoleInput, "ask again", 2),
	}
	for i := 0; i < 3*rowsBehindReach; i++ {
		head = append(head, said(message.RoleOutput, fmt.Sprintf("step %d", i), 2))
	}
	next := said(message.RoleInput, "ask a third time", 3)
	config := func(log store.Log[message.Message], rows store.Log[[]json.RawMessage]) CatchUpConfig {
		return CatchUpConfig{Log: log, Translator: rows, Form: bound, Fingerprint: "v1", Encode: emptyRowEncode}
	}
	fill := func(entries ...store.Entry[message.Message]) store.Log[message.Message] {
		t.Helper()
		log := store.NewMemLog[message.Message]()
		for _, e := range entries {
			if _, err := log.Append(e); err != nil {
				t.Fatal(err)
			}
		}
		return log
	}
	dump := func(rows store.Log[[]json.RawMessage]) string {
		t.Helper()
		b, err := json.Marshal(rows.Read())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	wholeRows := store.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(config(fill(append(append([]store.Entry[message.Message]{}, head...), next)...), wholeRows)); err != nil {
		t.Fatal(err)
	}
	want := dump(wholeRows)

	log, rows := fill(head...), store.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(config(log, rows)); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(next); err != nil {
		t.Fatal(err)
	}
	if _, err := CatchUp(config(log, rows)); err != nil {
		t.Fatal(err)
	}
	if got := dump(rows); got != want {
		t.Fatalf("rows after a stop %d assistant rows behind the last user row differ from an uninterrupted catch-up\n got ...%s\nwant ...%s",
			3*rowsBehindReach, got[max(0, len(got)-300):], want[max(0, len(want)-300):])
	}
}
