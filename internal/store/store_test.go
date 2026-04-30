package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// --- FileStore tests ---

func TestFileStore_EmptyStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	if ctx := fs.Context(); ctx != nil {
		t.Fatalf("expected nil context, got %v", ctx)
	}
	if lt := fs.LeafTime(); lt != 0 {
		t.Fatalf("expected leaf time 0, got %d", lt)
	}
}

func TestFileStore_AppendWritesToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	lt, err := fs.Append(message.Message{
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent("hello")},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if lt != 1 {
		t.Fatalf("expected lt=1, got %d", lt)
	}

	// File should exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	// Reload from disk — should recover the message.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx := fs2.Context()
	if ctx == nil || len(ctx.Messages()) != 1 {
		t.Fatalf("expected 1 message after reload, got %v", ctx)
	}
	if ctx.Messages()[0].Content[0].Text != "hello" {
		t.Fatalf("wrong text: %s", ctx.Messages()[0].Content[0].Text)
	}
	if ctx.Messages()[0].LogicalTime != 1 {
		t.Fatalf("wrong lt: %d", ctx.Messages()[0].LogicalTime)
	}
}

func TestFileStore_LogicalTimeContinuity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("one")}})
	fs.Append(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("two")}})
	lt3, _ := fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("three")}})

	if lt3 != 3 {
		t.Fatalf("expected lt=3, got %d", lt3)
	}

	// Reload and append — should continue from lt=4.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	lt4, _ := fs2.Append(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("four")}})
	if lt4 != 4 {
		t.Fatalf("expected lt=4 after reload, got %d", lt4)
	}
}

func TestFileStore_Branch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("one")}})
	fs.Append(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("two")}})
	fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("three")}})

	if err := fs.Branch(1); err != nil {
		t.Fatalf("Branch: %v", err)
	}

	ctx := fs.Context()
	if len(ctx.Messages()) != 1 {
		t.Fatalf("expected 1 message after branch, got %d", len(ctx.Messages()))
	}

	// Verify branch was persisted.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx2 := fs2.Context()
	if len(ctx2.Messages()) != 1 {
		t.Fatalf("expected 1 message after reload, got %d", len(ctx2.Messages()))
	}
}

func TestFileStore_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("old")}})

	// Overwrite with different content.
	newMsgs := []message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("alpha")}, LogicalTime: 1},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("beta")}, LogicalTime: 2},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("gamma")}, LogicalTime: 3},
	}
	if err := fs.Checkpoint(newMsgs, 4); err != nil {
		t.Fatalf("Overwrite: %v", err)
	}

	ctx := fs.Context()
	if len(ctx.Messages()) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(ctx.Messages()))
	}
	if ctx.Messages()[2].Content[0].Text != "gamma" {
		t.Fatalf("wrong content: %s", ctx.Messages()[2].Content[0].Text)
	}

	// Reload — should see the overwritten data.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx2 := fs2.Context()
	if len(ctx2.Messages()) != 3 {
		t.Fatalf("expected 3 messages after reload, got %d", len(ctx2.Messages()))
	}
}

func TestFileStore_Remove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("doomed")}})

	if err := fs.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted")
	}

	// After remove, context is empty.
	if ctx := fs.Context(); ctx != nil {
		t.Fatalf("expected nil context after remove, got %v", ctx)
	}
}

func TestFileStore_CreatesMissingDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "test.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	_, err = fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("deep")}})
	if err != nil {
		t.Fatalf("Append with nested dirs: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created in nested dir: %v", err)
	}
}

// --- MemStore tests (standalone, no downstream) ---

func TestMemStore_Standalone(t *testing.T) {
	s := NewMemStore()

	if ctx := s.Context(); ctx != nil {
		t.Fatalf("expected nil context")
	}

	lt, _ := s.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}})
	if lt != 1 {
		t.Fatalf("expected lt=1, got %d", lt)
	}

	ctx := s.Context()
	if ctx == nil || len(ctx.Messages()) != 1 {
		t.Fatalf("expected 1 message")
	}

	// Flush is no-op.
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Clear is no-op on downstream, but clears memory.
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if ctx := s.Context(); ctx != nil {
		t.Fatalf("expected nil after clear")
	}
}

