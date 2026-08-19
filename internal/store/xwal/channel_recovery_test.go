package xwal

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenTrunksCompletesPendingChannelBeforeManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	spec := ChannelSpec{Name: "turn-wal", Kind: ChannelLog, Opaque: true}
	plan := channelPendingPlan{Channel: manifestChannel{
		Name: spec.Name, Kind: spec.Kind.String(), Opaque: spec.Opaque,
	}}
	if err := writeChannelPending(dir, plan); err != nil {
		t.Fatal(err)
	}

	reopened, err := openTrunks(dir, withChannelSpec(trunksCfg(), spec))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, reopened)
	if pathExists(filepath.Join(dir, channelPendingName)) {
		t.Fatal("pending channel plan survived recovery")
	}
	if _, err := reopened.AppendChannel(trunk, spec.Name, 1, opaquePayloadOne, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOpenTrunksCompletesPartialReducibleBackfill(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	branch := f.head(trunk)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	spec := ChannelSpec{Name: "turn-state", Kind: ChannelReducible, Reducer: "jsonmerge"}
	plan := channelPendingPlan{Channel: manifestChannel{
		Name: spec.Name, Kind: spec.Kind.String(), Reducer: spec.Reducer,
	}}
	if err := writeChannelPending(dir, plan); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, spec.Name, branch)
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, ".fork"), []byte("base="), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := withChannelSpec(trunksCfg(), spec)
	reopened, err := openTrunks(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, reopened)
	if _, err := reopened.AppendChannel(
		trunk, spec.Name, 1, []byte(`{"set":{"recovered":true}}`), nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ForkTail(trunk); err != nil {
		t.Fatalf("fork after recovered partial backfill: %v", err)
	}
}

func TestOpenTrunksRepairsLegacyManifestAuthoritativeChannel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	spec := ChannelSpec{Name: "translations/recovered", Kind: ChannelLog, Opaque: true}
	plan := channelPendingPlan{Channel: manifestChannel{
		Name: spec.Name, Kind: spec.Kind.String(), Opaque: spec.Opaque,
	}}
	man, err := loadOrCreateManifest(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	man.Channels = append(man.Channels, plan.Channel)
	if err := writeManifest(dir, man); err != nil {
		t.Fatal(err)
	}

	reopened, err := openTrunks(dir, withChannelSpec(trunksCfg(), spec))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, reopened)
	if _, err := reopened.AppendChannel(trunk, spec.Name, 1, opaquePayloadOne, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementRecoveryPhases(t *testing.T) {
	t.Run("temp-ready-before-replace", func(t *testing.T) {
		dir := t.TempDir()
		final := filepath.Join(dir, ".fork")
		if err := writeSyncedFile(final, []byte("base=2\n")); err != nil {
			t.Fatal(err)
		}
		if err := writeSyncedFile(final+".tmp", []byte("base=3\n")); err != nil {
			t.Fatal(err)
		}
		base, err := readForkBaseFile(final)
		if err != nil {
			t.Fatal(err)
		}
		if base != 2 {
			t.Fatalf("base = %d, want durable old base 2", base)
		}
	})

	t.Run("backup-only-after-first-rename", func(t *testing.T) {
		dir := t.TempDir()
		final := filepath.Join(dir, ".fork")
		if err := writeSyncedFile(final, []byte("base=4\n")); err != nil {
			t.Fatal(err)
		}
		if err := writeSyncedFile(final+".replace-pending", []byte("replace\n")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(final, final+".invalid"); err != nil {
			t.Fatal(err)
		}
		base, err := readForkBaseFile(final)
		if err != nil {
			t.Fatal(err)
		}
		if base != 4 {
			t.Fatalf("base = %d, want restored base 4", base)
		}
		for _, suffix := range []string{".invalid", ".replace-pending"} {
			if pathExists(final + suffix) {
				t.Fatalf("recovery left %s", suffix)
			}
		}
	})
}

func TestRootTopologyMutationRejectsPeerBorrowedHead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	first, trunk := seedTrunk(t, dir)
	peer, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}

	cleanupTrunks(t, peer)

	shortTopologyWait(t)
	head, err := peer.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	spec := ChannelSpec{Name: "turn-wal", Kind: ChannelLog, Opaque: true}
	if err := first.ensureChannel(spec); !isTopologyTimeout(err) {
		head.Close()
		t.Fatalf("EnsureChannel error = %v, want bounded-wait timeout", err)
	}
	// A creator does not wait on a peer's borrowed head; only channel and
	// destructive mutations, which rewrite what that head is reading, do.
	if err := first.CreateStump("unblocked"); err != nil {
		head.Close()
		t.Fatalf("CreateStump must not wait on a peer head: %v", err)
	}
	if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.ensureChannel(spec); err != nil {
		t.Fatalf("EnsureChannel after peer release: %v", err)
	}
}

