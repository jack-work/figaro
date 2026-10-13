package angelus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/uiir"
)

// genesisPrefix is the genesis row plus the boot patch every conversation is born with.
const genesisPrefix = 2

func TestAriaReaderReadsDormantAria(t *testing.T) {
	backend, id := benchStore(t, 8)
	r := NewAriaReader(backend, uiir.New(nil))

	msgs, metrics, err := r.Context(id)
	require.NoError(t, err)
	require.Len(t, msgs, 8+genesisPrefix)
	require.Equal(t, message.RoleGenesis, msgs[0].Role)
	body := msgs[genesisPrefix:]
	require.Equal(t, message.RoleInput, body[0].Role)
	require.Contains(t, body[0].Content[0].Text, "message 0")

	// 4 output messages at 10 in / 20 out.
	require.Equal(t, 40, metrics.TokensIn)
	require.Equal(t, 80, metrics.TokensOut)
	require.Positive(t, metrics.ContextTokens)

	page, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 65536, false)
	require.NoError(t, err)
	require.NotEmpty(t, page.Parts, "sealed history projected to no parts")
	require.NotNil(t, page.Metrics)

	tail, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 65536, true)
	require.NoError(t, err)
	require.NotEmpty(t, tail.Parts)

	_, _, err = r.Form(id)
	require.NoError(t, err)
}

func TestAriaReaderRejectsUnknown(t *testing.T) {
	backend, _ := benchStore(t, 2)
	r := NewAriaReader(backend, uiir.New(nil))

	_, _, err := r.Context("deadbeef")
	require.Error(t, err)

	_, err = r.Page("deadbeef", aria.Anchor{}, aria.Anchor{}, 4096, false)
	require.Error(t, err)
}

func TestAriaReaderWithoutBackend(t *testing.T) {
	r := NewAriaReader(nil, uiir.New(nil))
	_, _, err := r.Context("abcd1234")
	require.ErrorContains(t, err, "no backend")
	_, _, err = r.Form("abcd1234")
	require.ErrorContains(t, err, "no backend")
}

func TestAriaReaderWithoutProjector(t *testing.T) {
	backend, id := benchStore(t, 4)
	r := NewAriaReader(backend, nil)

	page, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 4096, false)
	require.NoError(t, err)
	require.Empty(t, page.Parts)

	msgs, _, err := r.Context(id)
	require.NoError(t, err, "context does not need a projector")
	require.Len(t, msgs, 4+genesisPrefix)
}
