package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

func newTestCache(t *testing.T, ttl time.Duration) *LogCache {
	t.Helper()
	backend, err := NewFileBackend(t.TempDir())
	require.NoError(t, err)
	c := NewLogCache(backend, ttl)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestLogCache_SharesInstanceAcrossAcquires(t *testing.T) {
	c := newTestCache(t, 1*time.Hour)
	l1, rel1, err := c.AcquireIR("aria-1")
	require.NoError(t, err)
	defer rel1()

	l2, rel2, err := c.AcquireIR("aria-1")
	require.NoError(t, err)
	defer rel2()

	// Same instance: a write through l1 must be visible through l2.
	_, err = l1.Append(Entry[message.Message]{
		Payload: message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("shared")}},
	})
	require.NoError(t, err)
	assert.Len(t, l2.Read(), 1, "second acquirer sees writes through first")
}

func TestLogCache_RefcountKeepsAlive(t *testing.T) {
	c := newTestCache(t, 50*time.Millisecond)
	l1, rel1, err := c.AcquireIR("aria-keep")
	require.NoError(t, err)
	_, err = l1.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})
	require.NoError(t, err)

	// Hold the ref past several TTLs.
	time.Sleep(150 * time.Millisecond)

	// Another acquire still sees the entry.
	l2, rel2, err := c.AcquireIR("aria-keep")
	require.NoError(t, err)
	assert.Len(t, l2.Read(), 1, "ref-held entry must survive TTL")
	rel1()
	rel2()
}

func TestLogCache_EvictsAfterIdleTTL(t *testing.T) {
	c := newTestCache(t, 100*time.Millisecond)
	l, rel, err := c.AcquireIR("aria-idle")
	require.NoError(t, err)
	_, _ = l.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleUser}})
	rel()

	// Wait for GC to fire (loop runs at ttl/4, so ~25ms cadence; wait
	// well past ttl).
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, present := c.irs["aria-idle"]
		return !present
	}, 1*time.Second, 10*time.Millisecond, "entry should evict after idle TTL")
}

func TestLogCache_ReleasesDoublyAreNoOps(t *testing.T) {
	c := newTestCache(t, 1*time.Hour)
	_, rel, err := c.AcquireIR("aria-double")
	require.NoError(t, err)
	rel()
	rel() // second call must not drive refs negative
	c.mu.Lock()
	e, ok := c.irs["aria-double"]
	c.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, 0, e.refs)
}

func TestLogCache_TranslationDistinctFromIR(t *testing.T) {
	c := newTestCache(t, 1*time.Hour)
	_, relIR, err := c.AcquireIR("aria-mix")
	require.NoError(t, err)
	defer relIR()

	tr, relTR, err := c.AcquireTranslation("aria-mix", "anthropic")
	require.NoError(t, err)
	defer relTR()
	assert.NotNil(t, tr)

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Contains(t, c.irs, "aria-mix")
	assert.Contains(t, c.trs, trKey{"aria-mix", "anthropic"})
}

func TestLogCache_CloseShutsGCAndClosesLogs(t *testing.T) {
	c := newTestCache(t, 100*time.Millisecond)
	_, rel, err := c.AcquireIR("aria-close")
	require.NoError(t, err)
	rel()
	require.NoError(t, c.Close())
	// Acquire after Close errors.
	_, _, err = c.AcquireIR("aria-close")
	assert.Error(t, err)
}