func TestRootTopologyMutationRefreshesPeerTopology(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	first, _ := seedTrunk(t, dir)
	peer, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, peer)

	if err := first.CreateStump("peer-created"); err != nil {
		t.Fatal(err)
	}
	trunk, err := peer.SpawnUnderStump("peer-created")
	if err != nil {
		t.Fatalf("peer did not refresh topology: %v", err)
	}
	if _, _, err := peer.Append(trunk, 0, []byte(`"peer"`), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPeerTailAppendRefreshesStaleHead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	first, trunk := seedTrunk(t, dir)
	peer, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, peer)

	if head, err := peer.Head(trunk); err != nil {
		t.Fatal(err)
	} else if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.Append(trunk, 0, []byte(`"first"`), nil); err != nil {
		t.Fatal(err)
	}
	if _, lt, err := peer.Append(trunk, 0, []byte(`"peer"`), nil); err != nil {
		t.Fatal(err)
	} else if lt != 3 {
		t.Fatalf("peer append LT = %d, want 3", lt)
	}

	head, err := first.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	for lt, want := range map[uint64]string{2: `"first"`, 3: `"peer"`} {
		_, got, err := head.Read("ir", lt)
		if err != nil {
			t.Fatalf("read %d: %v", lt, err)
		}
		if string(got) != want {
			t.Fatalf("read %d = %s, want %s", lt, got, want)
		}
	}
}

func TestConcurrentPeerAppendsShareLineageWriter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	first, trunk := seedTrunk(t, dir)
	peer, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, peer)

	const writes = 40
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, trunks := range []*Trunks{first, peer} {
		wg.Add(1)
		go func(trunks *Trunks) {
			defer wg.Done()
			<-start
			for i := 0; i < writes; i++ {
				if _, _, err := trunks.Append(trunk, 0, []byte(`"entry"`), nil); err != nil {
					errs <- err
					return
				}
			}
		}(trunks)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	head, err := first.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	if got, want := mainTail(head), uint64(1+2*writes); got != want {
		t.Fatalf("tail = %d, want %d", got, want)
	}
	for lt := uint64(1); lt <= mainTail(head); lt++ {
		if _, _, err := head.Read("ir", lt); err != nil {
			t.Fatalf("read %d: %v", lt, err)
		}
	}
}

func TestPeerAppendRefreshesTopologyAfterFork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	first, trunk := seedTrunk(t, dir)
	peer, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, peer)

	if _, _, err := first.Append(trunk, 0, []byte(`"before"`), nil); err != nil {
		t.Fatal(err)
	}
	if head, err := peer.Head(trunk); err != nil {
		t.Fatal(err)
	} else if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ForkTail(trunk); err != nil {
		t.Fatal(err)
	}
	if _, lt, err := peer.Append(trunk, 0, []byte(`"after"`), nil); err != nil {
		t.Fatalf("peer append after fork: %v", err)
	} else if lt != 3 {
		t.Fatalf("peer append LT = %d, want 3", lt)
	}

	head, err := first.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	_, got, err := head.Read("ir", 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"after"` {
		t.Fatalf("continuation entry = %s", got)
	}
}

func TestPeerForkWaitsForLineageAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	first, trunk := seedTrunk(t, dir)
	peer, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, peer)

	appendEntered := make(chan struct{})
	releaseAppend := make(chan struct{})
	first.testAfterReadLock = func() {
		close(appendEntered)
		<-releaseAppend
	}
	appendDone := make(chan error, 1)
	go func() {
		_, _, err := first.Append(trunk, 0, []byte(`"append"`), nil)
		appendDone <- err
	}()
	<-appendEntered

	forkDone := make(chan error, 1)
	go func() {
		_, err := peer.ForkTail(trunk)
		forkDone <- err
	}()
	select {
	case err := <-forkDone:
		close(releaseAppend)
		t.Fatalf("fork returned before append released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseAppend)
	if err := <-appendDone; err != nil {
		t.Fatal(err)
	}
	if err := <-forkDone; err != nil {
		t.Fatal(err)
	}
	first.testAfterReadLock = nil

	head, err := first.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	if _, got, err := head.Read("ir", 2); err != nil || string(got) != `"append"` {
		t.Fatalf("continuation append = %s, err=%v", got, err)
	}
}
