package xwal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkload212c263aEnsureChannelLaterTopology(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	if err := f.CreateStump("before"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SpawnUnderStump("before"); err != nil {
		t.Fatal(err)
	}

	for _, spec := range []ChannelSpec{
		{Name: "turn-wal", Kind: ChannelLog, Opaque: true},
		{Name: "translations/anthropic", Kind: ChannelLog, Opaque: true},
		{Name: "turn-state", Kind: ChannelReducible, Reducer: "jsonmerge"},
	} {
		if err := f.ensureChannel(spec); err != nil {
			t.Fatalf("EnsureChannel(%q): %v", spec.Name, err)
		}
	}

	if err := f.CreateStump("after"); err != nil {
		t.Fatal(err)
	}
	trunk, err := f.SpawnUnderStump("after")
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"turn-wal", "translations/anthropic"} {
		if _, err := f.AppendChannel(trunk, channel, 1, []byte(`{"checkpoint":1}`), nil); err != nil {
			t.Fatalf("AppendChannel(%q) on later trunk: %v", channel, err)
		}
	}
	if _, err := f.AppendChannel(trunk, "turn-state", 1, []byte(`{"set":{"ready":true}}`), nil); err != nil {
		t.Fatalf("AppendChannel(turn-state) on later trunk: %v", err)
	}
}

func TestWorkload212c263aEnsureChannelRuntimePolicy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	spec := ChannelSpec{Name: "turn-wal", Kind: ChannelLog, Opaque: true}
	if err := f.ensureChannel(spec); err != nil {
		t.Fatal(err)
	}
	if err := f.ensureChannel(spec); err != nil {
		t.Fatalf("idempotent EnsureChannel: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	var man manifest
	if err := json.Unmarshal(data, &man); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ch := range man.Channels {
		if ch.Name == spec.Name {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("manifest has %d %q entries, want 1", count, spec.Name)
	}
	if strings.Contains(string(data), "sync") {
		t.Fatalf("runtime sync policy persisted in xwal.json: %s", data)
	}

	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if !head.chans[spec.Name].opaque {
		head.Close()
		t.Fatal("turn-wal is not opaque")
	}
	head.Close()

	if _, err := f.AppendChannel(trunk, spec.Name, 1, []byte(`{"checkpoint":"visible"}`), nil); err != nil {
		t.Fatal(err)
	}
	record, ok, err := f.LatestChannelRecord(trunk, spec.Name, 1)
	if err != nil || !ok || string(record.Payload) != `{"checkpoint":"visible"}` {
		t.Fatalf("in-process latest = %+v ok=%v err=%v", record, ok, err)
	}

	reopenCfg := withChannelSpec(trunksCfg(), spec)
	normal, err := Open(dir, reopenCfg)
	if err != nil {
		t.Fatal(err)
	}
	normal.Close()

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openTrunks(dir, reopenCfg)
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, reopened)
	head, err = reopened.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	head.Close()

	addDir := filepath.Join(t.TempDir(), "add")
	x, _ := triune(t, addDir)
	defer x.Close()
	added := ChannelSpec{Name: "manual-added", Kind: ChannelLog, Opaque: true}
	if err := x.addChannel(added); err != nil {
		t.Fatal(err)
	}
	if !x.chans[added.Name].opaque {
		t.Fatal("AddChannel did not apply opaque encoding")
	}
}

func TestWorkload212c263aLatestDuplicateAcrossFork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	const channel = "turn-wal"
	if err := f.ensureChannel(ChannelSpec{Name: channel, Kind: ChannelLog, Opaque: true}); err != nil {
		t.Fatal(err)
	}
	_, mainLT, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range []string{"first", "second"} {
		payload := []byte(fmt.Sprintf(`{"checkpoint":%q}`, checkpoint))
		if _, err := f.AppendChannel(trunk, channel, mainLT, payload, nil); err != nil {
			t.Fatal(err)
		}
	}

	record, ok, err := f.LatestChannelRecord(trunk, channel, mainLT)
	if err != nil || !ok || string(record.Payload) != `{"checkpoint":"second"}` || record.ChannelLT != 2 {
		t.Fatalf("latest duplicate = %+v ok=%v err=%v", record, ok, err)
	}
	if _, ok, err := f.LatestChannelRecord(trunk, channel, mainLT+1); err != nil || ok {
		t.Fatalf("minimum above tail: ok=%v err=%v", ok, err)
	}

	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err = f.LatestChannelRecord(alt, channel, mainLT)
	if err != nil || !ok || string(record.Payload) != `{"checkpoint":"second"}` {
		t.Fatalf("inherited latest = %+v ok=%v err=%v", record, ok, err)
	}
	if _, err := f.AppendChannel(alt, channel, mainLT, []byte(`{"checkpoint":"third"}`), nil); err != nil {
		t.Fatal(err)
	}
	record, ok, err = f.LatestChannelRecord(alt, channel, mainLT)
	if err != nil || !ok || string(record.Payload) != `{"checkpoint":"third"}` || record.ChannelLT != 3 {
		t.Fatalf("fork latest = %+v ok=%v err=%v", record, ok, err)
	}
	record, ok, err = f.LatestChannelRecord(trunk, channel, mainLT)
	if err != nil || !ok || string(record.Payload) != `{"checkpoint":"second"}` {
		t.Fatalf("original latest changed = %+v ok=%v err=%v", record, ok, err)
	}
}

