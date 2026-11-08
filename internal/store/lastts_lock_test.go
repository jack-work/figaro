//go:build unix

package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func channelDirsOf(t *testing.T, root string) map[string]string {
	t.Helper()
	dirs := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base, ext, ok := strings.Cut(d.Name(), ".")
		if ok && len(base) == 20 && strings.Trim(base, "0123456789") == "" {
			dirs[filepath.Dir(path)] = "." + ext
		}
		return nil
	})
	return dirs
}

// coldAria mints an aria in a fresh store, reopens the store so its last
// stamp is not yet known, and plants a FIFO as the newest segment of each of
// its channels: the cold probe's open of it blocks until release is called.
func coldAria(t *testing.T) (be *XwalBackend, aria, warm string, release func(), openOne func(string), fifos []string) {
	t.Helper()
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	warm, _, err = be.ForkWith("", 0, patchOf(t, map[string]string{"mantra": `"warm"`}))
	if err != nil {
		t.Fatal(err)
	}
	before := channelDirsOf(t, dir)
	aria, _, err = be.ForkWith("", 0, patchOf(t, map[string]string{"mantra": `"cold"`}))
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	for d, ext := range channelDirsOf(t, dir) {
		if _, seen := before[d]; seen {
			continue
		}
		fifo := filepath.Join(d, "99999999999999999999"+ext)
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		fifos = append(fifos, fifo)
	}
	if len(fifos) == 0 {
		t.Fatal("found no channel of the cold aria to plant a FIFO in")
	}
	sort.Strings(fifos)
	if be, err = NewXwalBackend(dir, 0); err != nil {
		t.Fatal(err)
	}
	if be.Store().LastTS(warm) == 0 {
		t.Fatal("the warm aria has no stamp")
	}
	var mu sync.Mutex
	opened := map[string]bool{}
	tryOpen := func(fifo string) bool {
		mu.Lock()
		defer mu.Unlock()
		if opened[fifo] {
			return true
		}
		f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return false
		}
		f.Close()
		opened[fifo] = true
		return true
	}
	release = func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			all := true
			for _, fifo := range fifos {
				all = tryOpen(fifo) && all
			}
			if all {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	openOne = func(fifo string) {
		deadline := time.Now().Add(2 * time.Second)
		for !tryOpen(fifo) {
			if time.Now().After(deadline) {
				t.Errorf("no probe opened %s", fifo)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Cleanup(func() {
		release()
		_ = be.Close()
	})
	return be, aria, warm, release, openOne, fifos
}

// A cold LastTS read a tail frame of every channel while holding the store's
// lock, so one slow probe stalled every other aria's LastTS (and with it every
// listing row) behind it. The probe now runs outside the lock: another aria's
// LastTS returns while the probe is still blocked on its file.
func TestAColdLastTSProbeDoesNotHoldTheStoreLock(t *testing.T) {
	be, aria, warm, release, _, _ := coldAria(t)
	probed := make(chan struct{})
	go func() {
		defer close(probed)
		be.Store().LastTS(aria)
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-probed:
		t.Fatal("the cold probe did not block on its planted FIFO; the test proves nothing")
	default:
	}
	other := make(chan int64, 1)
	go func() { other <- be.Store().LastTS(warm) }()
	select {
	case ts := <-other:
		if ts == 0 {
			t.Error("the warm aria lost its stamp")
		}
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("another aria's LastTS waited on a cold probe of a different aria")
	}
	release()
	<-probed
}

// Readers of one cold aria share a single probe: a reader that arrives while
// the probe is under way waits for it rather than starting its own. The
// first probe is let past its first FIFO before the second reader arrives,
// so a second probe would block there with no writer left to free it. The
// store lock gave this for free when it was held across the probe; the
// probe's own lock gives it now.
func TestColdLastTSReadersOfOneAriaShareOneProbe(t *testing.T) {
	be, aria, _, release, openOne, fifos := coldAria(t)
	if len(fifos) < 2 {
		t.Fatalf("the cold aria has %d channels, the test needs two", len(fifos))
	}
	done := make(chan struct{}, 2)
	read := func() {
		be.Store().LastTS(aria)
		done <- struct{}{}
	}
	go read()
	openOne(fifos[0])
	time.Sleep(20 * time.Millisecond)
	go read()
	time.Sleep(50 * time.Millisecond)
	for _, fifo := range fifos[1:] {
		openOne(fifo)
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			release()
			t.Fatal("a reader that arrived during the probe probed again and blocked on a FIFO already released")
		}
	}
}
