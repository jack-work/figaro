package tool_test

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jack-work/figaro/internal/tool"
)

func TestWithFileMutex_SerializesSamePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")

	var active int32
	var maxActive int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tool.WithFileMutex(path, func() error {
				cur := atomic.AddInt32(&active, 1)
				for {
					prev := atomic.LoadInt32(&maxActive)
					if cur <= prev || atomic.CompareAndSwapInt32(&maxActive, prev, cur) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), maxActive, "same-path mutex should serialize all goroutines")
}

func TestWithFileMutex_DifferentPathsConcurrent(t *testing.T) {
	dir := t.TempDir()

	var active int32
	var maxActive int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := filepath.Join(dir, "f", string(rune('a'+i))+".txt")
			_ = tool.WithFileMutex(path, func() error {
				cur := atomic.AddInt32(&active, 1)
				for {
					prev := atomic.LoadInt32(&maxActive)
					if cur <= prev || atomic.CompareAndSwapInt32(&maxActive, prev, cur) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	assert.Greater(t, maxActive, int32(1), "different-path mutexes should run concurrently")
}

func TestWithFileMutex_ErrorReleasesLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")

	boom := errors.New("boom")
	err := tool.WithFileMutex(path, func() error { return boom })
	assert.Same(t, boom, err)

	// Second call on the same path should not deadlock.
	done := make(chan struct{})
	go func() {
		_ = tool.WithFileMutex(path, func() error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second WithFileMutex call deadlocked after prior error")
	}
}
