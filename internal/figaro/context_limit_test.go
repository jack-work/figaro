package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/assert"
)

type limitProvider struct {
	perfProvider
	limit int
}

func (p limitProvider) ContextLimit(model string, snapshot form.Snapshot) int {
	return p.limit
}

func TestResolveContextLimitPrecedence(t *testing.T) {
	pinned := form.FromMap(map[string]json.RawMessage{"system.max_context_tokens": json.RawMessage(`200000`)})
	bogus := form.FromMap(map[string]json.RawMessage{"system.max_context_tokens": json.RawMessage(`"big"`)})

	// The form pin beats the provider both ways.
	assert.Equal(t, 200_000,
		resolveContextLimit(limitProvider{limit: 1_000_000}, "claude-opus-5", pinned))
	assert.Equal(t, 200_000,
		resolveContextLimit(limitProvider{limit: 100_000}, "claude-opus-5", pinned))

	assert.Equal(t, 1_000_000,
		resolveContextLimit(limitProvider{limit: 1_000_000}, "claude-opus-5", form.Snapshot{}))
	assert.Equal(t, 1_000_000,
		resolveContextLimit(limitProvider{limit: 1_000_000}, "claude-opus-5", bogus))

	assert.Equal(t, 200_000, resolveContextLimit(perfProvider{}, "claude-opus-5", pinned))
	assert.Equal(t, 0, resolveContextLimit(perfProvider{}, "claude-opus-5", form.Snapshot{}))
	assert.Equal(t, 0, resolveContextLimit(limitProvider{limit: 0}, "who-knows", form.Snapshot{}))
}

var _ provider.Provider = limitProvider{}
var _ provider.ContextLimitProvider = limitProvider{}
