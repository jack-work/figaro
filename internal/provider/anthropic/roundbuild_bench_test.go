package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// One provider round on a long, tool-heavy conversation: catch up the one
// new record, project the whole history, and write the body. The live
// daemon spends this between agent.round.send and the first byte on the
// wire, every round of every turn.

type roundBuildFixture struct {
	a      *Anthropic
	figLog store.Log[message.Message]
	form   benchForm
	aria   string
	n      int
}

type benchForm struct {
	be   *store.XwalBackend
	aria string
}

func (f benchForm) PatchesBetween(after, upTo uint64) []message.Patch {
	ps, err := f.be.FormPatchesBetween(f.aria, after, upTo)
	if err != nil || len(ps) == 0 {
		return nil
	}
	out := make([]message.Patch, len(ps))
	for i := range ps {
		out[i] = ps[i].Patch
	}
	return out
}

func (f benchForm) StateAt(version uint64) (form.Snapshot, bool) {
	s, err := f.be.FormStateAt(f.aria, version)
	return s, err == nil
}

func newRoundBuildFixture(b *testing.B, turns, resultBytes int) *roundBuildFixture {
	b.Helper()
	be, aria := store.NewTestAria(b, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		b.Fatal(err)
	}
	result := strings.Repeat("line of tool output with \"quotes\" and \\ escapes\n", resultBytes/48+1)
	appendMsg := func(m message.Message) {
		if _, err := figLog.Append(store.Entry[message.Message]{Payload: m}); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < turns; i++ {
		id := fmt.Sprintf("toolu_%06d", i)
		args := map[string]interface{}{"command": fmt.Sprintf("echo %d", i)}
		appendMsg(message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("prompt %d", i))}})
		appendMsg(message.Message{Role: message.RoleOutput, StopReason: message.StopToolInvoke, Content: []message.Content{
			message.TextContent(fmt.Sprintf("running %d", i)),
			{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "bash", Arguments: args},
		}})
		appendMsg(message.Message{Role: message.RoleInput, Content: []message.Content{message.ToolResultContent(id, "bash", result, false)}})
		appendMsg(message.Message{Role: message.RoleOutput, StopReason: message.StopEnd, Content: []message.Content{message.TextContent(fmt.Sprintf("done %d", i))}})
	}
	rows, err := be.OpenTranslator(aria, "anthropic")
	if err != nil {
		b.Fatal(err)
	}
	a, err := New(provider.Knobs{Model: "claude-test", MaxTokens: 64}, nilResolver{},
		func(string) (store.Log[[]json.RawMessage], error) { return rows, nil })
	if err != nil {
		b.Fatal(err)
	}
	f := &roundBuildFixture{a: a, figLog: figLog, form: benchForm{be: be, aria: aria}, aria: aria}
	f.send(b)
	return f
}

func (f *roundBuildFixture) send(b *testing.B) int {
	f.n++
	if _, err := f.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("round %d", f.n))},
	}}); err != nil {
		b.Fatal(err)
	}
	var size int
	err := f.a.SendWithTransport(context.Background(), provider.SendInput{AriaID: f.aria, FigLog: f.figLog, Form: f.form, MaxTokens: 64},
		&recordingBus{}, false, func(_ context.Context, body *provider.RequestBody, _ http.Header) (*http.Response, error) {
			req, _ := http.NewRequest(http.MethodPost, "http://bench.invalid/v1/messages", nil)
			body.Attach(req)
			n, err := io.Copy(io.Discard, req.Body)
			size = int(n)
			if err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseBody(evStart, evText, evDelta("end_turn"), evStop))),
			}, nil
		})
	if err != nil {
		b.Fatal(err)
	}
	return size
}

func BenchmarkRoundBuild(b *testing.B) {
	for _, turns := range []int{250, 1000} {
		b.Run(fmt.Sprintf("turns=%d", turns), func(b *testing.B) {
			f := newRoundBuildFixture(b, turns, 2048)
			b.ReportAllocs()
			b.ResetTimer()
			var size int
			for i := 0; i < b.N; i++ {
				size = f.send(b)
			}
			b.ReportMetric(float64(size), "body-bytes")
		})
	}
}
