package angelus

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/coder/websocket"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
)

// One aria call on an open connection through each door, against the same
// hub and agent: what the web door adds over the unix door.
func BenchmarkDoorCallUnix(b *testing.B) {
	quiet(b)
	hb := newAriaHub("abcd1234")
	defer hb.bind(newDrainAgent())()
	client, _ := dialDoor(b, hubDoor(b, hb, authz.AllowAll()), hb.id)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out map[string]any
		if err := client.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hi"}, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoorCallWeb(b *testing.B) {
	quiet(b)
	hb := newAriaHub("abcd1234")
	defer hb.bind(newDrainAgent())()
	c, _, err := dialWeb(b, serveWeb(b, hb, Web{Admit: admitAs(webPrincipal)}), http.Header{"Remote-User": {"gluck"}})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": rpc.MethodQua,
		"params": map[string]any{rpc.AriaKey: hb.id, "text": "hi"}})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Write(ctx, websocket.MessageText, req); err != nil {
			b.Fatal(err)
		}
		if _, _, err := c.Read(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func quiet(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
}

type drainAgent struct{}

func newDrainAgent() drainAgent { return drainAgent{} }

func (drainAgent) Handle(context.Context, string, json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}

func (drainAgent) Subscribe(figaro.Notifier) func() { return func() {} }
