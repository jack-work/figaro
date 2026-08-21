package angelus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/uiir"
)

// genesisPrefix is the genesis row plus the boot-patch input that every
// conversation is born with, ahead of anything a turn wrote.
const genesisPrefix = 2

// The load-bearing claim: a dormant aria is fully readable, and reading it
// constructs no agent and leaves nothing resident that was not already.
func TestAriaReaderReadsDormantAria(t *testing.T) {
	backend, id := benchStore(t, 8)
	r := NewAriaReader(backend, uiir.New(nil))

	msgs, metrics, err := r.Context(id)
	require.NoError(t, err)
	// A conversation's IR opens with the genesis row and the boot patch it
	// was forked with, so the count is what was written plus two. The reader
	// hands back the whole channel unfiltered, exactly as the agent does.
	require.Len(t, msgs, 8+genesisPrefix)
	require.Equal(t, message.RoleGenesis, msgs[0].Role)
	body := msgs[genesisPrefix:]
	require.Equal(t, message.RoleInput, body[0].Role)
	require.Contains(t, body[0].Content[0].Text, "message 0")

	// Token counts are pure functions of the IR, so they survive the round
	// trip exactly: 4 output messages at 10 in / 20 out.
	require.Equal(t, 40, metrics.TokensIn)
	require.Equal(t, 80, metrics.TokensOut)
	require.Positive(t, metrics.ContextTokens)

	page, err := r.Page(id, aria.Anchor{}, 65536, false)
	require.NoError(t, err)
	require.NotEmpty(t, page.Parts, "sealed history projected to no parts")
	require.NotNil(t, page.Metrics)

	tail, err := r.Page(id, aria.Anchor{}, 65536, true)
	require.NoError(t, err)
	require.NotEmpty(t, tail.Parts)

	_, _, err = r.Form(id)
	require.NoError(t, err)
}

// An unknown id must be an error, never an empty page that a client would
// render as "this aria has no history".
func TestAriaReaderRejectsUnknown(t *testing.T) {
	backend, _ := benchStore(t, 2)
	r := NewAriaReader(backend, uiir.New(nil))

	_, _, err := r.Context("deadbeef")
	require.Error(t, err)

	_, err = r.Page("deadbeef", aria.Anchor{}, 4096, false)
	require.Error(t, err)
}

// An ephemeral daemon has no store, and must say so rather than serve an
// empty conversation.
func TestAriaReaderWithoutBackend(t *testing.T) {
	r := NewAriaReader(nil, uiir.New(nil))
	_, _, err := r.Context("abcd1234")
	require.ErrorContains(t, err, "no backend")
	_, _, err = r.Form("abcd1234")
	require.ErrorContains(t, err, "no backend")
}

// A nil projector is legal: figaro-the-engine ships without
// figaro-the-display, and must serve empty pages, not panic.
func TestAriaReaderWithoutProjector(t *testing.T) {
	backend, id := benchStore(t, 4)
	r := NewAriaReader(backend, nil)

	page, err := r.Page(id, aria.Anchor{}, 4096, false)
	require.NoError(t, err)
	require.Empty(t, page.Parts)

	msgs, _, err := r.Context(id)
	require.NoError(t, err, "context does not need a projector")
	require.Len(t, msgs, 4+genesisPrefix)
}
