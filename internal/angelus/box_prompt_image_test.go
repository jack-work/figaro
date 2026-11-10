package angelus_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/provider"
)

type seenTurn struct {
	tools []provider.Tool
	input message.Message
}

type promptRecorder struct{ seen chan seenTurn }

func (*promptRecorder) Name() string                                         { return "mock" }
func (*promptRecorder) Fingerprint() string                                  { return "recorder/v1" }
func (*promptRecorder) SetModel(string)                                      {}
func (*promptRecorder) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *promptRecorder) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	var last message.Message
	for _, e := range in.FigLog.Read() {
		if e.Payload.Role == message.RoleInput && len(e.Payload.Content) > 0 {
			last = e.Payload
		}
	}
	msg := message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("seen")}, StopReason: message.StopEnd}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	p.seen <- seenTurn{tools: in.Tools, input: last}
	return nil
}

func receiptJPEG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 40, 90)), nil); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// PAYUP's intake: a host app behind the box door prompts an aria that has no
// tools at all, and the receipt must reach the model as a picture on the
// user turn, not through any tool.
func TestABoxSendsAPictureToAnAriaWithNoTools(t *testing.T) {
	sb := serveBoxDoor(t, true)
	door := filepath.Join(filepath.Dir(sb.sock), "door.sock")
	p := &promptRecorder{seen: make(chan seenTurn, 1)}
	r := newRigProviding(t, defaultPolicy,
		fmt.Sprintf("[sandbox.boxes.judge]\nexec = %q\ndoor = %q\n", sb.sock, door),
		func(string, provider.Knobs) (provider.Provider, error) { return p, nil })
	ln, err := angelus.ListenUnix(door, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.a.Serve(ctx, ln, authz.DoorBox, authz.BoxAdmit("judge")); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	raw, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"judged"}, Patch: heldPatch(t, map[string]any{"system.tools": []string{}})})
	out, err := r.h[rpc.MethodCreate](asOwner(), raw)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(rpc.CreateResponse).FigaroID
	c := doorRig{grantRig: r, sb: sb, door: door}.dial(t)

	img := receiptJPEG(t)
	refused := boxOnAria(t, c, rpc.MethodQua, id, rpc.QuaRequest{Text: "total?", Images: []rpc.QuaImage{{MimeType: "image/jpeg", Data: base64.StdEncoding.EncodeToString([]byte("<svg/>"))}}}, nil)
	if refused == nil || !strings.Contains(refused.Error(), "not png, jpeg, gif or webp") {
		t.Fatalf("a non-image through the box door must be refused, got %v", refused)
	}
	if err := boxOnAria(t, c, rpc.MethodQua, id, rpc.QuaRequest{Text: "total?", Images: []rpc.QuaImage{{MimeType: "image/jpeg", Data: img}}}, nil); err != nil {
		t.Fatalf("the box could not send a picture: %v", err)
	}

	var got seenTurn
	select {
	case got = <-p.seen:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	if len(got.tools) != 0 {
		t.Errorf("a tools=[] aria was offered %d tools", len(got.tools))
	}
	if len(got.input.Content) != 2 {
		t.Fatalf("want [image, text] on the user turn, got %+v", got.input.Content)
	}
	pic, text := got.input.Content[0], got.input.Content[1]
	if pic.Type != message.ContentImage || pic.MimeType != "image/jpeg" || pic.Data != img || pic.ToolCallID != "" {
		t.Errorf("first block is not the receipt: %+v", pic)
	}
	if text.Type != message.ContentProse || !strings.Contains(text.Text, "total?") {
		t.Errorf("second block is not the prompt: %+v", text)
	}
}
