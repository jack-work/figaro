package figaro_test

// THE QUOTE AT THE DOOR, end to end: a prompt that begins with a coordinate
// reaches the provider carrying the passage it names, and one that names
// nothing is refused on the reply with the log and the queue untouched.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

const quotedReply = "The retry loop backs off exponentially, but the jitter is applied before the cap."

// quoteProvider answers the first turn with a known paragraph and records
// the last user message it was shown on the second.
type quoteProvider struct {
	mu    sync.Mutex
	sends int
	seen  []string // the text blocks of the last user message, per send
}

func (p *quoteProvider) Name() string                                             { return "mock" }
func (p *quoteProvider) Fingerprint() string                                      { return "mock/v0" }
func (p *quoteProvider) SetModel(string)                                          {}
func (p *quoteProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *quoteProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.sends++
	entries := in.FigLog.Read()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Payload.Role == message.RoleInput {
			var texts []string
			for _, c := range entries[i].Payload.Content {
				texts = append(texts, c.Text)
			}
			p.seen = append(p.seen, strings.Join(texts, "\n"))
			break
		}
	}
	p.mu.Unlock()
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent(quotedReply)},
		StopReason: message.StopEnd,
	}
	bus.PushDelta(message.Content{Type: message.ContentProse, Text: quotedReply})
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func newQuoteAgent(t *testing.T) (*figaro.Agent, *quoteProvider) {
	t.Helper()
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
		"mantra":          json.RawMessage(`"the retry loop"`),
	}, nil))
	prov := &quoteProvider{}
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/test-figaro-quote-" + testID + ".sock",
		Provider:   prov,
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	t.Cleanup(a.Kill)
	return a, prov
}

// lastOutputLT finds the LT of the newest assistant message, which is what a
// reader would read off `figaro show -v`.
func lastOutputLT(t *testing.T, a *figaro.Agent) uint64 {
	t.Helper()
	msgs := a.Context()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.RoleOutput {
			return msgs[i].LogicalTime
		}
	}
	t.Fatal("no assistant message in the log")
	return 0
}

func TestQuotedPromptReachesTheProvider(t *testing.T) {
	a, prov := newQuoteAgent(t)
	ch, unsub := subscribeChan(a)
	defer unsub()

	a.SubmitPrompt(rpc.QuaRequest{Text: "explain the retry loop"})
	waitTurnDone(t, ch)
	lt := lastOutputLT(t, a)

	// The passage, a board reference, and the reader's question in one send.
	tok := "<" + itoa(lt) + ".0:4-14>!"
	if err := a.SubmitPromptFrom(rpc.QuaRequest{Text: tok + " is @mantra! racy here?"}, "Gluck"); err != nil {
		t.Fatalf("a good coordinate was refused: %v", err)
	}
	waitTurnDone(t, ch)

	prov.mu.Lock()
	defer prov.mu.Unlock()
	if len(prov.seen) != 2 {
		t.Fatalf("provider saw %d user messages, want 2", len(prov.seen))
	}
	got := prov.seen[1]
	for _, want := range []string{
		"> quoting aria " + a.ID() + " · turn 1 · lt " + itoa(lt) + ".0 · chars 4-14 (10 chars)\n",
		"> retry loop\n",
		"\n\nis the retry loop racy here?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the provider did not receive %q; it received:\n%s", want, got)
		}
	}
	if strings.Contains(got, tok) {
		t.Fatalf("the raw token reached the provider: %s", got)
	}
}

func TestBadQuoteIsRefusedBeforeAnythingLands(t *testing.T) {
	a, prov := newQuoteAgent(t)
	ch, unsub := subscribeChan(a)
	defer unsub()

	a.SubmitPrompt(rpc.QuaRequest{Text: "explain the retry loop"})
	waitTurnDone(t, ch)
	before := len(a.Context())
	lt := lastOutputLT(t, a)

	cases := []struct{ text, want string }{
		{"<9999>! what", "quote: <9999>: no message at lt 9999"},
		{"<" + itoa(lt) + ".7>! what", "quote: <" + itoa(lt) + ".7>: lt " + itoa(lt) + " has 1 block, not block 7"},
		{"<" + itoa(lt) + ".0:0-5000>! what", "quote: <" + itoa(lt) + ".0:0-5000>: block 0 of lt " + itoa(lt) + " has " + itoa(uint64(len(quotedReply))) + " chars, not 0-5000"},
		{"<" + itoa(lt) + ".0:9-2>! what", "quote: <" + itoa(lt) + ".0:9-2>: end 2 is before start 9"},
		{"@nothing! what", "ref: @nothing! is not on the board"},
	}
	for _, c := range cases {
		err := a.SubmitPromptFrom(rpc.QuaRequest{Text: c.text}, "Gluck")
		if err == nil {
			t.Fatalf("%q was accepted", c.text)
		}
		if err.Error() != c.want {
			t.Fatalf("%q: refused with %q, want %q", c.text, err, c.want)
		}
	}
	// Nothing was appended, nothing queued, no turn started.
	if after := len(a.Context()); after != before {
		t.Fatalf("a refused message changed the log: %d -> %d entries", before, after)
	}
	if _, queued := a.QueuedPrompts(true); len(queued) != 0 {
		t.Fatalf("a refused message was queued: %+v", queued)
	}
	select {
	case n := <-ch:
		if n.Method == rpc.MethodTurnDone {
			t.Fatal("a refused message started a turn")
		}
	case <-time.After(300 * time.Millisecond):
	}
	prov.mu.Lock()
	defer prov.mu.Unlock()
	if prov.sends != 1 {
		t.Fatalf("the provider was called %d times; a refusal must not reach it", prov.sends)
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }
