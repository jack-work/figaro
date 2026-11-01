package figaro_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

func noisyPNG(t *testing.T, side int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	r := rand.New(rand.NewPCG(7, 9))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.Set(x, y, color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// Each queued prompt's pictures fit the inline budget alone; prompts queued
// during a turn fold into one user message, and the fold must still fit, or
// the record outgrows the segment the budget was sized for.
func TestImagePromptsQueuedDuringATurnFoldUnderTheBudget(t *testing.T) {
	img := noisyPNG(t, 160)
	segment := len(img) * 3
	settings := &config.Loaded{}
	settings.Config.Store.SegmentSize = &segment
	budget := settings.InlineImageBudget()
	require.LessOrEqual(t, len(img), budget, "fixture: one picture must fit alone")
	require.Greater(t, 3*len(img), budget, "fixture: three must not")

	bt := &blockingSteeringTool{started: make(chan struct{}), release: make(chan struct{})}
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(bt))
	prov := &staggeredProvider{
		tools:     []specTool{{id: "tc_steer", name: "steer", args: map[string]interface{}{}, readyAt: 0}},
		streamEnd: 10 * time.Millisecond,
	}
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock"`),
		"system.provider": json.RawMessage(`"staggered"`),
	}, nil))
	be, id := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend: be, Projector: uiir.New(nil), ID: id, Provider: prov,
		Tools: reg, Form: cb, Settings: settings,
	})
	defer a.Kill()

	frames, _ := subscribeChan(a)
	submitPrompt(a, "start")
	select {
	case <-bt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	for _, text := range []string{"one", "two", "three"} {
		require.NoError(t, a.SubmitPromptFrom(context.Background(),
			rpc.QuaRequest{Text: text, Images: []rpc.QuaImage{{MimeType: "image/png", Data: img}}}, ""))
	}
	close(bt.release)
	waitTurnDone(t, frames)

	var folded *message.Message
	for _, m := range conversationOnly(a.Context()) {
		if m.Role == message.RoleInput && m.Steering {
			folded = &m
		}
	}
	require.NotNil(t, folded, "the three prompts did not fold into a steer")
	images, total := 0, 0
	for _, c := range folded.Content {
		if c.Type == message.ContentImage {
			images++
			total += len(c.Data)
		}
	}
	require.Equal(t, 3, images, "a picture was dropped instead of shrunk")
	require.LessOrEqual(t, total, budget, "the folded message carries %d bytes of images, over the %d budget", total, budget)
}
