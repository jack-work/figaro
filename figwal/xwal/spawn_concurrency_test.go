package xwal

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A spawn in progress is held open by hand: a second spawn must finish
// beside it, where the old exclusive create lock made every fork wait for
// every other.
func TestASpawnDoesNotWaitOnAnotherSpawn(t *testing.T) {
	f, first := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	second, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Append(second, 0, []byte(`"m"`), nil); err != nil {
		t.Fatal(err)
	}
	end, err := f.beginSpawn()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 3)
	go func() { _, err := f.ForkTail(second); done <- err }()
	go func() { _, err := f.ForkAt(second, 1); done <- err }()
	go func() { _, err := f.SpawnUnderStump("s"); done <- err }()
	for range 3 {
		select {
		case err := <-done:
			if err != nil {
				end()
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			end()
			t.Fatal("a spawn waited for another spawn to finish")
		}
	}
	end()
	if _, err := f.ForkTail(first); err != nil {
		t.Fatal(err)
	}
}

// A removal still reshapes the tree alone: it waits for a spawn in
// progress, so it never takes a node a spawn is reading from.
func TestARemovalWaitsForASpawnInProgress(t *testing.T) {
	f, _ := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	victim, err := f.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	end, err := f.beginSpawn()
	if err != nil {
		t.Fatal(err)
	}
	removed := make(chan error, 1)
	go func() { removed <- f.Remove(victim, false) }()
	select {
	case err := <-removed:
		end()
		t.Fatalf("the removal ran beside a spawn in progress (err %v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	end()
	select {
	case err := <-removed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the removal never ran after the spawn ended")
	}
	if f.hasHead(victim) {
		t.Fatal("the removed trunk still has a head")
	}
}

// Retain reads a node's pins and then drops the segments nothing pins. A
// spawn in between would pin a segment the drop did not know about, so a
// retain waits for every spawn in progress.
func TestARetainWaitsForASpawnInProgress(t *testing.T) {
	fx := newRetainFixture(t)
	fx.patch(10)
	end, err := fx.f.beginSpawn()
	if err != nil {
		t.Fatal(err)
	}
	retained := make(chan error, 1)
	go func() {
		_, err := fx.f.Retain(fx.formNode(), "chalkboard", false)
		retained <- err
	}()
	select {
	case err := <-retained:
		end()
		t.Fatalf("the retain ran beside a spawn in progress (err %v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	end()
	select {
	case err := <-retained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retain never ran after the spawn ended")
	}
	fx.childrenRead("after the retain")
}

// A quarantine release checks that no other figaro shares the corrupt
// segment and then cuts it. A spawn in between could start sharing it, so
// the release waits for every spawn in progress.
func TestAQuarantineReleaseWaitsForASpawnInProgress(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))
	end, err := f.beginSpawn()
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		_, err := f.ReleaseQuarantine(trunk, "", 0, t.TempDir(), true)
		released <- err
	}()
	select {
	case err := <-released:
		end()
		t.Fatalf("the release ran beside a spawn in progress (err %v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	end()
	select {
	case err := <-released:
		if !errors.Is(err, ErrNotQuarantined) {
			t.Fatalf("release = %v, want ErrNotQuarantined", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the release never ran after the spawn ended")
	}
}
