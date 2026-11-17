package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type landingBus struct {
	captureBus
	ready  []message.Content
	landed []message.Message
}

func (b *landingBus) PushToolReady(c message.Content) { b.ready = append(b.ready, c) }
func (b *landingBus) PushFigaro(m message.Message, _ ...provider.AssistantCache) {
	b.landed = append(b.landed, m)
}

// #80: a Chat reply that is only a call cut at max_tokens must still land, so
// figaro can name the dropped call; returning nothing ended the turn as "stop".
func TestAChatReplyCutInsideItsOnlyCallLandsAsALengthStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"write","arguments":"{\"path\":\"page.html\",\"content\":\"<html"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":64}}`,
			`data: [DONE]`,
		}, "\n\n")+"\n\n")
	}))
	defer srv.Close()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("write the page")},
	}}); err != nil {
		t.Fatal(err)
	}
	p, err := New(Spec{Name: "chat-cut", BaseURL: func() string { return srv.URL }, Default: Chat},
		provider.Knobs{Model: "m", MaxTokens: 64}, staticToken("k"),
		func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
	if err != nil {
		t.Fatal(err)
	}
	bus := &landingBus{}
	in := provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64,
		Snapshot: form.FromValue(form.NewValue([]byte(`{"system":{"model":"m"}}`)))}
	if err := p.Send(context.Background(), in, bus); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(bus.ready) != 0 {
		t.Errorf("a cut call was readied: %+v", bus.ready)
	}
	if len(bus.landed) != 1 {
		t.Fatalf("landed %d messages, want the length stop", len(bus.landed))
	}
	got := bus.landed[0]
	if got.StopReason != message.StopLength {
		t.Errorf("stop = %q", got.StopReason)
	}
	for _, c := range got.Content {
		if c.Type == message.ContentToolInvoke {
			t.Errorf("the cut call landed: %+v", c)
		}
	}
}
