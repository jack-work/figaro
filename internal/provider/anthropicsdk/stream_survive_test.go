package anthropicsdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/jack-work/figaro/internal/message"
)

// A hand-built SSE body, driven through the SAME decoder drainStream uses.
// No network, no daemon, no tokens: the accumulator is the thing under test.
func sseStream(body string) *ssestream.Stream[anthropic.MessageStreamEventUnion] {
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bufio.NewReader(bytes.NewReader([]byte(body)))),
	}
	return ssestream.NewStream[anthropic.MessageStreamEventUnion](ssestream.NewDecoder(resp), nil)
}

func sseEvent(t *testing.T, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return "event: " + name + "\ndata: " + string(b) + "\n\n"
}

// toolTurn builds one assistant turn: a thinking-free text block, then a
// tool_use whose input arrives as the given fragments, verbatim.
func toolTurn(t *testing.T, prose string, fragments []string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(sseEvent(t, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-opus-5", "content": []any{}, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}},
	}))
	b.WriteString(sseEvent(t, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}))
	b.WriteString(sseEvent(t, "content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": prose},
	}))
	b.WriteString(sseEvent(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))
	b.WriteString(sseEvent(t, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 1,
		"content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "edit", "input": map[string]any{}},
	}))
	for _, f := range fragments {
		b.WriteString(sseEvent(t, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": f},
		}))
	}
	b.WriteString(sseEvent(t, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1}))
	b.WriteString(sseEvent(t, "message_delta", map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"},
		"usage": map[string]any{"output_tokens": 9}}))
	b.WriteString(sseEvent(t, "message_stop", map[string]any{"type": "message_stop"}))
	return b.String()
}

// FIGARO AND THE SDK DECODE THE WIRE EXACTLY ONCE.
//
// This is the control for the whole investigation. `partial_json` is a JSON
// STRING whose contents are a fragment of the tool input's JSON TEXT, so a tab
// inside an argument travels as the four bytes \\t and arrives as the two
// bytes \t. If anything on our side decoded a second time, the tab would
// arrive RAW — which is precisely the corruption seen in the field.
//
// It does not. The accumulated buffer is byte-identical to what was meant, so
// a payload that arrives broken was already broken when it reached us.
func TestWireIsDecodedExactlyOnce(t *testing.T) {
	intended := `{"path": "x.go", "new_text": "\tif a != \"b\" {\n\t}"}`
	if !json.Valid([]byte(intended)) {
		t.Fatal("fixture: the tool input must itself be valid JSON")
	}
	body := toolTurn(t, "here goes", []string{intended[:20], intended[20:]})
	if !strings.Contains(body, `\\t`) {
		t.Fatal("fixture: a correct wire double-escapes the tab inside partial_json")
	}

	fig, acc, err := drainStream(context.Background(), sseStream(body), "claude-opus-5", nopBus{})
	if err != nil {
		t.Fatalf("a well-formed turn must not fail: %v", err)
	}
	if got := string(acc.Content[1].Input); got != intended {
		t.Errorf("the accumulated input is not what was sent:\n got %q\nwant %q", got, intended)
	}
	for _, c := range fig.Content {
		if c.Type != message.ContentToolInvoke {
			continue
		}
		if _, bad := message.MalformedArgsOf(c); bad {
			t.Error("a valid call was quarantined")
		}
		if c.Arguments["path"] != "x.go" {
			t.Errorf("arguments = %v, want path=x.go", c.Arguments)
		}
	}
}

// ONE UNUSABLE BLOCK COSTS ONE TOOL CALL, NOT THE TURN.
//
// The fragments here are the field failure, reduced: the model's source text
// arrived with its tabs, newlines and quotes unescaped, so `new_text` never
// closes and the key after it runs into the value. escapeRawControlChars
// cannot save it (the value/key boundary is gone), and guessing would write
// the wrong bytes into a source file — so the call is refused, and everything
// else in the turn survives.
func TestMalformedToolInputDoesNotKillTheTurn(t *testing.T) {
	broken := "{\"edits\": [{\"new_text\": \"\tif a != \"b\" {\n\t}, \"path\": \"x.go\"}"
	if json.Valid([]byte(broken)) {
		t.Fatal("fixture: the payload must be the broken one")
	}
	if fixed, _ := escapeRawControlChars([]byte(broken)); json.Valid(fixed) {
		t.Fatal("fixture: control-char repair must NOT be enough, or this is the other bug")
	}

	fig, acc, err := drainStream(context.Background(),
		sseStream(toolTurn(t, "the prose that would have been lost", []string{broken})),
		"claude-opus-5", nopBus{})
	if err != nil {
		t.Fatalf("the turn died for one bad block: %v", err)
	}

	var prose, invokes int
	for _, c := range fig.Content {
		switch c.Type {
		case message.ContentProse:
			prose++
			if c.Text != "the prose that would have been lost" {
				t.Errorf("prose = %q", c.Text)
			}
		case message.ContentToolInvoke:
			invokes++
			raw, bad := message.MalformedArgsOf(c)
			if !bad {
				t.Fatalf("the call was not quarantined: %v", c.Arguments)
			}
			if raw != broken {
				t.Errorf("the quarantine did not keep the bytes verbatim:\n got %q\nwant %q", raw, broken)
			}
		}
	}
	if prose != 1 || invokes != 1 {
		t.Errorf("turn = %d prose + %d invokes, want 1 + 1", prose, invokes)
	}

	// The wire must stay legal: a tool_use that cannot replay would orphan its
	// tool_result on the next request.
	if !json.Valid(acc.Content[1].Input) {
		t.Error("the quarantined block is still not marshalable")
	}
	if keep, fatal := cacheableAccumulatedBlock(acc.Content[1]); !keep || fatal {
		t.Errorf("quarantined block: keep=%v fatal=%v, want keep and not fatal", keep, fatal)
	}
}

// The control-char case still repairs, and is NOT quarantined: a turn that can
// be saved whole must be saved whole.
func TestControlCharsStillRepairInsteadOfQuarantine(t *testing.T) {
	raw := "{\"path\": \"x.go\", \"new_text\": \"\tindented\"}"
	if json.Valid([]byte(raw)) {
		t.Fatal("fixture: the tab must be raw")
	}
	fig, _, err := drainStream(context.Background(),
		sseStream(toolTurn(t, "ok", []string{raw})), "claude-opus-5", nopBus{})
	if err != nil {
		t.Fatalf("repairable input must not fail: %v", err)
	}
	for _, c := range fig.Content {
		if c.Type != message.ContentToolInvoke {
			continue
		}
		if _, bad := message.MalformedArgsOf(c); bad {
			t.Fatal("a repairable call was quarantined instead of mended")
		}
		if c.Arguments["new_text"] != "\tindented" {
			t.Errorf("arguments = %v", c.Arguments)
		}
	}
}
