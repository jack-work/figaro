package angelus

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// BenchmarkDoorFanout pushes one frame through a hub to 8 following
// connections and waits until every one has it.
func BenchmarkDoorFanout(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })
	const followers = 8
	hb, chans := fanoutFixture(b, followers)
	agent := newFakeAgent()
	defer hb.bind(agent)()
	frame := map[string]any{"parts": []map[string]any{{"turn": 1, "markdown": strings.Repeat("the quick brown fox ", 25)}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := agent.notif.Notify(rpc.MethodAriaFrame, frame); err != nil {
			b.Fatal(err)
		}
		for _, ch := range chans {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				b.Fatal("a follower missed a frame")
			}
		}
	}
}

func fanoutFixture(b *testing.B, followers int) (*ariaHub, []chan frame) {
	hb := newAriaHub("abcd1234")
	path := hubDoor(b, hb, authz.AllowAll())
	chans := make([]chan frame, followers)
	for i := range chans {
		_, chans[i] = dialHubOn(b, path, hb)
	}
	return hb, chans
}
