package angelus_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
)

// --- Mock figaro for registry tests ---

type mockFigaro struct {
	id         string
	socketPath string
	killed     bool
}

func (m *mockFigaro) ID() string                              { return m.id }
func (m *mockFigaro) SocketPath() string                      { return m.socketPath }
func (m *mockFigaro) Prompt(text string)                      {}
func (m *mockFigaro) Context() []message.Message               { return nil }
func (m *mockFigaro) Subscribe() <-chan rpc.Notification       { return make(chan rpc.Notification) }
func (m *mockFigaro) Unsubscribe(ch <-chan rpc.Notification)   {}
func (m *mockFigaro) SetModel(model string)                    {}
func (m *mockFigaro) Kill()                                    { m.killed = true }
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

// --- Registry tests ---

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
	require.NoError(t, r.Bind(1234, "abc"))
	require.NoError(t, r.Bind(5678, "abc"))

	require.NoError(t, r.Kill("abc"))

	// Both PIDs should be unbound.
	id, f := r.Resolve(1234)
	assert.Empty(t, id)
	assert.Nil(t, f)
	id, f = r.Resolve(5678)
	assert.Empty(t, id)
	assert.Nil(t, f)
}

// --- PID index tests ---

func TestRegistry_BindAndResolve(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))

	require.NoError(t, r.Bind(1234, "abc"))

	id, f := r.Resolve(1234)
	assert.Equal(t, "abc", id)
	assert.NotNil(t, f)
}

func TestRegistry_ResolveUnbound(t *testing.T) {
	r := angelus.NewRegistry()
	id, f := r.Resolve(9999)
	assert.Empty(t, id)
	assert.Nil(t, f)
}

func TestRegistry_BindToNonexistentFigaro(t *testing.T) {
	r := angelus.NewRegistry()
	err := r.Bind(1234, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRegistry_BindSamePidSameFigaro_Noop(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Bind(1234, "abc"))
	require.NoError(t, r.Bind(1234, "abc")) // no-op

	assert.Equal(t, 1, r.BoundPIDCount())
	pids := r.BoundPIDs("abc")
	assert.Equal(t, []int{1234}, pids)
}

func TestRegistry_BindRebindsToNewFigaro(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Register(newMock("def")))

	require.NoError(t, r.Bind(1234, "abc"))
	require.NoError(t, r.Bind(1234, "def")) // rebind

	// Should now resolve to def.
	id, _ := r.Resolve(1234)
	assert.Equal(t, "def", id)

	// abc should have no bound PIDs.
	assert.Empty(t, r.BoundPIDs("abc"))

	// def should have the pid.
	assert.Equal(t, []int{1234}, r.BoundPIDs("def"))

	// Total count is still 1.
	assert.Equal(t, 1, r.BoundPIDCount())
}

func TestRegistry_MultiplePIDsSameFigaro(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))

	require.NoError(t, r.Bind(1234, "abc"))
	require.NoError(t, r.Bind(5678, "abc"))

	assert.Equal(t, 2, r.BoundPIDCount())
	pids := r.BoundPIDs("abc")
	assert.Len(t, pids, 2)
	assert.ElementsMatch(t, []int{1234, 5678}, pids)
}

func TestRegistry_Unbind(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Bind(1234, "abc"))

	r.Unbind(1234)

	id, f := r.Resolve(1234)
	assert.Empty(t, id)
	assert.Nil(t, f)
	assert.Empty(t, r.BoundPIDs("abc"))
}

func TestRegistry_UnbindNoop(t *testing.T) {
	r := angelus.NewRegistry()
	// Should not panic.
	r.Unbind(9999)
}

// --- List / counts ---

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
	require.NoError(t, r.Bind(1234, "abc"))

	assert.Equal(t, 1, r.FigaroCount())
	assert.Equal(t, 1, r.BoundPIDCount())
}

func TestRegistry_AllPIDs(t *testing.T) {
	r := angelus.NewRegistry()
	require.NoError(t, r.Register(newMock("abc")))
	require.NoError(t, r.Register(newMock("def")))
	require.NoError(t, r.Bind(1234, "abc"))
	require.NoError(t, r.Bind(5678, "def"))

	pids := r.AllPIDs()
	assert.ElementsMatch(t, []int{1234, 5678}, pids)
}
