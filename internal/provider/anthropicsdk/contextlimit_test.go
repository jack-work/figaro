package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/assert"
)

var _ provider.ContextLimitProvider = (*Provider)(nil)

func TestProviderContextLimit(t *testing.T) {
	p := &Provider{model: "claude-opus-5"}

	assert.Equal(t, 1_000_000, p.ContextLimit("claude-opus-5", form.Snapshot{}))
	assert.Equal(t, 1_000_000, p.ContextLimit("claude-sonnet-4-6", form.Snapshot{}))
	assert.Equal(t, 200_000, p.ContextLimit("claude-haiku-4-5-20251001", form.Snapshot{}))
	assert.Equal(t, 1_000_000, p.ContextLimit("claude-sonnet-4-5-20250929", form.Snapshot{}))
	assert.Equal(t, 0, p.ContextLimit("some-unreleased-model", form.Snapshot{}))
	assert.Equal(t, 1_000_000, p.ContextLimit("", form.Snapshot{}))

	// A window learned from the models endpoint wins over the table...
	p.windows.Learn("some-unreleased-model", 640_000)
	assert.Equal(t, 640_000, p.ContextLimit("some-unreleased-model", form.Snapshot{}))
	// ...and the user's pin wins over both.
	assert.Equal(t, 42_000, p.ContextLimit("some-unreleased-model", form.FromMap(map[string]json.RawMessage{
		"system.max_context_tokens": json.RawMessage(`42000`),
	})))
}
