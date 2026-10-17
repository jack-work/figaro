package angelus_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro"
)

type mockFigaro struct {
	id         string
	socketPath string
	killed     bool
}

func (m *mockFigaro) ID() string { return m.id }
func (m *mockFigaro) Interrupt() {}
func (m *mockFigaro) Kill()      { m.killed = true }
func (m *mockFigaro) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{
		ID:        m.id,
		State:     "idle",
		Provider:  "mock",
		Model:     "mock-model",
		CreatedAt: time.Now(),
	}
}

func newMock(id string) *mockFigaro {
	return &mockFigaro{id: id, socketPath: "/tmp/" + id + ".sock"}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := angelus.NewRegistry()
	m := newMock("abc")

	require.NoError(t, r.Register(m))
	assert.Equal(t, m, r.Get("abc"))
	assert.Nil(t, r.Get("nonexistent"))
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	err := r.Register(newMock("abc"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestRegistry_Kill(t *testing.T) {
	r := angelus.NewRegistry()
	m := newMock("abc")
	require.NoError(t, r.Register(m))

	require.NoError(t, r.Kill("abc"))
	assert.True(t, m.killed)
	assert.Nil(t, r.Get("abc"))
	assert.Equal(t, 0, r.FigaroCount())
}

func TestRegistry_KillNotFound(t *testing.T) {
	r := angelus.NewRegistry()
	err := r.Kill("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRegistry_KillUnbindsPIDs(t *testing.T) {
	r := angelus.NewRegistry()
	m := newMock("abc")
	require.NoError(t, r.Register(m))
	require.NoError(t, r.Bind(1234, "abc", 0))
	require.NoError(t, r.Bind(5678, "abc", 0))

	require.NoError(t, r.Kill("abc"))

	id, f, _ := r.Resolve(1234)
	assert.Empty(t, id)
	assert.Nil(t, f)
	id, f, _ = r.Resolve(5678)
	assert.Empty(t, id)
	assert.Nil(t, f)
}

func TestRegistry_BindAndResolve(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))

	require.NoError(t, r.Bind(1234, "abc", 0))

	id, f, _ := r.Resolve(1234)
	assert.Equal(t, "abc", id)
	assert.NotNil(t, f)
}

func TestRegistry_ResolveUnbound(t *testing.T) {
	r := angelus.NewRegistry()
	id, f, _ := r.Resolve(9999)
	assert.Empty(t, id)
	assert.Nil(t, f)
}

// Existence is proven at the handler; the registry accepts a dormant id.
func TestRegistry_BindDormantAriaIsLegal(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Bind(1234, "dormant-aria", 0))

	id, f, _ := r.Resolve(1234)
	assert.Equal(t, "dormant-aria", id, "binding lost")
	assert.Nil(t, f, "a dormant aria has no agent, and that is not an error")
	assert.Equal(t, []int{1234}, r.BoundPIDs("dormant-aria"))
}

// pidToFigaro and figaroPIDs are inverses.
func TestRegistry_WakingABoundDormantAriaKeepsItsPIDs(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Bind(1234, "sleeper", 0))

	require.NoError(t, r.Register(newMock("sleeper")), "the aria wakes")

	assert.Equal(t, []int{1234}, r.BoundPIDs("sleeper"),
		"Register erased the pid set Bind had already filled")

	id, f, _ := r.Resolve(1234)
	assert.Equal(t, "sleeper", id)
	assert.NotNil(t, f, "resident now, so Resolve hands back the agent")
}

func TestRegistry_BindEmptyIDFails(t *testing.T) {
	r := angelus.NewRegistry()
	assert.Error(t, r.Bind(1234, "", 0))
}

func TestRegistry_BindSamePidSameFigaro_Noop(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Bind(1234, "abc", 0))
	require.NoError(t, r.Bind(1234, "abc", 0)) // no-op

	assert.Equal(t, 1, r.BoundPIDCount())
	pids := r.BoundPIDs("abc")
	assert.Equal(t, []int{1234}, pids)
}

func TestRegistry_BindRebindsToNewFigaro(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Register(newMock("def")))

	require.NoError(t, r.Bind(1234, "abc", 0))
	require.NoError(t, r.Bind(1234, "def", 0)) // rebind

	id, _, _ := r.Resolve(1234)
	assert.Equal(t, "def", id)

	assert.Empty(t, r.BoundPIDs("abc"))

	assert.Equal(t, []int{1234}, r.BoundPIDs("def"))

	assert.Equal(t, 1, r.BoundPIDCount())
}

func TestRegistry_MultiplePIDsSameFigaro(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))

	require.NoError(t, r.Bind(1234, "abc", 0))
	require.NoError(t, r.Bind(5678, "abc", 0))

	assert.Equal(t, 2, r.BoundPIDCount())
	pids := r.BoundPIDs("abc")
	assert.Len(t, pids, 2)
	assert.ElementsMatch(t, []int{1234, 5678}, pids)
}

func TestRegistry_Unbind(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Bind(1234, "abc", 0))

	r.Unbind(1234)

	id, f, _ := r.Resolve(1234)
	assert.Empty(t, id)
	assert.Nil(t, f)
	assert.Empty(t, r.BoundPIDs("abc"))
}

func TestRegistry_UnbindNoop(t *testing.T) {
	r := angelus.NewRegistry()
	r.Unbind(9999)
}

func TestRegistry_List(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Register(newMock("def")))

	list := r.List()
	assert.Len(t, list, 2)

	ids := []string{list[0].ID, list[1].ID}
	assert.ElementsMatch(t, []string{"abc", "def"}, ids)
}

func TestRegistry_Counts(t *testing.T) {
	r := angelus.NewRegistry()
	assert.Equal(t, 0, r.FigaroCount())
	assert.Equal(t, 0, r.BoundPIDCount())

	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Bind(1234, "abc", 0))

	assert.Equal(t, 1, r.FigaroCount())
	assert.Equal(t, 1, r.BoundPIDCount())
}

func TestRegistry_AllPIDs(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Register(newMock("def")))
	require.NoError(t, r.Bind(1234, "abc", 0))
	require.NoError(t, r.Bind(5678, "def", 0))

	pids := r.AllPIDs()
	assert.ElementsMatch(t, []int{1234, 5678}, pids)
}
