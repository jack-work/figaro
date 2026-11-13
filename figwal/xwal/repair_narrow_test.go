package xwal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/figwal/segment"
)

// The narrow repair reads only the tails it can judge. These tests hold it
// to the old repair's result: the same "trimmed incoherent channel tail"
// actions and the same bytes on disk afterwards, on stores torn in the ways
// a crash tears them.

type repairLog struct {
	mu       sync.Mutex
	trimmed  []string
	unrepair []string
}

func (l *repairLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *repairLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *repairLog) WithGroup(string) slog.Handler            { return l }
func (l *repairLog) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != "err" {
			fmt.Fprintf(&b, "%s=%v ", a.Key, a.Value)
		}
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case r.Message == "xwal: trimmed incoherent channel tail":
		l.trimmed = append(l.trimmed, b.String())
	case strings.Contains(r.Message, "left unrepaired"):
		l.unrepair = append(l.unrepair, b.String())
	}
	return nil
}

func referenceRepair(t *Trunks) error {
	repairReadsTailOnly = false
	defer func() { repairReadsTailOnly = true }()
	man, err := loadOrCreateManifest(t.root, t.cfg)
	if err != nil {
		return err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return err
	}
	kids := t.idx.ChildIndex()
	for key := range t.idx.All() {
		if len(kids[key]) > 0 {
			continue
		}
		mainTail, err := nodeMainTail(t.irDir(key), codec)
		if errors.Is(err, segment.ErrCorrupt) {
			quarantine(key, err)
			continue
		}
		if err != nil {
			return fmt.Errorf("xwal: repair leaf %q: %w", key, err)
		}
		for _, mc := range man.Channels {
			if mc.Name == man.Main {
				continue
			}
			dir := filepath.Join(t.root, mc.Name, fsName(key))
			if !pathExists(dir) {
				continue
			}
			trimmed, err := trimChannelNode(dir, codec, mc.Kind == ChannelReducible.String(), mainTail)
			if errors.Is(err, segment.ErrCorrupt) {
				quarantine(key, err)
				break
			}
			if err != nil {
				return fmt.Errorf("xwal: repair channel %q leaf %q: %w", mc.Name, key, err)
			}
			if trimmed > 0 {
				slog.Warn("xwal: trimmed incoherent channel tail",
					"channel", mc.Name, "leaf", key, "records", trimmed, "mainTail", mainTail)
			}
		}
	}
	return nil
}

func runRepair(t *testing.T, dir string, cfg Config, repair func(*Trunks) error) *repairLog {
	t.Helper()
	log := &repairLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(log))
	defer slog.SetDefault(prev)
	tr, err := openTrunks(dir, cfg)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	rerr := repair(tr)
	if err := tr.Close(); err != nil {
		t.Fatalf("close %s: %v", dir, err)
	}
	if rerr != nil {
		t.Fatalf("repair %s: %v", dir, rerr)
	}
	sort.Strings(log.trimmed)
	sort.Strings(log.unrepair)
	return log
}

func storeDigest(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel+"/"] = "dir"
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			out[rel] = "link:" + target
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		out[rel] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func copyStore(t *testing.T, src, dst string) {
	t.Helper()
	if out, err := exec.Command("cp", "-a", "--reflink=auto", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v: %s", err, out)
	}
}

