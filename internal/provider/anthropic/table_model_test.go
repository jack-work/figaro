package anthropic

import (
	"testing"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/assert"
)

var _ provider.ModelTable = (*Anthropic)(nil)

// The table answers for aliases the models endpoint never lists.
func TestAnthropicTableModel(t *testing.T) {
	a := &Anthropic{}

	m, ok := a.TableModel("claude-opus-5")
	assert.True(t, ok)
	assert.Equal(t, 1_000_000, m.ContextWindow)
	assert.Equal(t, "claude-opus-5", m.ID)

	m, ok = a.TableModel("claude-opus-4-5-20251101")
	assert.True(t, ok)
	assert.Equal(t, 200_000, m.ContextWindow)

	_, ok = a.TableModel("some-unreleased-model")
	assert.False(t, ok)
}
