package chalkboard_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// raw is a tiny helper that marshals a Go value to json.RawMessage.
func raw(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// --- Snapshot.Diff / .Apply round-trip ---

func TestDiff_NoChange(t *testing.T) {
	prev := chalkboard.Snapshot{
		"cwd":   raw(t, "/foo"),
		"model": raw(t, "claude-opus-4-6"),
	}
	next := chalkboard.Snapshot{
		"cwd":   raw(t, "/foo"),
		"model": raw(t, "claude-opus-4-6"),
	}
	p := next.Diff(prev)
	assert.True(t, p.IsEmpty(), "identical snapshots produce empty patch")
}

func TestDiff_AddSetRemove(t *testing.T) {
	prev := chalkboard.Snapshot{
		"cwd":   raw(t, "/foo"),
		"label": raw(t, "alpha"),
	}
	next := chalkboard.Snapshot{
		"cwd":   raw(t, "/bar"),       // changed
		"model": raw(t, "claude-opus-4-6"), // added
		// label removed
	}
	p := next.Diff(prev)
	assert.Equal(t, raw(t, "/bar"), p.Set["cwd"])
	assert.Equal(t, raw(t, "claude-opus-4-6"), p.Set["model"])
	assert.Equal(t, []string{"label"}, p.Remove)
}

func TestApply_RoundTrip(t *testing.T) {
	prev := chalkboard.Snapshot{
		"cwd":  raw(t, "/foo"),
		"keep": raw(t, "x"),
	}
	next := chalkboard.Snapshot{
		"cwd":   raw(t, "/bar"),
		"keep":  raw(t, "x"),
		"added": raw(t, "y"),
	}
	p := next.Diff(prev)
	got := prev.Apply(p)
	assert.Equal(t, next, got, "Apply(Diff) must reconstruct the next snapshot")
}

func TestApply_DoesNotMutateReceiver(t *testing.T) {
	prev := chalkboard.Snapshot{"k": raw(t, "v1")}
	p := chalkboard.Patch{Set: map[string]json.RawMessage{"k": raw(t, "v2")}}
	_ = prev.Apply(p)
	assert.Equal(t, raw(t, "v1"), prev["k"], "Apply must not mutate the receiver")
}

func TestMerge_QWinsOnConflict(t *testing.T) {
	p := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"a": raw(t, 1),
			"b": raw(t, 2),
		},
		Remove: []string{"x"},
	}
	q := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"a": raw(t, 100), // conflicts with p
		},
		Remove: []string{"b"}, // cancels p's set of b
	}
	merged := chalkboard.Merge(p, q)
	assert.Equal(t, raw(t, 100), merged.Set["a"], "q wins on conflicting Set")
	_, hasB := merged.Set["b"]
	assert.False(t, hasB, "q's Remove cancels p's Set of the same key")
	assert.ElementsMatch(t, []string{"x", "b"}, merged.Remove)
}

// --- Patch.Entries: deterministic order ---

func TestEntries_DeterministicOrder(t *testing.T) {
	prev := chalkboard.Snapshot{"a": raw(t, "old-a")}
	p := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"zeta":  raw(t, "1"),
			"alpha": raw(t, "2"),
			"a":     raw(t, "new-a"),
		},
		Remove: []string{"omega"},
	}
	es := p.Entries(prev)
	keys := make([]string, len(es))
	for i, e := range es {
		keys[i] = e.Key
	}
	assert.Equal(t, []string{"a", "alpha", "omega", "zeta"}, keys, "entries must be sorted by key")

	// 'a' should have Old populated; 'alpha' / 'zeta' should not; 'omega' is a removal.
	for _, e := range es {
		switch e.Key {
		case "a":
			assert.Equal(t, raw(t, "old-a"), e.Old)
			assert.Equal(t, raw(t, "new-a"), e.New)
		case "alpha":
			assert.Nil(t, e.Old)
		case "omega":
			assert.True(t, e.IsRemoval())
		}
	}
}

// --- Render with default templates ---

