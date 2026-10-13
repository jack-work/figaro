package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
)

type countingEncoder struct {
	provider string
	calls    int
	seen     []uint64
	fail     bool
}

func (e *countingEncoder) Provider() string { return e.provider }

func (e *countingEncoder) EncodeEntry(_ string, _ Log[message.Message], en Entry[message.Message]) ([]json.RawMessage, string, error) {
	e.calls++
	e.seen = append(e.seen, en.LT)
	if e.fail {
		return nil, "", fmt.Errorf("encoder refuses")
	}
	body, err := json.Marshal(map[string]any{"lt": en.LT, "role": string(en.Payload.Role)})
	if err != nil {
		return nil, "", err
	}
	return []json.RawMessage{body}, e.provider + "/v1", nil
}

func writeFigIR(t *testing.T, log Log[message.Message], text string) Entry[message.Message] {
	t.Helper()
	e, err := log.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent(text)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A translation is written by the site that holds the repaired payload, to
// the channels the aria already has, never to one it does not.
func TestAnAppendTranslatesIntoTheChannelsTheAriaAlreadyHas(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	anth := &countingEncoder{provider: "anthropic"}
	never := &countingEncoder{provider: "a-provider-this-aria-never-used"}
	be.AddTranslatorEncoders(anth, never)

	// The aria has an anthropic channel because something opened it once.
	trans, err := be.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}

	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	first := writeFigIR(t, log, "one")
	second := writeFigIR(t, log, "two")

	if anth.calls != 2 {
		t.Fatalf("the anthropic encoder ran %d times, want 2 -- one per fig IR entry", anth.calls)
	}
	if never.calls != 0 {
		t.Fatalf("an encoder ran %d times for a channel this aria does not have", never.calls)
	}

	got := trans.Read()
	if len(got) != 2 {
		t.Fatalf("translator entries=%d, want 2", len(got))
	}
	if got[0].FigaroLT != first.LT || got[1].FigaroLT != second.LT {
		t.Fatalf("entries name FigaroLT %d,%d, want %d,%d",
			got[0].FigaroLT, got[1].FigaroLT, first.LT, second.LT)
	}
	if got[0].Fingerprint != "anthropic/v1" {
		t.Fatalf("fingerprint=%q, want the encoder's", got[0].Fingerprint)
	}
	// THE ENTRY NAMES ITS SOURCE BY CONTENT, not only by position.
	want, err := FigaroHash(first.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].FigaroHash != want {
		t.Fatalf("FigaroHash=%q, want %q", got[0].FigaroHash, want)
	}
}

// An encoder that fails does not fail the append: the IR entry is canonical
// and has landed; the translation is rebuilt by the next catch-up.
func TestATranslationFailureDoesNotFailTheFigIRAppend(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	bad := &countingEncoder{provider: "anthropic", fail: true}
	be.AddTranslatorEncoders(bad)
	if _, err := be.OpenTranslator(aria, "anthropic"); err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}

	before := len(log.Read())
	e := writeFigIR(t, log, "the entry that must survive a bad encoder")
	if e.LT == 0 {
		t.Fatal("the fig IR append did not land")
	}
	if got := len(log.Read()); got != before+1 {
		t.Fatalf("fig IR entries=%d, want %d", got, before+1)
	}
	trans, err := be.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(trans.Read()); got != 0 {
		t.Fatalf("translator entries=%d, want 0 -- a refused encode writes nothing", got)
	}
}