func assertSameRepair(t *testing.T, src string, cfg Config) *repairLog {
	t.Helper()
	work := filepath.Dir(src)
	oldDir, newDir := filepath.Join(work, "old"), filepath.Join(work, "new")
	copyStore(t, src, oldDir)
	copyStore(t, src, newDir)
	oldLog := runRepair(t, oldDir, cfg, referenceRepair)
	newLog := runRepair(t, newDir, cfg, repairCoherentCuts)
	if strings.Join(oldLog.trimmed, "\n") != strings.Join(newLog.trimmed, "\n") {
		t.Fatalf("trim actions differ\nold:\n%s\nnew:\n%s", strings.Join(oldLog.trimmed, "\n"), strings.Join(newLog.trimmed, "\n"))
	}
	oldSum, newSum := storeDigest(t, oldDir), storeDigest(t, newDir)
	var diff []string
	for p, h := range oldSum {
		if newSum[p] != h {
			diff = append(diff, p)
		}
	}
	for p := range newSum {
		if _, ok := oldSum[p]; !ok {
			diff = append(diff, p)
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		t.Fatalf("stores differ after repair in %d files: %v", len(diff), diff)
	}
	return newLog
}

func narrowTestOptions() StoreOptions {
	opts := testStoreOptions()
	opts.Opaque = []string{"translations"}
	opts.Unkeyed = []string{"notes"}
	opts.SegmentSize = 1200
	return opts
}

func headNode(t *testing.T, s *Store, trunk string) string {
	t.Helper()
	for _, ti := range s.ListLight() {
		if ti.ID == trunk {
			return ti.Head[0]
		}
	}
	t.Fatalf("no head for %s", trunk)
	return ""
}

func newestSegment(t *testing.T, dir string) (string, []uint64) {
	t.Helper()
	bases, err := segmentBases(dir, segment.JSONLCodec{})
	if err != nil || len(bases) == 0 {
		t.Fatalf("segments in %s: %v %v", dir, bases, err)
	}
	return filepath.Join(dir, segFileName(bases[len(bases)-1], segment.JSONLCodec{})), bases
}

func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func flipInLastLines(t *testing.T, path string, fromEnd int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	if lines[len(lines)-1] == nil || len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	i := len(lines) - fromEnd
	off := 0
	for _, l := range lines[:i] {
		off += len(l)
	}
	raw[off+len(lines[i])/2] ^= 0x01
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildTornStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	const trunks = 9
	var ids []string
	for n := 0; n < trunks; n++ {
		tr, err := s.SpawnUnderRoot()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tr)
		for i := 1; i <= 12; i++ {
			lt, err := s.Append(tr, "ir", 0, []byte(fmt.Sprintf(`{"t":%d,"pad":"%s"}`, i, strings.Repeat("x", 40))), nil)
			if err != nil {
				t.Fatal(err)
			}
			if n != 5 {
				if _, err := s.Append(tr, "translations", lt, []byte(fmt.Sprintf(`wire-%d-%d`, n, i)), nil); err != nil {
					t.Fatal(err)
				}
			}
			if i%2 == 0 && n != 5 {
				patch, _ := MapSetPatch([]string{"turn"}, []byte(itoa(lt)))
				if _, err := s.Append(tr, "chalkboard", lt, patch, nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Append(tr, "notes", 0, []byte(fmt.Sprintf(`{"note":%d}`, i)), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.Fork(ids[0], 6); err != nil {
		t.Fatal(err)
	}
	heads := make([]string, trunks)
	for n, tr := range ids {
		heads[n] = headNode(t, s, tr)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	ir := func(n int) string { return filepath.Join(dir, "ir", fsName(heads[n])) }
	tx := func(n int) string { return filepath.Join(dir, "translations", fsName(heads[n])) }

	seg, _ := newestSegment(t, ir(1))
	truncateLastFrames(t, seg, 2)

	seg, _ = newestSegment(t, ir(2))
	truncateLastFrames(t, seg, 1)
	appendBytes(t, seg, []byte(`{"_hash":"0123456789abcdef","_idx":`))

	seg, _ = newestSegment(t, tx(3))
	appendBytes(t, seg, []byte("{\"_hash\":\"torn"))
	seg, _ = newestSegment(t, ir(3))
	truncateLastFrames(t, seg, 1)

	seg, bases := newestSegment(t, ir(4))
	if len(bases) < 2 {
		t.Fatalf("fixture: leaf 4 wants several IR segments, has %v", bases)
	}
	if err := os.Remove(seg); err != nil {
		t.Fatal(err)
	}

	seg, _ = newestSegment(t, ir(5))
	truncateLastFrames(t, seg, 3)

	seg, _ = newestSegment(t, tx(6))
	flipInLastLines(t, seg, 2)
	seg, _ = newestSegment(t, ir(6))
	truncateLastFrames(t, seg, 2)

	seg, _ = newestSegment(t, ir(7))
	truncateLastFrames(t, seg, 1)
	flipInLastLines(t, seg, 1)

	if err := os.WriteFile(uncleanPath(dir), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTheNarrowRepairTrimsWhatTheFullRepairTrims(t *testing.T) {
	dir := buildTornStore(t)
	log := assertSameRepair(t, dir, narrowTestOptions().config())
	if len(log.trimmed) < 4 {
		t.Fatalf("fixture tore too little: only %d trims\n%s", len(log.trimmed), strings.Join(log.trimmed, "\n"))
	}
	for _, l := range log.trimmed {
		t.Logf("trim %s", l)
	}
	if len(log.unrepair) != 1 {
		t.Fatalf("the corrupt frame under a valid tail frame was not quarantined: %v", log.unrepair)
	}
}

func TestTheNarrowRepairNeverReadsAnUnkeyedChannel(t *testing.T) {
	dir := buildTornStore(t)
	notes, _ := filepath.Glob(filepath.Join(dir, "notes", "*", "*.jsonl"))
	if len(notes) == 0 {
		t.Fatal("fixture: no notes segments")
	}
	for _, p := range notes {
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
	}
	runRepair(t, dir, narrowTestOptions().config(), repairCoherentCuts)
}

func TestTheNarrowRepairSkipsALeafWithNoKeyedRows(t *testing.T) {
	dir := buildTornStore(t)
	var bare []string
	for _, d := range mustGlob(t, filepath.Join(dir, "ir", "*")) {
		key := filepath.Base(d)
		if len(mustGlob(t, filepath.Join(dir, "translations", key, "*.jsonl"))) == 0 && len(mustGlob(t, filepath.Join(dir, "chalkboard", key, "*.jsonl"))) == 0 {
			bare = append(bare, d)
		}
	}
	if len(bare) < 2 {
		t.Fatalf("fixture: want the root and leaf 5 without keyed rows, got %v", bare)
	}
	for _, d := range bare {
		empty := filepath.Join(dir, "translations", filepath.Base(d))
		if err := os.MkdirAll(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(empty, segFileName(1, segment.JSONLCodec{})), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, p := range mustGlob(t, filepath.Join(d, "*.jsonl")) {
			if err := os.Chmod(p, 0o000); err != nil {
				t.Fatal(err)
			}
		}
	}
	runRepair(t, dir, narrowTestOptions().config(), repairCoherentCuts)
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	out, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadingFromTheEndFindsTheSameLastFrameAsTheScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "00000000000000000007.jsonl")
	codec := segment.JSONLCodec{}
	var raw []byte
	for i := 0; i < 40; i++ {
		frame, err := codec.Frame(uint64(7+i), []byte(fmt.Sprintf(`{"m":%d,"pad":"%s"}`, i, strings.Repeat("y", i*3000))))
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, frame...)
	}
	cases := map[string][]byte{
		"whole":      raw,
		"torn":       append(append([]byte(nil), raw...), []byte(`{"_hash":"ab`)...),
		"torn-line":  append(append([]byte(nil), raw...), []byte("{\"_hash\":\"zz\"}\n")...),
		"empty":      nil,
		"only-torn":  []byte(`{"_hash":"`),
		"blank-tail": append(append([]byte(nil), raw...), '\n'),
	}
	for name, b := range cases {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		spans, err := scanSegmentFrames(path, codec)
		if err != nil {
			t.Fatalf("%s: scan: %v", name, err)
		}
		idx, found, stamped, err := lastFrameIdx(path, codec)
		if err != nil {
			t.Fatalf("%s: from the end: %v", name, err)
		}
		if found != (len(spans) > 0) {
			t.Fatalf("%s: found=%v, scan has %d frames", name, found, len(spans))
		}
		if found && (!stamped || idx != 7+uint64(len(spans))-1) {
			t.Fatalf("%s: idx %d stamped %v, scan says %d", name, idx, stamped, 7+len(spans)-1)
		}
	}
}

// FIGWAL_REPAIR_EQ_STORE names a store root, which this test only reads; it
// tears a copy the way a crash would and holds the narrow repair to the full
// one. FIGWAL_REPAIR_EQ_WORK puts the copies on a filesystem that reflinks.
func TestTheNarrowRepairMatchesTheFullOneOnARealStore(t *testing.T) {
	src := os.Getenv("FIGWAL_REPAIR_EQ_STORE")
	if src == "" {
		t.Skip("FIGWAL_REPAIR_EQ_STORE not set")
	}
	work := t.TempDir()
	if dir := os.Getenv("FIGWAL_REPAIR_EQ_WORK"); dir != "" {
		var err error
		if work, err = os.MkdirTemp(dir, "eq-"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(work) })
	}
	torn := filepath.Join(work, "torn")
	copyStore(t, src, torn)
	man, err := readManifestFile(torn)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Main: man.Main, Codec: man.Codec, Registry: map[string]Reducer{}}
	for _, mc := range man.Channels {
		cfg.Channels = append(cfg.Channels, specOf(mc))
		if mc.Reducer != "" {
			cfg.Registry[mc.Reducer] = MapReducer()
		}
	}
	tears := tearRealStore(t, torn, man, cfg)
	t.Logf("tears: %s", strings.Join(tears, "; "))
	if err := os.WriteFile(uncleanPath(torn), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := assertSameRepair(t, torn, cfg)
	t.Logf("trims: %d, quarantined: %d", len(log.trimmed), len(log.unrepair))
	if len(log.trimmed) < len(tears) {
		t.Fatalf("%d tears made only %d trims", len(tears), len(log.trimmed))
	}
	for _, l := range log.trimmed {
		t.Logf("trim %s", l)
	}
}

func tearRealStore(t *testing.T, root string, man manifest, cfg Config) []string {
	t.Helper()
	codec := segment.JSONLCodec{}
	tr, err := openTrunks(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	kids := tr.idx.ChildIndex()
	leaves := map[string]bool{}
	for key := range tr.idx.All() {
		if len(kids[key]) == 0 {
			leaves[fsName(key)] = true
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	repairReadsTailOnly = false
	defer func() { repairReadsTailOnly = true }()

	type victim struct {
		node, channel string
		irSeg         string
		irBases       []uint64
		irFrames      int
		gap           int
	}
	var victims []victim
	for _, mc := range man.Channels {
		if mc.Name == man.Main || mc.Unkeyed || mc.Kind == ChannelReducible.String() {
			continue
		}
		for _, d := range mustGlob(t, filepath.Join(root, mc.Name, "*")) {
			node := filepath.Base(d)
			if !leaves[node] {
				continue
			}
			cb, err := segmentBases(d, codec)
			if err != nil || len(cb) == 0 {
				continue
			}
			chSeg := filepath.Join(d, segFileName(cb[len(cb)-1], codec))
			spans, err := scanSegmentFrames(chSeg, codec)
			if err != nil || len(spans) == 0 {
				continue
			}
			lastRow, err := readFrameMainLT(chSeg, codec, spans[len(spans)-1])
			if err != nil || lastRow == 0 {
				continue
			}
			irDir := filepath.Join(root, man.Main, node)
			tail, err := nodeMainTail(irDir, codec)
			if err != nil || tail < lastRow {
				continue
			}
			bases, err := segmentBases(irDir, codec)
			if err != nil || len(bases) == 0 {
				continue
			}
			irSeg := filepath.Join(irDir, segFileName(bases[len(bases)-1], codec))
			irSpans, err := scanSegmentFrames(irSeg, codec)
			if err != nil {
				continue
			}
			victims = append(victims, victim{node, mc.Name, irSeg, bases, len(irSpans), int(tail - lastRow)})
		}
	}
	sort.Slice(victims, func(i, j int) bool {
		if victims[i].node != victims[j].node {
			return victims[i].node < victims[j].node
		}
		return victims[i].channel < victims[j].channel
	})
	var tears []string
	done := map[string]bool{}
	want := []int{0, 1, 2, 3, 0, 1, 2, 3, 3, 0}
	step := 0
	for _, v := range victims {
		if step >= len(want) || done[v.node] {
			continue
		}
		var tear string
		switch want[step] {
		case 0:
			if v.gap+3 > v.irFrames {
				continue
			}
			truncateLastFrames(t, v.irSeg, v.gap+3)
			tear = fmt.Sprintf("%s: ir lost %d frames, past the last %s row", v.node, v.gap+3, v.channel)
		case 1:
			if v.gap+2 > v.irFrames {
				continue
			}
			truncateLastFrames(t, v.irSeg, v.gap+2)
			appendBytes(t, v.irSeg, []byte(`{"_hash":"0123`))
			tear = fmt.Sprintf("%s: ir lost %d frames and ends torn", v.node, v.gap+2)
		case 2:
			if len(v.irBases) < 2 || v.gap >= v.irFrames {
				continue
			}
			if err := os.Remove(v.irSeg); err != nil {
				t.Fatal(err)
			}
			tear = fmt.Sprintf("%s: ir lost its last segment (%d frames)", v.node, v.irFrames)
		case 3:
			if v.gap+1 > v.irFrames {
				continue
			}
			chDir := filepath.Join(root, v.channel, v.node)
			cb, _ := segmentBases(chDir, codec)
			appendBytes(t, filepath.Join(chDir, segFileName(cb[len(cb)-1], codec)), []byte(`{"_hash":"torn`))
			truncateLastFrames(t, v.irSeg, v.gap+1)
			tear = fmt.Sprintf("%s: %s ends torn, ir lost %d frames", v.node, v.channel, v.gap+1)
		}
		done[v.node] = true
		tears = append(tears, tear)
		step++
	}
	if step < len(want) {
		t.Fatalf("store too small to tear: %d of %d tears: %v", step, len(want), tears)
	}
	return tears
}
