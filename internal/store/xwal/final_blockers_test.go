package xwal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWatermarkStateComparisonIsCanonical(t *testing.T) {
	dir := t.TempDir()
	codec, err := codecByName("jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := seedWatermark(dir, 1, codec, []byte(`{"b":2,"a":1}`)); err != nil {
		t.Fatal(err)
	}
	valid, err := validateWatermark(dir, 1, codec, []byte(`{"a":1,"b":2}`))
	if err != nil || !valid {
		t.Fatalf("canonical watermark valid=%v err=%v", valid, err)
	}
}

func TestEnsureChannelValidatesBeforePendingPlan(t *testing.T) {
	for _, spec := range []ChannelSpec{
		{Name: "../escape", Kind: ChannelLog},
		{Name: "bad-kind", Kind: Kind(99)},
		{Name: "missing-reducer", Kind: ChannelReducible, Reducer: "absent"},
		{Name: "log-reducer", Kind: ChannelLog, Reducer: "jsonmerge"},
	} {
		t.Run(spec.Name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "f")
			f, _ := seedTrunk(t, dir)
			if err := f.ensureChannel(spec); err == nil {
				t.Fatal("EnsureChannel accepted invalid spec")
			}
			if pathExists(filepath.Join(dir, channelPendingName)) {
				t.Fatal("invalid spec persisted a pending plan")
			}
		})
	}
}

func TestReservedChannelComponentsRejectedAtCreateAndEnsure(t *testing.T) {
	names := []string{
		".xwal-channel-pending",
		"xwal.json",
		"translations/.fork",
		"translations/cache.tmp",
		"translations/cache.invalid",
		"translations/cache.replace-pending",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "create")
			cfg := trunksCfg()
			cfg.Channels = append(cfg.Channels, ChannelSpec{Name: name, Kind: ChannelLog})
			if _, err := Open(dir, cfg); err == nil {
				t.Fatal("creation accepted reserved channel path")
			}
			if pathExists(filepath.Join(dir, manifestName)) {
				t.Fatal("invalid creation published a manifest")
			}

			storeDir := filepath.Join(t.TempDir(), "ensure")
			f, _ := seedTrunk(t, storeDir)
			if err := f.ensureChannel(ChannelSpec{Name: name, Kind: ChannelLog}); err == nil {
				t.Fatal("EnsureChannel accepted reserved channel path")
			}
			if pathExists(filepath.Join(storeDir, channelPendingName)) {
				t.Fatal("invalid EnsureChannel published a pending plan")
			}
		})
	}
}

func TestDetachedHeadRetainsRootBorrowUntilClose(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if err := head.ensurePrivate(); err != nil {
		head.Close()
		t.Fatal(err)
	}
	shortTopologyWait(t)
	// A creator never waits on a detached head. A destructive op still does:
	// the borrow is what keeps its directories alive.
	if _, err := f.ForkTail(trunk); err != nil {
		t.Fatalf("ForkTail must not wait on a detached head: %v", err)
	}
	if err := f.Remove(trunk, true); !isTopologyTimeout(err) {
		head.Close()
		t.Fatalf("Remove error = %v, want bounded-wait timeout", err)
	}
	if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(trunk, true); err != nil {
		t.Fatalf("Remove after detached head close: %v", err)
	}
}

func TestAddChannelRetiresPostPublicationGeneration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if err := head.Close(); err != nil {
		t.Fatal(err)
	}
	x, err := Open(dir, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	spec := ChannelSpec{Name: "translations/barrier", Kind: ChannelLog, Opaque: true}
	if err := x.addChannel(spec); err != nil {
		t.Fatal(err)
	}
	f.hotMu.Lock()
	hot := f.hot
	f.hotMu.Unlock()
	if hot != nil {
		t.Fatal("AddChannel left a pre-publication hot generation active")
	}
	head, err = f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Close()
	if head.chans[spec.Name] == nil {
		t.Fatal("fresh hot generation did not observe added channel")
	}
}

func TestRepeatedEmptyReducibleForksKeepInheritedState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	_, mainLT, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AppendChannel(
		trunk, "chalkboard", mainLT, []byte(`{"set":{"shared":"value"}}`), nil,
	); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := f.ForkTail(trunk); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.Append(trunk, 0, []byte(`"next"`), nil); err != nil {
			t.Fatal(err)
		}
	}
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	state, err := head.StateAt("chalkboard", channelTail(head, "chalkboard"))
	head.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !statesEqual(state, []byte(`{"shared":"value"}`)) {
		t.Fatalf("state = %s", state)
	}
}

func TestForkPreflightRepairsMissingRehomeMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "f")
	f, trunk := seedTrunk(t, dir)
	spec := ChannelSpec{Name: "turn-wal", Kind: ChannelLog, Opaque: true}
	if err := f.ensureChannel(spec); err != nil {
		t.Fatal(err)
	}
	_, atMainLT, err := f.Append(trunk, 0, []byte(`"one"`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Append(trunk, 0, []byte(`"two"`), nil); err != nil {
		t.Fatal(err)
	}
	alt, err := f.ForkTail(trunk)
	if err != nil {
		t.Fatal(err)
	}
	altBranch := f.head(alt)
	f.retireRootHot()
	descendantDir := filepath.Join(dir, spec.Name, altBranch)
	if err := os.Remove(filepath.Join(descendantDir, ".fork")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ForkAt(trunk, atMainLT); err != nil {
		t.Fatalf("ForkAt after rehome marker repair: %v", err)
	}
	if pathExists(filepath.Join(dir, channelPendingName)) {
		t.Fatal("rehome marker repair left a pending channel plan")
	}
}