// --- MemStore + FileStore integration tests ---

func TestMemStore_FlushToFileStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	ms := NewMemStoreWith(fs)

	// Append several messages.
	ms.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("one")}})
	ms.Append(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("two")}})
	ms.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("three")}})

	// Before flush, file should not exist (MemStore doesn't write on Append).
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should not exist before flush")
	}

	// Flush.
	if err := ms.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Reload from disk — should have all 3 messages.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx := fs2.Context()
	if ctx == nil || len(ctx.Messages()) != 3 {
		t.Fatalf("expected 3 messages on disk, got %v", ctx)
	}

	// Memory still has all 3.
	memCtx := ms.Context()
	if len(memCtx.Messages()) != 3 {
		t.Fatalf("memory should still have 3 messages")
	}
}

func TestMemStore_SeedFromFileStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.json")

	// Pre-populate the file store.
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("old msg")}})
	fs.Append(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("old reply")}})

	// Create a new MemStore backed by the same file — should seed.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ms := NewMemStoreWith(fs2)

	ctx := ms.Context()
	if ctx == nil || len(ctx.Messages()) != 2 {
		t.Fatalf("expected 2 seeded messages, got %v", ctx)
	}
	if ctx.Messages()[0].Content[0].Text != "old msg" {
		t.Fatalf("wrong seeded content: %s", ctx.Messages()[0].Content[0].Text)
	}

	// Next append should continue logical time.
	lt, _ := ms.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("new")}})
	if lt != 3 {
		t.Fatalf("expected lt=3, got %d", lt)
	}
}

func TestMemStore_ClearCascadesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ms := NewMemStoreWith(fs)

	ms.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("doomed")}})
	ms.Flush()

	// File exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist after flush")
	}

	// Clear.
	if err := ms.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	// Memory is empty.
	if ctx := ms.Context(); ctx != nil {
		t.Fatalf("expected nil after clear")
	}

	// File is gone.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted after clear")
	}
}

func TestMemStore_CloseFlushes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ms := NewMemStoreWith(fs)

	ms.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("saved")}})

	// Close should flush.
	if err := ms.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reload — data should be on disk.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx := fs2.Context()
	if ctx == nil || len(ctx.Messages()) != 1 {
		t.Fatalf("expected 1 message after close+reload")
	}
}

func TestMemStore_MultipleFlushesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aria.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ms := NewMemStoreWith(fs)

	// First turn.
	ms.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("turn1")}})
	ms.Flush()

	// Second turn.
	ms.Append(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("reply1")}})
	ms.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("turn2")}})
	ms.Flush()

	// Reload — should have all 3, not duplicated.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx := fs2.Context()
	if len(ctx.Messages()) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(ctx.Messages()))
	}
}

// --- FileBackend tests ---

func TestFileBackend_List_Empty(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}
	arias, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arias) != 0 {
		t.Fatalf("expected 0 arias, got %d", len(arias))
	}
}

func TestFileBackend_List_NonexistentDir(t *testing.T) {
	// NewFileBackend creates the directory if missing; listing an
	// empty backend should simply return zero results.
	dir := filepath.Join(t.TempDir(), "fresh")
	b, err := NewFileBackend(dir)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}
	arias, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arias) != 0 {
		t.Fatalf("expected 0 arias")
	}
}

func TestFileBackend_List_FindsArias(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBackend(dir)
	if err != nil {
		t.Fatalf("NewFileBackend: %v", err)
	}

	ds1, _ := b.Open("abc123")
	ds1.Checkpoint([]message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}, LogicalTime: 1},
		{Role: message.RoleAssistant, Content: []message.Content{message.TextContent("hi")}, LogicalTime: 2},
	}, 3)

	ds2, _ := b.Open("def456")
	ds2.Checkpoint([]message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("one")}, LogicalTime: 1},
	}, 2)

	// Also create a non-json file — should be ignored.
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0600)

	arias, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arias) != 2 {
		t.Fatalf("expected 2 arias, got %d", len(arias))
	}

	byID := make(map[string]AriaInfo)
	for _, a := range arias {
		byID[a.ID] = a
	}
	if byID["abc123"].MessageCount != 2 {
		t.Fatalf("abc123: expected 2 messages, got %d", byID["abc123"].MessageCount)
	}
	if byID["def456"].MessageCount != 1 {
		t.Fatalf("def456: expected 1 message, got %d", byID["def456"].MessageCount)
	}
}

