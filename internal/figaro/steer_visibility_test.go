package figaro_test

// HOW LONG IS A STEER DURABLE AND INVISIBLE?
//
// The queue row clears the moment the drain loop lifts the message and appends
// it. If nothing announces that append, the text reaches the client only when
// something ELSE composes a frame -- the next provider round's first chunk --
// so the reader watches their message leave the queue and arrive seconds
// later. That interval is the bug, and this is its instrument.
//
// The provider stalls its second round by steerTTFT, standing in for a real
// time-to-first-token, and the measurement is the gap between the two events a
// reader actually sees:
//
//	t_clear   the queue intrinsic says the steer left the drawer
//	t_visible an aria frame carries the steer's text
//
// THE STEER MUST BE QUEUED BEFORE THE FIRST ROUND ENDS or there is nothing to
// be early to: a mock round completes in a millisecond, so round one waits for
// the test to say the message is in the queue. Submitting on a timer instead
// measured a message that sat queued for the whole turn.
//
//	go test ./internal/figaro -run TestSteerVisibilityGap -v -count=1

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// steerTTFT is the stall before the second round's first chunk: the quantity
// the bug converts into user-visible latency. Long enough to dwarf scheduling
// noise, short enough to keep the test quick, and settable
// (FIGARO_STEER_TTFT=1s) so a reader can check that the gap TRACKS it rather
// than being some constant of the fixture.
var steerTTFT = func() time.Duration {
	if v := os.Getenv("FIGARO_STEER_TTFT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 3 * time.Second
}()

const steerToken = "SENTINELSTEER"

// steerProvider calls a tool on round one, waits for the steer to be queued
// before finishing it, and stalls round two.
type steerProvider struct {
	mu     sync.Mutex
	sends  int
	queued chan struct{} // closed once the steer is in the queue
}

func (p *steerProvider) Name() string                                             { return "mock" }
func (p *steerProvider) Fingerprint() string                                      { return "mock/v0" }
func (p *steerProvider) SetModel(string)                                          {}
func (p *steerProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *steerProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.sends++
	round := p.sends
	p.mu.Unlock()

	var msg message.Message
	if round == 1 {
		bus.PushDelta(message.Content{Type: message.ContentProse, Text: "calling a tool"})
		select {
		case <-p.queued:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
		msg = message.Message{
			Role: message.RoleOutput,
			Content: []message.Content{
				{Type: message.ContentProse, Text: "calling a tool"},
				{Type: message.ContentToolInvoke, ToolCallID: "tc_1", ToolName: "bash",
					Arguments: map[string]any{"command": "true"}},
			},
			StopReason: message.StopToolInvoke,
		}
	} else {
		// THE STALL: nothing is pushed for steerTTFT, which is exactly the
		// window in which a steer nobody announced is invisible.
		select {
		case <-time.After(steerTTFT):
		case <-ctx.Done():
			return ctx.Err()
		}
		bus.PushDelta(message.Content{Type: message.ContentProse, Text: "all done"})
		msg = message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{message.TextContent("all done")},
			StopReason: message.StopEnd,
		}
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func TestSteerVisibilityGap(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	prov := &steerProvider{queued: make(chan struct{})}
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/test-figaro-steer-gap.sock",
		Provider:   prov,
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	t.Cleanup(a.Kill)

	ch, unsub := subscribeChan(a)
	defer unsub()
	a.SubmitPrompt(rpc.QuaRequest{Text: "do the thing"})
	// A steer is an ordinary message that arrives WHILE A TURN RUNS, and the
	// drain classifies it there. Submitted back to back with the opener it is
	// not a steer at all: the inbox coalesces the contiguous run into the
	// inquiry (measured: the second message reported "merged"). So it goes in
	// only once the turn is thinking, which round one is holding open.
	steer := sync.OnceFunc(func() { a.SubmitPrompt(rpc.QuaRequest{Text: steerToken}) })

	start := time.Now()
	var tClear, tVisible time.Duration
	var steerItem string
	release := sync.OnceFunc(func() { close(prov.queued) })
	defer release()
	deadline := time.After(30 * time.Second)
loop:
	for {
		select {
		case n := <-ch:
			switch n.Method {
			case rpc.MethodFormDelta:
				d, ok := n.Params.(rpc.FormDelta)
				if !ok {
					continue
				}
				if d.Intrinsic == "runtime" {
					if strings.Contains(patchText(d.Patch), string(rpc.RuntimeThinking)) {
						steer()
					}
					continue
				}
				if d.Intrinsic != "queue" {
					continue
				}
				// THE ROW THIS TEST WATCHES IS THE STEER'S. The opening prompt
				// commits a millisecond in, so matching "committed" alone
				// timed the wrong message. Learn the steer's item id from the
				// patch that carries its text, then time THAT id's exit.
				if steerItem == "" && strings.Contains(patchText(d.Patch), steerToken) {
					steerItem = itemIDOf(d.Patch, steerToken)
					start = time.Now()
					release() // the steer is queued: let round one finish
					continue
				}
				if tClear == 0 && steerItem != "" && leftTheDrawer(d.Patch, steerItem) {
					tClear = time.Since(start)
				}
			case rpc.MethodAriaFrame:
				if tVisible != 0 {
					continue
				}
				for _, part := range n.Params.(aria.Page).Parts {
					if part.Live == nil {
						continue
					}
					for _, nd := range part.Live.Nodes {
						if strings.Contains(nodeText(nd), steerToken) {
							tVisible = time.Since(start)
						}
					}
				}
			case rpc.MethodTurnDone:
				break loop
			}
		case <-deadline:
			t.Fatal("timeout waiting for turn.done")
		}
	}

	if prov.sends < 2 {
		t.Fatalf("the provider ran %d round(s); a steer needs a second one", prov.sends)
	}
	if tClear == 0 || tVisible == 0 {
		t.Fatalf("incomplete observation: clear=%v visible=%v (steer item %q)", tClear, tVisible, steerItem)
	}
	gap := tVisible - tClear
	t.Logf("MEASURE ttft=%v clear=%v visible=%v gap=%v",
		steerTTFT, tClear.Round(time.Millisecond), tVisible.Round(time.Millisecond),
		gap.Round(time.Millisecond))

	// THE ASSERTION IS THE POINT, and the bound is generous on purpose: the
	// defect makes the gap the WHOLE stall, so anything near it is the bug and
	// anything far below it is a frame. Measured at 8ee80d7c: 3.001s with a 3s
	// stall, 1.000s with a 1s stall -- the gap was the time-to-first-token,
	// exactly. On feat/journal: 0ms at both.
	if gap > steerTTFT/2 {
		t.Fatalf("the steer was durable and INVISIBLE for %v (stall %v). Its queue row "+
			"cleared at %v and its text did not arrive until %v, so a reader watched "+
			"their message leave the queue and land seconds later: appending must "+
			"announce, see journal.go", gap.Round(time.Millisecond), steerTTFT,
			tClear.Round(time.Millisecond), tVisible.Round(time.Millisecond))
	}
}

func nodeText(d aria.NodeDelta) string {
	var b strings.Builder
	for _, v := range d.Set {
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
	}
	for _, p := range d.Patch {
		b.WriteString(p.Ins)
	}
	return b.String()
}

// itemIDOf finds the "items.<id>." prefix whose text is the token.
func itemIDOf(p message.Patch, token string) string {
	for _, e := range p.Entries() {
		if !strings.HasSuffix(e.Key, ".text") || !strings.Contains(string(e.New), token) {
			continue
		}
		return strings.TrimSuffix(e.Key, "text")
	}
	return ""
}

// leftTheDrawer reports whether this patch takes the item out of the drawer:
// either its keys are removed or its state becomes committed.
func leftTheDrawer(p message.Patch, item string) bool {
	for _, e := range p.Entries() {
		if !strings.HasPrefix(e.Key, item) {
			continue
		}
		if len(e.New) == 0 { // the key was removed: the row is gone
			return true
		}
		if e.Key == item+"state" && strings.Contains(string(e.New), string(rpc.QueueStateCommitted)) {
			return true
		}
	}
	return false
}

func patchText(p message.Patch) string {
	b, _ := json.Marshal(p)
	return string(b)
}
