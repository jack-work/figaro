package xwal

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func orphanDirs(t *testing.T, dir, key string) []string {
	t.Helper()
	var out []string
	for _, ch := range []string{"main"} {
		d := filepath.Join(dir, ch, fsName(key))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, ".fork"), []byte("base=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "00000000000000000001.jsonl"), []byte(`{"guest":"text"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func sweepStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir, StoreOptions{Main: "main", IdleUnload: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	live, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(live, "main", 0, []byte(`"keep"`), nil); err != nil {
		t.Fatal(err)
	}
	s.syncDirty()
	return s, dir, live
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

// Dry first: the sweep names what it would remove and removes nothing until
// it is turned on; on, it removes exactly those, names each path, and never
// a file's contents.
func TestTheNodeSweepIsDryUntilTurnedOn(t *testing.T) {
	s, dir, live := sweepStore(t)
	want := mainRecords(t, s, live)
	orphans := orphanDirs(t, dir, "n77")
	logs := captureLogs(t)

	r, err := s.SweepUnmarkedNodes(false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Unmarked, orphans) || r.Removed != 0 {
		t.Fatalf("dry run = %+v, want %v listed and nothing removed", r, orphans)
	}
	for _, d := range orphans {
		if !pathExists(d) {
			t.Fatalf("the dry run removed %s", d)
		}
		if !strings.Contains(logs.String(), d) {
			t.Fatalf("the dry run did not name %s", d)
		}
	}

	r, err = s.SweepUnmarkedNodes(true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Removed != len(orphans) {
		t.Fatalf("sweep = %+v, want %d removed", r, len(orphans))
	}
	for _, d := range orphans {
		if pathExists(d) {
			t.Fatalf("%s survived the sweep", d)
		}
	}
	if strings.Contains(logs.String(), "guest") {
		t.Fatal("the sweep logged a file's contents")
	}
	if got := mainRecords(t, s, live); !slices.Equal(got, want) {
		t.Fatalf("the live aria reads %v after the sweep, want %v", got, want)
	}
}

// Only "does not exist" means no marker. Here the marker's path runs through
// a file (ENOTDIR): an error that is not absence keeps the directory.
func TestAMarkerThatCannotBeReadKeepsItsDirectory(t *testing.T) {
	s, dir, _ := sweepStore(t)
	if err := s.Trunks.ensureChannel(ChannelSpec{Name: "aux", Kind: ChannelLog}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main", "n78"), []byte("not a dir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(dir, "aux", "n78")
	if err := os.MkdirAll(node, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(node, ".fork"), []byte("base=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := s.SweepUnmarkedNodes(true)
	if err != nil {
		t.Fatal(err)
	}
	if !pathExists(node) || r.Removed != 0 {
		t.Fatalf("a directory whose marker could not be read was removed: %+v", r)
	}
}

// A plan the sweep cannot read may name the very nodes it would remove: it
// keeps everything this time.
func TestAnUnreadablePlanKeepsEverything(t *testing.T) {
	s, dir, _ := sweepStore(t)
	orphans := orphanDirs(t, dir, "n79")
	gdir := filepath.Join(dir, graftPrefix+"0123456789abcdef")
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gdir, graftPlanName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := s.SweepUnmarkedNodes(true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped == "" || r.Removed != 0 {
		t.Fatalf("sweep beside an unparsable plan = %+v, want skipped", r)
	}
	for _, d := range orphans {
		if !pathExists(d) {
			t.Fatalf("%s was removed beside an unparsable plan", d)
		}
	}
}

// A node a move or removal plan names belongs to that plan.
func TestTheSweepLeavesANodeAPlanNames(t *testing.T) {
	s, dir, _ := sweepStore(t)
	staged := orphanDirs(t, dir, "n88")
	body, _ := json.Marshal(graftPlan{Component: Component{Nodes: []ComponentNode{{Key: "n88", Kind: "conversation", Trunk: "t9"}}}})
	gdir := filepath.Join(dir, graftPrefix+"0123456789abcdef")
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gdir, graftPlanName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepUnmarkedNodes(true); err != nil {
		t.Fatal(err)
	}
	for _, d := range staged {
		if !pathExists(d) {
			t.Fatalf("%s, named by a pending plan, was swept", d)
		}
	}
}

// Nested channels live inside another channel's directory and are not nodes.
func TestTheSweepLeavesANestedChannel(t *testing.T) {
	s, dir, live := sweepStore(t)
	if err := s.Trunks.ensureChannel(ChannelSpec{Name: "main/provider", Kind: ChannelLog, Opaque: true}); err != nil {
		t.Fatal(err)
	}
	_ = live
	nested := filepath.Join(dir, "main", "provider")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".fork"), []byte("base=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepUnmarkedNodes(true); err != nil {
		t.Fatal(err)
	}
	if !pathExists(nested) {
		t.Fatal("a nested channel was swept as a node")
	}
}

// A fork stopped between its .fork writes and its marker is a live fork to
// whoever holds the store. A second opener cannot take the writer lock, and
// no opener but the store holder sweeps, so the fork's directories survive.
func TestAForkInFlightSurvivesASecondOpener(t *testing.T) {
	s, dir, _ := sweepStore(t)
	inFlight := orphanDirs(t, dir, "n99")
	if second, err := OpenStore(dir, StoreOptions{Main: "main"}); err == nil {
		second.Close()
		t.Fatal("a second opener took the writer lock of a live store")
	}
	peer, err := openTrunks(dir, s.Trunks.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	r, err := s.SweepUnmarkedNodes(true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped == "" {
		t.Fatalf("the sweep ran with a second opener in the process: %+v", r)
	}
	for _, d := range inFlight {
		if !pathExists(d) {
			t.Fatalf("a fork in flight lost %s", d)
		}
	}
}

func mainRecords(t *testing.T, s *Store, trunk string) []string {
	t.Helper()
	rs, err := s.RecordsFrom(trunk, "main", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, string(r.Payload))
	}
	return out
}
