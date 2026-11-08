package anthropic

import (
	"testing"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/assert"
)

var _ provider.ModelSeeder = (*Anthropic)(nil)

// The table answers for aliases the models endpoint never lists.
func TestAnthropicSeedsFromItsTable(t *testing.T) {
	a := &Anthropic{}

	e, ok := a.SeedModel("claude-opus-5")
	assert.True(t, ok)
	assert.Equal(t, 1_000_000, e.Window(""))
	assert.Equal(t, models.SourceMapping, e.Source("context"))

	e, ok = a.SeedModel("claude-opus-4-5-20251101")
	assert.True(t, ok)
	assert.Equal(t, 200_000, e.Window(""))

	_, ok = a.SeedModel("some-unreleased-model")
	assert.False(t, ok)
}