func TestRender_DefaultTemplates_Cwd(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	prev := chalkboard.Snapshot{}
	next := chalkboard.Snapshot{"cwd": raw(t, "/home/figaro")}
	p := next.Diff(prev)

	rendered, err := chalkboard.Render(p, prev, tmpls)
	require.NoError(t, err)
	require.Len(t, rendered, 1)
	assert.Equal(t, "cwd", rendered[0].Key)
	assert.Equal(t, "Working directory: /home/figaro", rendered[0].Body)
}

func TestRender_DefaultTemplates_Model_Old_New(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	prev := chalkboard.Snapshot{"model": raw(t, "claude-sonnet")}
	next := chalkboard.Snapshot{"model": raw(t, "claude-opus")}
	p := next.Diff(prev)

	rendered, err := chalkboard.Render(p, prev, tmpls)
	require.NoError(t, err)
	require.Len(t, rendered, 1)
	assert.Equal(t, "Model changed from claude-sonnet to claude-opus.", rendered[0].Body)
}

func TestRender_UnknownKey_SilentlySkipped(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	prev := chalkboard.Snapshot{}
	next := chalkboard.Snapshot{"unknown_key": raw(t, "v")}
	p := next.Diff(prev)

	rendered, err := chalkboard.Render(p, prev, tmpls)
	require.NoError(t, err)
	assert.Empty(t, rendered, "keys without templates produce no rendered entries")
}

func TestRender_EmptyPatch(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	rendered, err := chalkboard.Render(chalkboard.Patch{}, nil, tmpls)
	require.NoError(t, err)
	assert.Empty(t, rendered)
}

func TestRender_OverrideTemplate(t *testing.T) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, writeFile(filepath.Join(dir, "cwd.tmpl"), "you are working in {{.NewString}}"))

	overridden, err := chalkboard.LoadOverrideTemplates(tmpls, dir)
	require.NoError(t, err)

	prev := chalkboard.Snapshot{}
	next := chalkboard.Snapshot{"cwd": raw(t, "/over")}
	p := next.Diff(prev)

	rendered, err := chalkboard.Render(p, prev, overridden)
	require.NoError(t, err)
	require.Len(t, rendered, 1)
	assert.Equal(t, "you are working in /over", rendered[0].Body)
}

// --- Lint ---

func TestLint_ImperativePrefix(t *testing.T) {
	rendered := []chalkboard.RenderedEntry{
		{Key: "x", Body: "YOU MUST always use ripgrep"},
		{Key: "y", Body: "Working directory: /foo"},
	}
	issues := chalkboard.Lint(rendered, chalkboard.LintOptions{})
	require.Len(t, issues, 1)
	assert.Equal(t, "x", issues[0].Key)
	assert.Contains(t, issues[0].Reason, "imperative")
}

func TestLint_Length(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	rendered := []chalkboard.RenderedEntry{
		{Key: "x", Body: string(long)},
	}
	issues := chalkboard.Lint(rendered, chalkboard.LintOptions{MaxBodyLength: 100})
	require.Len(t, issues, 1)
	assert.Contains(t, issues[0].Reason, "exceeds")
}

func TestLint_Duplicate(t *testing.T) {
	rendered := []chalkboard.RenderedEntry{
		{Key: "x", Body: "Quick of hand. Light on your feet."},
	}
	issues := chalkboard.Lint(rendered, chalkboard.LintOptions{
		DuplicateText: []string{"Quick of hand. Light on your feet."},
	})
	require.Len(t, issues, 1)
	assert.Contains(t, issues[0].Reason, "duplicates")
}

func TestLint_CleanBody(t *testing.T) {
	rendered := []chalkboard.RenderedEntry{
		{Key: "x", Body: "Working directory: /foo"},
	}
	issues := chalkboard.Lint(rendered, chalkboard.LintOptions{})
	assert.Empty(t, issues)
}

// helper
func writeFile(path, body string) error {
	return writeFileTrunc(path, []byte(body))
}
