package figaro_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// imageTool returns whatever content blocks it is handed, standing in for
// read (an image file) or the screenshot skill.
type imageTool struct {
	name    string
	content []message.Content
	err     error
}

func (it *imageTool) Name() string        { return it.name }
func (it *imageTool) Description() string { return "test image tool" }
func (it *imageTool) Parameters() any     { return map[string]any{} }

func (it *imageTool) Execute(context.Context, map[string]any, tool.OnOutput) ([]message.Content, error) {
	if it.err != nil {
		return nil, it.err
	}
	return it.content, nil
}

// oneRoundProvider calls one tool, then on its second Send captures the IR
// the agent has assembled — which is exactly the history any real provider
// would encode onto the wire.
type oneRoundProvider struct {
	calls    []message.Content
	sends    atomic.Int32
	mu       sync.Mutex
	seen     []message.Message
	captured chan struct{}
	once     sync.Once
}

func (p *oneRoundProvider) Name() string        { return "oneround" }
func (p *oneRoundProvider) Fingerprint() string { return "oneround/v0" }
func (p *oneRoundProvider) SetModel(string)     {}
func (p *oneRoundProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *oneRoundProvider) history() []message.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]message.Message(nil), p.seen...)
}

func (p *oneRoundProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if p.sends.Add(1) > 1 {
		p.mu.Lock()
		for _, e := range in.FigLog.Read() {
			p.seen = append(p.seen, e.Payload)
		}
		p.mu.Unlock()
		p.once.Do(func() { close(p.captured) })

		msg := message.Message{
			Role:       message.RoleOutput,
			Content:    []message.Content{message.TextContent("done")},
			StopReason: message.StopEnd,
		}
		entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
		if err != nil {
			return err
		}
		msg.LogicalTime = entry.LT
		bus.PushMessageEnd(string(msg.StopReason))
		bus.PushFigaro(msg)
		return nil
	}

	for _, c := range p.calls {
		bus.PushToolReady(c)
	}
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    p.calls,
		StopReason: message.StopToolInvoke,
	}
	entry, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg})
	if err != nil {
		return err
	}
	msg.LogicalTime = entry.LT
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func runImageTurn(t *testing.T, id string, reg *tool.Registry, calls []message.Content) []message.Message {
	t.Helper()

	prov := &oneRoundProvider{calls: calls, captured: make(chan struct{})}
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"oneround"`),
	}})
	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         id,
		SocketPath: "/tmp/" + id + ".sock",
		Provider:   prov,
		Tools:      reg,
		Form:       cb,
	})
	defer a.Kill()

	ch, _ := subscribeChan(a)
	submitPrompt(a, "look")

	timeout := time.After(5 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Method == rpc.MethodTurnDone {
				select {
				case <-prov.captured:
					return prov.history()
				default:
					t.Fatal("turn finished without a second Send")
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for turn.done")
		}
	}
}

func toolResultTic(msgs []message.Message) (message.Message, bool) {
	for _, m := range msgs {
		if m.Role != message.RoleInput {
			continue
		}
		for _, c := range m.Content {
			if c.Type == message.ContentToolResult {
				return m, true
			}
		}
	}
	return message.Message{}, false
}

func imagesOf(m message.Message) []message.Content {
	var out []message.Content
	for _, c := range m.Content {
		if c.Type == message.ContentImage {
			out = append(out, c)
		}
	}
	return out
}

func call(id, name string) message.Content {
	return message.Content{
		Type:       message.ContentToolInvoke,
		ToolCallID: id,
		ToolName:   name,
		Arguments:  map[string]any{},
	}
}

// TestToolImageReachesProvider is the regression this whole change exists
// for: a tool returning an image block used to have it silently discarded
// while assembling the tool_result, so the model saw only the placeholder
// text. The image must survive into the IR the provider encodes.
func TestToolImageReachesProvider(t *testing.T) {
	const pixel = "iVBORw0KGgoAAAANSUhEUg=="

	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&imageTool{
		name: "shot",
		content: []message.Content{
			message.TextContent("[Image: shot.png (image/png, 1.0KB)]"),
			message.ImageContent("image/png", pixel),
		},
	}))

	history := runImageTurn(t, "img-001", reg, []message.Content{call("tc_1", "shot")})

	tic, ok := toolResultTic(history)
	require.True(t, ok, "no tool_result message reached the provider")

	imgs := imagesOf(tic)
	require.Len(t, imgs, 1, "the tool's image did not survive into the provider's history")
	assert.Equal(t, pixel, imgs[0].Data)
	assert.Equal(t, "image/png", imgs[0].MimeType)
	assert.Equal(t, "tc_1", imgs[0].ToolCallID, "image must name the call that produced it")
	assert.Equal(t, "shot", imgs[0].ToolName)

	// The prose note still rides in the tool_result text, unchanged.
	var result message.Content
	for _, c := range tic.Content {
		if c.Type == message.ContentToolResult {
			result = c
		}
	}
	assert.Contains(t, result.Text, "[Image: shot.png")
	assert.False(t, result.IsError)
}

// TestToolTextOnlyUnchanged pins the no-image path: a tool that returns
// prose produces exactly the tic it always did, with no stray blocks.
func TestToolTextOnlyUnchanged(t *testing.T) {
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&imageTool{
		name:    "plain",
		content: []message.Content{message.TextContent("hello")},
	}))

	history := runImageTurn(t, "img-002", reg, []message.Content{call("tc_1", "plain")})

	tic, ok := toolResultTic(history)
	require.True(t, ok)
	require.Len(t, tic.Content, 1)
	assert.Equal(t, message.ContentToolResult, tic.Content[0].Type)
	assert.Equal(t, "hello", tic.Content[0].Text)
	assert.Empty(t, imagesOf(tic))
}

// TestToolImagesFromSeveralTools guards the multi-tool case: every image is
// carried, in call order, each still bound to its own call.
func TestToolImagesFromSeveralTools(t *testing.T) {
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&imageTool{
		name: "left",
		content: []message.Content{
			message.TextContent("left note"),
			message.ImageContent("image/png", "TEZU"),
		},
	}))
	require.NoError(t, reg.Register(&imageTool{
		name: "right",
		content: []message.Content{
			message.TextContent("right note"),
			message.ImageContent("image/jpeg", "UklHUQ=="),
			message.ImageContent("image/gif", "R0lGOD"),
		},
	}))

	history := runImageTurn(t, "img-003", reg, []message.Content{
		call("tc_l", "left"),
		call("tc_r", "right"),
	})

	tic, ok := toolResultTic(history)
	require.True(t, ok)

	imgs := imagesOf(tic)
	require.Len(t, imgs, 3)
	assert.Equal(t, []string{"tc_l", "tc_r", "tc_r"}, []string{
		imgs[0].ToolCallID, imgs[1].ToolCallID, imgs[2].ToolCallID,
	})
	assert.Equal(t, "image/gif", imgs[2].MimeType)

	// Tool results keep their canonical call order, ahead of the images.
	assert.Equal(t, message.ContentToolResult, tic.Content[0].Type)
	assert.Equal(t, message.ContentToolResult, tic.Content[1].Type)
	assert.Equal(t, "tc_l", tic.Content[0].ToolCallID)
	assert.Equal(t, "tc_r", tic.Content[1].ToolCallID)
}

// TestFailedToolCarriesNoImage records the shape of the error path: when
// Execute returns an error the outcome is synthesized error text, so there
// is no image to carry and the tic is text-only.
func TestFailedToolCarriesNoImage(t *testing.T) {
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&imageTool{
		name: "broken",
		err:  assertError{},
	}))

	history := runImageTurn(t, "img-004", reg, []message.Content{call("tc_1", "broken")})

	tic, ok := toolResultTic(history)
	require.True(t, ok)
	require.Len(t, tic.Content, 1)
	assert.True(t, tic.Content[0].IsError)
	assert.True(t, strings.HasPrefix(tic.Content[0].Text, "Error: "))
	assert.Empty(t, imagesOf(tic))
}

type assertError struct{}

func (assertError) Error() string { return "boom" }