func TestFileBackend_Remove(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewFileBackend(dir)

	ds, _ := b.Open("doomed")
	ds.Checkpoint([]message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("bye")}, LogicalTime: 1},
	}, 2)

	if err := b.Remove("doomed"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "doomed.json")); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted")
	}

	// Remove again — no-op.
	if err := b.Remove("doomed"); err != nil {
		t.Fatalf("Remove idempotent: %v", err)
	}
}

func TestFileBackend_Remove_Nonexistent(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewFileBackend(dir)
	if err := b.Remove("ghost"); err != nil {
		t.Fatalf("Remove nonexistent: %v", err)
	}
}

// --- Metadata persistence tests ---

func TestFileStore_MetaPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	meta := &AriaMeta{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
		Cwd:      "/home/test",
		Root:     "/home/test/project",
	}
	fs.SetMeta(meta)

	// Write something to disk.
	fs.Append(message.Message{Role: message.RoleUser, Content: []message.Content{message.TextContent("hello")}})

	// Reload and check meta.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := fs2.Meta()
	if got == nil {
		t.Fatal("expected meta after reload, got nil")
	}
	if got.Provider != meta.Provider {
		t.Fatalf("provider: got %q, want %q", got.Provider, meta.Provider)
	}
	if got.Model != meta.Model {
		t.Fatalf("model: got %q, want %q", got.Model, meta.Model)
	}
	if got.Cwd != meta.Cwd {
		t.Fatalf("cwd: got %q, want %q", got.Cwd, meta.Cwd)
	}
	if got.Root != meta.Root {
		t.Fatalf("root: got %q, want %q", got.Root, meta.Root)
	}
}

func TestListArias_IncludesMeta(t *testing.T) {
	dir := t.TempDir()
	b, _ := NewFileBackend(dir)

	ds, err := b.Open("withmeta")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ds.SetMeta(&AriaMeta{Provider: "mock", Model: "test-v1"})
	ds.Checkpoint([]message.Message{
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("x")}, LogicalTime: 1},
	}, 2)

	arias, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arias) != 1 {
		t.Fatalf("expected 1 aria, got %d", len(arias))
	}
	if arias[0].Meta == nil {
		t.Fatal("expected meta in AriaInfo")
	}
	if arias[0].Meta.Provider != "mock" {
		t.Fatalf("provider: got %q", arias[0].Meta.Provider)
	}
}

func TestMemStore_DownstreamAccessor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "downstream.json")

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ms := NewMemStoreWith(fs)
	assert.NotNil(t, ms.Downstream())

	standalone := NewMemStore()
	assert.Nil(t, standalone.Downstream())
}

func TestFileStore_Seed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.json")

	fs, err := NewFileStore(path)
	require.NoError(t, err)

	// Empty store seeds empty.
	msgs, nextLT, err := fs.Seed()
	require.NoError(t, err)
	assert.Empty(t, msgs)
	assert.Equal(t, uint64(1), nextLT)

	// Append some messages.
	fs.Append(message.Message{
		Role:    message.RoleUser,
		Content: []message.Content{message.TextContent("hello")},
	})
	fs.Append(message.Message{
		Role:    message.RoleAssistant,
		Content: []message.Content{message.TextContent("hi")},
	})

	// Seed returns messages and correct nextLT.
	msgs, nextLT, err = fs.Seed()
	require.NoError(t, err)
	assert.Len(t, msgs, 2)
	assert.Equal(t, uint64(3), nextLT)
	assert.Equal(t, message.RoleUser, msgs[0].Role)
	assert.Equal(t, message.RoleAssistant, msgs[1].Role)

	// Seed returns a copy — mutating it doesn't affect the store.
	msgs[0].Role = "mutated"
	msgs2, _, _ := fs.Seed()
	assert.Equal(t, message.RoleUser, msgs2[0].Role)
}