// The synthesized entries (the tool-close message, the late-result note) are
// translated too; a caller writing translations upstream would miss exactly these.
func TestTheWritePathTranslatesTheRecordsItMintsItself(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	enc := &countingEncoder{provider: "anthropic"}
	be.AddTranslatorEncoders(enc)
	if _, err := be.OpenTranslator(aria, "anthropic"); err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}

	// An aria is BORN with records, and they predate this channel, so only
	// what is appended from here on can have been translated on append.
	born := len(log.Read())

	// An assistant message that opens a tool call, then a message that is not
	// its result: the write path closes the call with a synthesized record.
	if _, err := log.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleOutput,
		Content: []message.Content{
			{Type: message.ContentToolInvoke, ToolCallID: "t1", ToolName: "echo", Arguments: map[string]any{}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	writeFigIR(t, log, "something else entirely")

	appended := log.Read()[born:]
	if len(appended) != 3 {
		t.Fatalf("appended %d fig IR entries, want 3 (assistant, the MINTED tool-close, the next message)",
			len(appended))
	}
	// The middle one is the record nobody upstream asked for.
	minted := appended[1].Payload
	if minted.Role != message.RoleInput || len(minted.Content) == 0 ||
		minted.Content[0].Type != message.ContentToolResult {
		t.Fatalf("entry 2 is not the synthesized tool-close: role=%s content=%d",
			minted.Role, len(minted.Content))
	}

	trans, err := be.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	// The assistant entry belongs to the provider that produced it, so what
	// must be translated here is the minted tool-close and the message after it.
	var want []uint64
	for _, e := range appended {
		if e.Payload.Role != message.RoleOutput {
			want = append(want, e.LT)
		}
	}
	var got []uint64
	for _, e := range trans.Read() {
		got = append(got, e.FigaroLT)
	}
	if len(got) != len(want) {
		t.Fatalf("translations name %v, want %v -- the minted record must be among them", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("translations name %v, want %v", got, want)
		}
	}
	if want[0] != appended[1].LT {
		t.Fatalf("the first translated entry is %d, but the MINTED tool-close is %d",
			want[0], appended[1].LT)
	}
}

// Registration is additive: providers are built one at a time as arias open,
// and a replacing set would put every other provider silently back on catch-up.
func TestRegisteringASecondEncoderKeepsTheFirst(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	first := &countingEncoder{provider: "anthropic"}
	second := &countingEncoder{provider: "copilot-messages"}
	be.AddTranslatorEncoders(first)
	be.AddTranslatorEncoders(second)

	for _, p := range []string{"anthropic", "copilot-messages"} {
		if _, err := be.OpenTranslator(aria, p); err != nil {
			t.Fatal(err)
		}
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	writeFigIR(t, log, "one")

	if first.calls != 1 {
		t.Fatalf("the FIRST encoder ran %d times, want 1: a later registration dropped it", first.calls)
	}
	if second.calls != 1 {
		t.Fatalf("the second encoder ran %d times, want 1", second.calls)
	}
}

// An assistant entry is left to the provider that produced it: its wire form
// carries provider-native material the IR does not hold, and a rendering
// written here would be a second entry at one FigaroLT, serving the unsigned
// rendering over the signed original.
func TestAnAssistantEntryIsNotTranslatedOnAppend(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	enc := &countingEncoder{provider: "anthropic"}
	be.AddTranslatorEncoders(enc)
	if _, err := be.OpenTranslator(aria, "anthropic"); err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}

	writeFigIR(t, log, "a user message")
	if _, err := log.Append(Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleOutput,
		Content: []message.Content{message.TextContent("an assistant message")},
	}}); err != nil {
		t.Fatal(err)
	}

	trans, err := be.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint64]int{}
	for _, e := range trans.Read() {
		seen[e.FigaroLT]++
	}
	for lt, n := range seen {
		if n > 1 {
			t.Fatalf("FigaroLT %d has %d translations: a second entry at one coordinate "+
				"is invisible to a warm read", lt, n)
		}
	}
	if enc.calls != 1 {
		t.Fatalf("the encoder ran %d times, want 1 -- the assistant entry belongs to the provider", enc.calls)
	}
}

// A read of the encoder set takes no lock, so an IR append is never held up
// by a provider being built for another aria. The property is the artifact:
// hold the writer's lock and serve a read from another goroutine.
func TestReadingTheEncoderSetTakesNoLock(t *testing.T) {
	var set translatorEncoders
	set.add([]TranslatorEncoder{fakeEncoder{name: "anthropic"}})

	set.writeMu.Lock()
	defer set.writeMu.Unlock()

	done := make(chan bool, 1)
	go func() {
		_, ok := set.get("anthropic")
		done <- ok
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("the read did not find the encoder it was given")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a read blocked behind a writer: the encoder set is on the append path " +
			"and a provider being built for another aria must not hold it up")
	}
}

type fakeEncoder struct{ name string }

func (f fakeEncoder) Provider() string { return f.name }
func (f fakeEncoder) EncodeEntry(string, Log[message.Message], Entry[message.Message]) ([]json.RawMessage, string, error) {
	return nil, "", nil
}

// emptyEncoder owes a row for every entry and fills none of them.
type emptyEncoder struct{ owed bool }

func (e emptyEncoder) Provider() string { return "anthropic" }
func (e emptyEncoder) EncodeEntry(string, Log[message.Message], Entry[message.Message]) ([]json.RawMessage, string, error) {
	if e.owed {
		return nil, "anthropic/v1", nil
	}
	return nil, "", nil
}

// An entry that translates to nothing still owns a row, so coverage below the
// watermark stays a count; one that owes no row writes none.
func TestAnEmptyTranslationStillOwnsARow(t *testing.T) {
	for _, tc := range []struct {
		name string
		owed bool
		rows int
	}{{"owed", true, 1}, {"not owed", false, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			be, aria := NewTestAria(t, "d", message.Patch{})
			be.AddTranslatorEncoders(emptyEncoder{owed: tc.owed})
			trans, err := be.OpenTranslator(aria, "anthropic")
			if err != nil {
				t.Fatal(err)
			}
			log, err := be.OpenFigIR(aria)
			if err != nil {
				t.Fatal(err)
			}
			e := writeFigIR(t, log, "nothing on the wire")
			got := trans.Read()
			if len(got) != tc.rows {
				t.Fatalf("rows=%d, want %d", len(got), tc.rows)
			}
			if tc.rows == 0 {
				return
			}
			want, _ := FigaroHash(e.Payload)
			if got[0].FigaroLT != e.LT || len(got[0].Payload) != 0 || got[0].Fingerprint != "anthropic/v1" || got[0].FigaroHash != want {
				t.Fatalf("row=%+v, want an empty row stamped for %d", got[0], e.LT)
			}
		})
	}
}
