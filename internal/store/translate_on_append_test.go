package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"
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

// A TRANSLATION IS WRITTEN BY THE SITE THAT HOLDS THE REPAIRED PAYLOAD, and it
// is written to the channels the aria ALREADY HAS -- never to a channel it
// does not, which is what keeps the fan-out proportional to use.
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

// AN ENCODER THAT FAILS DOES NOT FAIL THE APPEND. The fig IR entry is
// canonical and has landed; a translation that did not is derived, missing,
// and rebuilt by the next catch-up. This is the asymmetry that makes fig-IR-
// first the safe order.
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

// THE SYNTHESIZED ENTRIES ARE TRANSLATED TOO. The fig IR write path mints
// records nobody upstream knows about -- the tool-close message and the
// late-result note -- and they must reach the wire well-formed. A caller
// writing translations upstream would miss exactly these.
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
	// The ASSISTANT entry belongs to the provider that produced it, so what
	// must be translated here is the minted tool-close and the message after
	// it -- and the minted one is the whole point: nobody upstream knows it
	// exists.
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

// REGISTRATION IS ADDITIVE, because providers are built one at a time as arias
// open. A set that replaced would leave the last one registered as the only
// translator and put every other provider silently back on the catch-up --
// which looks exactly like working software.
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

// AN ASSISTANT ENTRY IS LEFT TO THE PROVIDER THAT PRODUCED IT.
//
// Its wire form carries provider-native material the fig IR does not hold, and
// the turn commits that durably. A rendering written here would be a SECOND
// entry at one FigaroLT -- and the residency index cannot represent that: a
// warm read serves the first, so the model would get the unsigned rendering
// while the signed original sat underneath it until a restart.
//
// Found live, not in a fixture: scripts/live/onappendlive.sh read translations
// naming LTs "4 5 5" against fig IR entries "3 4 5".
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

// A READ OF THE ENCODER SET TAKES NO LOCK, so a fig IR append is never held up
// by a provider being built for some other aria. The property is the artifact:
// hold the WRITER's lock and serve a read from another goroutine. Under the
// RWMutex this test deadlocks; under an atomic publish it returns.
//
// CANARY: restore `writeMu` around the body of get() and this goes red by
// timeout.
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
