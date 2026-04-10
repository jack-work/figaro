package tool_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/tool"
)

// fakeTool is a zero-dependency Tool implementation used to exercise
// the Registry without pulling in the real Bash/Read/Write/Edit tools.
type fakeTool struct {
	name string
}

func (f *fakeTool) Name() string             { return f.name }
func (f *fakeTool) Description() string      { return "fake " + f.name }
func (f *fakeTool) Parameters() interface{}  { return map[string]interface{}{} }
func (f *fakeTool) Execute(_ context.Context, _ map[string]interface{}, _ tool.OnOutput) (string, error) {
	return "ok", nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, r.Register(&fakeTool{name: "alpha"}))
	require.NoError(t, r.Register(&fakeTool{name: "beta"}))

	got, ok := r.Get("alpha")
	require.True(t, ok)
	assert.Equal(t, "alpha", got.Name())

	_, ok = r.Get("missing")
	assert.False(t, ok)
}

func TestRegistry_DuplicateRegister(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, r.Register(&fakeTool{name: "dup"}))
	err := r.Register(&fakeTool{name: "dup"})
	assert.Error(t, err)
}

func TestRegistry_RejectsNilAndEmptyName(t *testing.T) {
	r := tool.NewRegistry()
	assert.Error(t, r.Register(nil))
	assert.Error(t, r.Register(&fakeTool{name: ""}))
}

func TestRegistry_MustRegisterPanicsOnDup(t *testing.T) {
	r := tool.NewRegistry()
	r.MustRegister(&fakeTool{name: "x"})
	assert.Panics(t, func() {
		r.MustRegister(&fakeTool{name: "x"})
	})
}

func TestRegistry_ListAndNamesAreAlphabetical(t *testing.T) {
	r := tool.NewRegistry()
	r.MustRegister(
		&fakeTool{name: "gamma"},
		&fakeTool{name: "alpha"},
		&fakeTool{name: "beta"},
	)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, r.Names())
	list := r.List()
	require.Len(t, list, 3)
	assert.Equal(t, "alpha", list[0].Name())
	assert.Equal(t, "beta", list[1].Name())
	assert.Equal(t, "gamma", list[2].Name())
}

func TestDefaultRegistry_ContainsStandardTools(t *testing.T) {
	r := tool.DefaultRegistry(t.TempDir())
	for _, name := range []string{"bash", "read", "write", "edit"} {
		_, ok := r.Get(name)
		assert.Truef(t, ok, "expected standard tool %q to be registered", name)
	}
}
