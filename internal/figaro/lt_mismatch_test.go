package figaro_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

type mismatchLog struct {
	store.Log[message.Message]
}

func (l mismatchLog) Append(entry store.Entry[message.Message]) (store.Entry[message.Message], error) {
	stamped, err := l.Log.Append(entry)
	stamped.LT++
	stamped.FigaroLT++
	return stamped, err
}

type mismatchBackend struct {
	store.Backend
	log store.Log[message.Message]
}

func (b mismatchBackend) Open(string) (store.Log[message.Message], error) {
	return b.log, nil
}

func TestAssistantSealRejectsPredictedLTMismatch(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	base, err := real.Open(id)
	require.NoError(t, err)
	backend := mismatchBackend{
		Backend: journalBackend{Backend: real, journal: &failingJournal{}},
		log:     mismatchLog{Log: base},
	}
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: canonicalThenFrameProvider{}, Backend: backend, Tools: tool.NewRegistry(),
	})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	assert.Contains(t, reason, "assistant seal LT mismatch")
	history := a.Context()
	require.NotEmpty(t, history)
	assert.Equal(t, message.RoleAssistant, history[len(history)-1].Role)
}
