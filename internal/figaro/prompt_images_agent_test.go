package figaro_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

func tinyPNG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// What the provider is handed is the logged user message: the picture rides
// on it ahead of the text, untagged by any tool call, so an aria with no
// tools at all still sees it.
func TestAPromptImageLandsOnTheUserMessageBeforeItsText(t *testing.T) {
	a := newTestAgent(t, "a receipt")
	t.Cleanup(a.Kill)
	ch, unsub := subscribeChan(a)
	defer unsub()

	data := tinyPNG(t)
	require.NoError(t, a.SubmitPromptFrom(context.Background(),
		rpc.QuaRequest{Text: "total?", Images: []rpc.QuaImage{{MimeType: "image/png", Data: data}}}, ""))
	waitTurnDone(t, ch)

	var user *message.Message
	for _, m := range a.Context() {
		if m.Role == message.RoleInput && len(m.Content) > 0 {
			user = &m
			break
		}
	}
	require.NotNil(t, user, "no user message was logged")
	require.Len(t, user.Content, 2)
	require.Equal(t, message.ContentImage, user.Content[0].Type)
	require.Equal(t, "image/png", user.Content[0].MimeType)
	require.Equal(t, data, user.Content[0].Data)
	require.Empty(t, user.Content[0].ToolCallID)
	require.Equal(t, message.ContentProse, user.Content[1].Type)
	require.Equal(t, "total?", user.Content[1].Text)
}

func TestARefusedPromptImageLeavesNoTrace(t *testing.T) {
	a := newTestAgent(t, "never")
	t.Cleanup(a.Kill)

	err := a.SubmitPromptFrom(context.Background(), rpc.QuaRequest{
		Text:   "total?",
		Images: []rpc.QuaImage{{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("not a picture"))}},
	}, "")
	require.ErrorContains(t, err, "not png, jpeg, gif or webp")
	time.Sleep(50 * time.Millisecond)
	for _, m := range a.Context() {
		for _, c := range m.Content {
			require.NotEqual(t, message.ContentImage, c.Type, "a refused image reached the log")
			require.NotEqual(t, "total?", c.Text, "a refused prompt reached the log")
		}
	}
}