func TestWorkload212c263aConcurrentLineagesAndTopology(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}

	cleanupTrunks(t, f)
	if err := f.CreateStump("active"); err != nil {
		t.Fatal(err)
	}
	left, err := f.SpawnUnderStump("active")
	if err != nil {
		t.Fatal(err)
	}
	right, err := f.SpawnUnderStump("active")
	if err != nil {
		t.Fatal(err)
	}
	const channel = "turn-wal"
	if err := f.ensureChannel(ChannelSpec{Name: channel, Kind: ChannelLog, Opaque: true}); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 3)
	var wg sync.WaitGroup
	appendAndSync := func(trunk string) {
		defer wg.Done()
		for i := uint64(1); i <= 48; i++ {
			if _, err := f.AppendChannel(trunk, channel, i, []byte(fmt.Sprintf(`{"checkpoint":%d}`, i)), nil); err != nil {
				errs <- err
				return
			}
		}

	}
	wg.Add(3)
	go appendAndSync(left)
	go appendAndSync(right)
	go func() {
		defer wg.Done()
		retryBusy := func(fn func() error) error {
			for {
				err := fn()
				if !isTopologyTimeout(err) {
					return err
				}
				runtime.Gosched()
			}
		}
		for i := 0; i < 8; i++ {
			name := fmt.Sprintf("created-%d", i)
			if err := retryBusy(func() error { return f.CreateStump(name) }); err != nil {
				errs <- err
				return
			}
			if err := retryBusy(func() error {
				_, err := f.SpawnUnderStump(name)
				return err
			}); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, trunk := range []string{left, right} {
		record, ok, err := f.LatestChannelRecord(trunk, channel, 48)
		if err != nil || !ok || record.MainLT != 48 {
			t.Fatalf("trunk %s latest = %+v ok=%v err=%v", trunk, record, ok, err)
		}
	}
}

func TestRootBorrowWaitsForActiveMutation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	endMutation, _, err := beginRootTopologyMutation(dir)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := f.Append(trunk, 0, []byte(`"waited"`), nil)
		done <- err
	}()
	select {
	case err := <-done:
		endMutation()
		t.Fatalf("append returned during mutation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	endMutation()
	if err := <-done; err != nil {
		t.Fatalf("append after mutation: %v", err)
	}
}

func TestConcurrentAppendsWithTopologyMutations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Trunks) func() error
	}{
		{
			name: "fork",
			setup: func(t *testing.T, f *Trunks) func() error {
				right, err := f.SpawnUnderStump("s")
				if err != nil {
					t.Fatal(err)
				}
				return func() error {
					for i := 0; i < 8; i++ {
						if _, _, err := f.Append(right, 0, []byte(`"right"`), nil); err != nil {
							return err
						}
						if _, err := f.ForkTail(right); err != nil {
							return err
						}
					}

					return nil
				}
			},
		},
		{
			name: "ensure-channel",
			setup: func(_ *testing.T, f *Trunks) func() error {
				return func() error {
					for i := 0; i < 8; i++ {
						err := f.ensureChannel(ChannelSpec{
							Name: fmt.Sprintf("turn-wal/%d", i), Kind: ChannelLog,
							Opaque: true,
						})
						if err != nil {
							return err
						}
					}
					return nil
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "f")
			f, trunk := seedTrunk(t, dir)
			topology := tc.setup(t, f)
			start := make(chan struct{})
			errs := make(chan error, 2)
			go func() {
				<-start
				for i := 0; i < 128; i++ {
					if _, _, err := f.Append(trunk, 0, []byte(`"append"`), nil); err != nil {
						errs <- err
						return
					}
				}
				errs <- nil
			}()
			go func() {
				<-start
				for {
					err := topology()
					if !isTopologyTimeout(err) {
						errs <- err
						return
					}
					runtime.Gosched()
				}
			}()
			close(start)
			for i := 0; i < 2; i++ {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
