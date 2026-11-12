package angelus_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type joinRig struct {
	a       *angelus.Angelus
	backend *store.XwalBackend
	root    string
	aria    string
	outfit  string
}

func newJoinRig(t *testing.T) joinRig {
	t.Helper()
	root := t.TempDir()
	backend, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	outfit, err := backend.CreateOutfit("o", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	aria, err := backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, t.TempDir()), Backend: backend})
	return joinRig{a: a, backend: backend, root: root, aria: aria, outfit: outfit}
}

func (r joinRig) unclean(t *testing.T) bool {
	t.Helper()
	var found bool
	err := filepath.WalkDir(r.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == ".unclean" {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func (r joinRig) write(key string) error {
	v, _ := json.Marshal("v")
	_, err := r.backend.ApplyForm(r.aria, form.Build(form.Snapshot{}, map[string]json.RawMessage{key: v}, nil))
	return err
}

// A writer still running when the stop arrives is waited for: its write
// lands before the store closes, and the store closes clean.
func TestShutdownWaitsForAWriterBeforeClosingTheStore(t *testing.T) {
	r := newJoinRig(t)
	if !r.unclean(t) {
		t.Fatal("an open store carries .unclean")
	}
	release := make(chan struct{})
	wrote := make(chan error, 1)
	if !r.a.Go("slow writer", func() {
		<-release
		wrote <- r.write("late")
	}) {
		t.Fatal("the writer was refused before any stop")
	}
	stopped := make(chan struct{})
	go func() { r.a.Shutdown(time.Second); close(stopped) }()

	select {
	case <-stopped:
		t.Fatal("Shutdown returned while a writer was still out")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return once the writer finished")
	}
	if err := <-wrote; err != nil {
		t.Fatalf("a write made before the join ended was refused: %v", err)
	}
	if r.unclean(t) {
		t.Fatal(".unclean survived a stop whose join completed")
	}
}

// When the bound expires the straggler is named, the store keeps .unclean,
// and the straggler's late write gets an error instead of a handle.
func TestShutdownPastTheBoundNamesTheStragglerAndLeavesUnclean(t *testing.T) {
	logs := captureLogs(t)
	r := newJoinRig(t)
	r.a.SetJoinBoundForTest(50 * time.Millisecond)
	release := make(chan struct{})
	wrote := make(chan error, 1)
	r.a.Go("stuck writer", func() {
		<-release
		wrote <- r.write("too-late")
	})
	r.a.Shutdown(time.Second)

	if !strings.Contains(logs.String(), "stuck writer") {
		t.Fatalf("the timeout log does not name the straggler:\n%s", logs.String())
	}
	if !r.unclean(t) {
		t.Fatal(".unclean was cleared although the join timed out")
	}
	close(release)
	if err := <-wrote; !errors.Is(err, store.ErrClosed) {
		t.Fatalf("a write after the fence: got %v, want store.ErrClosed", err)
	}
	if _, err := r.backend.OpenFigIR(r.aria); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("a read handle after the fence: got %v, want store.ErrClosed", err)
	}
}

// After a clean stop nothing reopens the store: writes and read handles are
// refused alike.
func TestNothingReopensTheStoreAfterACleanStop(t *testing.T) {
	r := newJoinRig(t)
	r.a.Shutdown(time.Second)
	if r.unclean(t) {
		t.Fatal(".unclean survived a clean stop")
	}
	if err := r.write("after"); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("write after Close: got %v, want store.ErrClosed", err)
	}
	if _, err := r.backend.OpenFigIR(r.aria); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("OpenFigIR after Close: got %v, want store.ErrClosed", err)
	}
	if _, err := r.backend.ModelsForm(); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("ModelsForm after Close: got %v, want store.ErrClosed", err)
	}
	if _, err := r.backend.CreateConversation(r.outfit); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("CreateConversation after Close: got %v, want store.ErrClosed", err)
	}
	if r.unclean(t) {
		t.Fatal("a refused write re-marked the store")
	}
	if r.a.Go("late", func() {}) {
		t.Fatal("a goroutine was admitted after the stop sealed the join")
	}
}
