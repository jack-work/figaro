package figaro_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

func deliveriesIn(t *testing.T, be store.Backend, id string) []message.Content {
	t.Helper()
	log, err := be.OpenFigIR(id)
	require.NoError(t, err)
	var out []message.Content
	for _, entry := range log.Read() {
		for _, c := range entry.Payload.Content {
			if c.Delivery != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

// A delivery's text is another party's and opaque: it reaches the
// conversation as it was sent, a quote token in it included, in a block that
// carries the delivery's id and the principal that delivered it.
func TestADeliveryIsAppendedUnderItsIDWithItsTextUntouched(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	p := newSpoolProvider(false)
	a := spoolAgent(t, be, id, figaro.NewSpool(filepath.Join(t.TempDir(), "spool")), p)
	defer a.Kill()
	text := "Ada: see <1.0:1-4>! and <@form>"
	require.NoError(t, a.SubmitDelivery("user:broker", "m1", text))
	require.Contains(t, p.await(t), "Ada: see")
	got := deliveriesIn(t, be, id)
	require.Len(t, got, 1)
	require.Equal(t, message.Content{Type: message.ContentProse, Text: text, Principal: "user:broker", Delivery: "m1"}, got[0])
}

// A delivery accepted and not yet run is named by the agent that holds it
// and is in the spool; the next agent on the same store runs it, still
// under its id. So a retry across a restart finds the id in the queue or in
// the conversation.
func TestAQueuedDeliveryKeepsItsIDAcrossARestart(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	spool := figaro.NewSpool(filepath.Join(t.TempDir(), "spool"))

	a1 := spoolAgent(t, be, id, spool, newSpoolProvider(false))
	a1.Suspend()
	require.NoError(t, a1.SubmitDelivery("user:broker", "m7", "late"))
	require.Equal(t, []string{"m7"}, a1.UnansweredDeliveries())
	a1.Kill()
	require.Empty(t, deliveriesIn(t, be, id), "a suspended agent ran the delivery")

	held := newSpoolProvider(true)
	a2 := spoolAgent(t, be, id, spool, held)
	defer a2.Kill()
	require.Contains(t, held.await(t), "late")
	deadline := time.Now().Add(10 * time.Second)
	for len(deliveriesIn(t, be, id)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := deliveriesIn(t, be, id)
	require.Len(t, got, 1)
	require.Equal(t, "m7", got[0].Delivery)
	require.Equal(t, "late", got[0].Text)
}
