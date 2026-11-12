package angelus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
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

// The timeout log names a call by method, aria id and door. It never prints
// request params (troupe sign, api key mint and provider login carry
// secrets), and an aria field that is not an id prints as "-".
func TestStragglerLogNamesMethodAriaAndDoorNeverParams(t *testing.T) {
	logs := captureLogs(t)
	r := newJoinRig(t)
	r.a.SetJoinBoundForTest(50 * time.Millisecond)
	release := make(chan struct{})
	defer close(release)
	block := func(context.Context, json.RawMessage) (any, error) { <-release; return nil, nil }
	entered := make(chan struct{}, 2)
	call := func(method, aria string) {
		params, _ := json.Marshal(map[string]string{rpc.AriaKey: aria, "secret": "hunter2-do-not-log"})
		h := r.a.JoinedForTest(method, func(ctx context.Context, p json.RawMessage) (any, error) {
			entered <- struct{}{}
			return block(ctx, p)
		})
		go h(authz.WithDoor(context.Background(), authz.DoorBox), params)
	}
	call("troupe.sign", r.aria)
	call("apikey.mint", "../../etc/passwd\nforged=line")
	<-entered
	<-entered
	r.a.Shutdown(time.Second)

	out := logs.String()
	for _, want := range []string{"troupe.sign aria=" + r.aria + " door=" + authz.DoorBox.String(), "apikey.mint aria=- door=" + authz.DoorBox.String()} {
		if !strings.Contains(out, want) {
			t.Errorf("the timeout log lacks %q:\n%s", want, out)
		}
	}
	for _, never := range []string{"hunter2", "passwd", "forged"} {
		if strings.Contains(out, never) {
			t.Errorf("the timeout log printed %q from the request params:\n%s", never, out)
		}
	}
}

// Once the stop has sealed the join, a request that still arrives is refused
// with the fixed error and its handler never runs.
func TestARequestAfterTheSealIsRefusedUnrun(t *testing.T) {
	r := newJoinRig(t)
	ran := false
	h := r.a.JoinedForTest("set", func(context.Context, json.RawMessage) (any, error) { ran = true; return "ok", nil })
	if _, err := h(context.Background(), nil); err != nil || !ran {
		t.Fatalf("before the stop: err=%v ran=%v", err, ran)
	}
	ran = false
	r.a.Shutdown(time.Second)
	if _, err := h(context.Background(), nil); !errors.Is(err, angelus.ErrStoppingForTest) {
		t.Fatalf("after the stop: got %v, want the daemon-stopping refusal", err)
	}
	if ran {
		t.Fatal("the handler ran after the stop sealed the join")
	}
}

// A door served on a context its caller never cancels still stops accepting
// when the stop begins: the seal closes the listener, the accept loop
// returns, and the join has nothing to wait for (S1).
func TestAStopClosesADoorServedOnAContextNobodyCancels(t *testing.T) {
	r := newJoinRig(t)
	r.a.SetJoinBoundForTest(2 * time.Second)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- r.a.Serve(context.Background(), ln, authz.DoorNode, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the door never accepted: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	r.a.Shutdown(time.Second)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Shutdown took %v: the join waited on an accept loop the stop should have closed", took)
	}
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("the accept loop was still running after Shutdown returned")
	}
	if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		c.Close()
		t.Fatal("the door still accepted a connection after the stop")
	}
	if r.unclean(t) {
		t.Fatal(".unclean survived a stop whose only open door was an accept loop")
	}
}
