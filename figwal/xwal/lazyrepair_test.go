package xwal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/figwal/segment"
)

// The crash repair runs per leaf, on the leaf's first borrow or in a
// background pass. These tests hold it to the eager repair it replaces: the
// same bytes on disk once every leaf is done, and no reader ever seeing a
// leaf's rows before its repair.

func holdRepairPass(t *testing.T) func() {
	t.Helper()
	ch := make(chan struct{})
	repairPassHold = ch
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(func() {
		release()
		repairPassHold = nil
	})
	return release
}

func waitRepaired(t *testing.T, s *Store) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for s.RepairsPending() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d leaves still pending repair", s.RepairsPending())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func eagerOracle(t *testing.T, src string, cfg Config) (string, *repairLog) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "eager")
	copyStore(t, src, dir)
	log := runRepair(t, dir, cfg, repairCoherentCuts)
	if err := os.Remove(uncleanPath(dir)); err != nil {
		t.Fatal(err)
	}
	return dir, log
}

func lazyCopy(t *testing.T, src string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lazy")
	copyStore(t, src, dir)
	return dir
}

func sameStoreFiles(t *testing.T, want, got string, skip ...string) {
	t.Helper()
	ignore := map[string]bool{uncleanName: true, lockName: true}
	for _, s := range skip {
		ignore[s] = true
	}
	a, b := storeDigest(t, want), storeDigest(t, got)
	var diff []string
	for p, h := range a {
		if !ignore[p] && b[p] != h {
			diff = append(diff, p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok && !ignore[p] {
			diff = append(diff, p)
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		t.Fatalf("stores differ in %d paths: %v", len(diff), diff)
	}
}

type lazyLog struct {
	*repairLog
	mu   sync.Mutex
	msgs []string
}

func (l *lazyLog) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	l.mu.Lock()
	l.msgs = append(l.msgs, b.String())
	l.mu.Unlock()
	return l.repairLog.Handle(ctx, r)
}

func (l *lazyLog) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *lazyLog) WithGroup(string) slog.Handler      { return l }

func (l *lazyLog) attrOf(message, key string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if !strings.HasPrefix(m, message) {
			continue
		}
		rest := m[len(message):]
		i := strings.Index(rest, " "+key+"=")
		if i < 0 {
			return ""
		}
		v := rest[i+len(key)+2:]
		for _, other := range []string{" leaves=", " cause=", " trimmed=", " ms="} {
			if j := strings.Index(v, other); j >= 0 {
				v = v[:j]
			}
		}
		return v
	}
	return ""
}

func (l *lazyLog) pendingAtStop() string {
	return l.attrOf("xwal: store closed with crash repairs pending", "leaves")
}

func (l *lazyLog) openCause() string {
	return l.attrOf("xwal: store was not closed clean", "cause")
}

func captureLog(t *testing.T) *lazyLog {
	t.Helper()
	log := &lazyLog{repairLog: &repairLog{}}
	prev := slog.Default()
	slog.SetDefault(slog.New(log))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return log
}

type rowsOf map[string]string

func storeRows(t *testing.T, s *Store, trunks []string) rowsOf {
	t.Helper()
	out := rowsOf{}
	for _, tr := range trunks {
		for _, ch := range []string{"ir", "translations", "chalkboard"} {
			recs, err := s.RecordsFrom(tr, ch, 0, 0)
			if err != nil {
				out[tr+"/"+ch] = "refused"
				continue
			}
			var b strings.Builder
			for _, r := range recs {
				fmt.Fprintf(&b, "%d:%d:%s;", r.MainLT, r.ChannelLT, r.Payload)
			}
			out[tr+"/"+ch] = b.String()
		}
	}
	return out
}

func conversationTrunks(s *Store) []string {
	var out []string
	for _, ti := range s.ListLight() {
		if ti.Kind == "conversation" {
			out = append(out, ti.ID)
		}
	}
	sort.Strings(out)
	return out
}

func sameRows(t *testing.T, want, got rowsOf) {
	t.Helper()
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s:\n want %s\n got  %s", k, w, got[k])
		}
	}
	if len(got) != len(want) {
		t.Errorf("read %d channels, want %d", len(got), len(want))
	}
}

func oracleRows(t *testing.T, dir string) rowsOf {
	t.Helper()
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.RepairsPending() != 0 {
		t.Fatal("the oracle copy opened suspect")
	}
	return storeRows(t, s, conversationTrunks(s))
}

func TestTheLazyRepairLeavesWhatTheEagerOneLeaves(t *testing.T) {
	src := buildTornStore(t)
	cfg := narrowTestOptions().config()
	want, eagerLog := eagerOracle(t, src, cfg)
	if len(eagerLog.trimmed) < 4 {
		t.Fatalf("fixture tore too little: %d trims", len(eagerLog.trimmed))
	}
	dir := lazyCopy(t, src)
	log := captureLog(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	waitRepaired(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if pathExists(uncleanPath(dir)) {
		t.Fatal("a clean close after every repair left the marker")
	}
	sort.Strings(log.trimmed)
	if strings.Join(log.trimmed, "\n") != strings.Join(eagerLog.trimmed, "\n") {
		t.Fatalf("trims differ\neager:\n%s\nlazy:\n%s", strings.Join(eagerLog.trimmed, "\n"), strings.Join(log.trimmed, "\n"))
	}
	if len(log.unrepair) != len(eagerLog.unrepair) {
		t.Fatalf("quarantined %d leaves, eager %d", len(log.unrepair), len(eagerLog.unrepair))
	}
	sameStoreFiles(t, want, dir)
}

func TestAFirstBorrowBeforeThePassSeesTheRepairedRows(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	wantRows := oracleRows(t, want)
	dir := lazyCopy(t, src)
	release := holdRepairPass(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	before := s.RepairsPending()
	if before == 0 {
		t.Fatal("a torn store opened with nothing pending")
	}
	sameRows(t, wantRows, storeRows(t, s, conversationTrunks(s)))
	if s.RepairsPending() >= before {
		t.Fatalf("borrowing every trunk repaired nothing: %d pending, %d before", s.RepairsPending(), before)
	}
	release()
	waitRepaired(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sameStoreFiles(t, want, dir)
}

func copyOf(t *testing.T, src string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "copy")
	copyStore(t, src, dir)
	return dir
}

func tornLeaves(t *testing.T, src string) []string {
	t.Helper()
	tr, err := openTrunks(src, narrowTestOptions().config())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	return suspectLeaves(tr.idx)
}

func trunkOfNode(t *testing.T, s *Store, key string) string {
	t.Helper()
	for _, ti := range s.ListLight() {
		if ti.Head[0] == key {
			return ti.ID
		}
	}
	t.Fatalf("no trunk heads %s", key)
	return ""
}

func childBases(t *testing.T, dir, key string) string {
	t.Helper()
	var out []string
	for _, ch := range []string{"translations", "chalkboard", "notes"} {
		base, err := readForkBaseStrict(filepath.Join(dir, ch, fsName(key)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s=%d", ch, base))
	}
	return strings.Join(out, " ")
}

func openHeld(t *testing.T, dir string) *Store {
	t.Helper()
	holdRepairPass(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAForkOfASuspectLeafTakesItsBasesFromTheRepairedRows(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	for _, at := range []uint64{0, 9} {
		ws := openHeld(t, copyOf(t, want))
		ls := openHeld(t, lazyCopy(t, src))
		for _, tr := range conversationTrunks(ls) {
			wkid, werr := ws.Fork(tr, at)
			lkid, lerr := ls.Fork(tr, at)
			if (werr == nil) != (lerr == nil) {
				t.Fatalf("fork %s at %d: eager %v, lazy %v", tr, at, werr, lerr)
			}
			if werr != nil {
				continue
			}
			w := childBases(t, ws.root, headNode(t, ws, wkid))
			l := childBases(t, ls.root, headNode(t, ls, lkid))
			if w != l {
				t.Errorf("fork %s at %d: eager bases %s, lazy %s", tr, at, w, l)
			}
		}
	}
}

func exportDigest(t *testing.T, e *Export) string {
	t.Helper()
	var lines []string
	for _, f := range e.Files {
		b, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %d %x", f.Node, f.Channel, f.Name, f.Size, b))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestAnExportOfASuspectComponentCarriesTheRepairedRows(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	ws := openHeld(t, copyOf(t, want))
	ls := openHeld(t, lazyCopy(t, src))
	for i, tr := range conversationTrunks(ls) {
		token := fmt.Sprintf("%08x", i+1)
		we, werr := ws.Export(tr, token)
		le, lerr := ls.Export(tr, token)
		if (werr == nil) != (lerr == nil) {
			t.Fatalf("export of %s: eager %v, lazy %v", tr, werr, lerr)
		}
		if werr != nil {
			continue
		}
		if w, l := exportDigest(t, we), exportDigest(t, le); w != l {
			t.Errorf("export of %s differs\neager:\n%s\nlazy:\n%s", tr, w, l)
		}
		we.Close()
		le.Close()
	}
}

func TestReparentingUnderASuspectLeafRepairsItFirst(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	dir := lazyCopy(t, src)
	s := openHeld(t, dir)
	for _, under := range tornLeaves(t, src) {
		if under == "" {
			continue
		}
		tr, err := s.SpawnUnderRoot()
		if err != nil {
			t.Fatal(err)
		}
		node := headNode(t, s, tr)
		if err := s.Reparent(node, under); err != nil {
			t.Fatalf("reparent under %s: %v", under, err)
		}
		for _, ch := range []string{"translations", "chalkboard"} {
			rel := filepath.Join(ch, fsName(under))
			if !pathExists(filepath.Join(want, rel)) {
				if pathExists(filepath.Join(dir, rel)) {
					t.Errorf("%s exists after a reparent, and not in the eager store", rel)
				}
				continue
			}
			w, l := storeDigest(t, filepath.Join(want, rel)), storeDigest(t, filepath.Join(dir, rel))
			if fmt.Sprint(w) != fmt.Sprint(l) {
				t.Errorf("%s gained a child before its repair: %s differs", under, rel)
			}
		}
	}
}

func quarantineReport(t *testing.T, s *Store) string {
	t.Helper()
	qs, err := s.ScanQuarantine()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, q := range qs {
		rel, _ := filepath.Rel(s.root, q.Segment)
		lines = append(lines, fmt.Sprintf("%s %s %s %d", q.Node, q.Channel, rel, q.Offset))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestTheQuarantineReportBeforeThePassIsTheEagerOne(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	w := quarantineReport(t, openHeld(t, copyOf(t, want)))
	ls := openHeld(t, lazyCopy(t, src))
	l := quarantineReport(t, ls)
	if n := ls.RepairsPending(); n != 0 {
		t.Errorf("the scan left %d leaves unrepaired; a release after it would cut before the repair", n)
	}
	if w == "" {
		t.Fatal("fixture: nothing quarantined")
	}
	if w != l {
		t.Fatalf("quarantine differs\neager:\n%s\nlazy:\n%s", w, l)
	}
}

func TestRemovingASuspectLeafNeverRacesItsRepair(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	leaves := tornLeaves(t, src)
	dir := lazyCopy(t, src)
	release := holdRepairPass(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	r := s.Trunks.repairs.Load()
	var queued, busy string
	for _, k := range leaves {
		if k == "" {
			continue
		}
		if queued == "" {
			queued = k
		} else if busy == "" {
			busy = k
		}
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	r.testRepair = func(key string) error {
		if key == busy {
			close(entered)
			<-proceed
		}
		return nil
	}
	if err := s.Remove(trunkOfNode(t, s, queued), false); err != nil {
		t.Fatal(err)
	}
	release()
	<-entered
	removed := make(chan error, 1)
	go func() { removed <- s.Remove(trunkOfNode(t, s, busy), false) }()
	select {
	case err := <-removed:
		t.Fatalf("a removal finished under a running repair: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	waitRepaired(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	gone := map[string]bool{}
	for _, k := range []string{queued, busy} {
		for _, ch := range []string{"ir", "translations", "chalkboard", "notes"} {
			gone[filepath.Join(ch, fsName(k))+"/"] = true
			if pathExists(filepath.Join(dir, ch, fsName(k))) {
				t.Errorf("%s of a removed leaf is still on disk", filepath.Join(ch, fsName(k)))
			}
		}
	}
	wantSum, gotSum := storeDigest(t, want), storeDigest(t, dir)
	for p, h := range wantSum {
		if p == uncleanName || p == lockName || underAny(p, gone) {
			continue
		}
		if gotSum[p] != h {
			t.Errorf("%s differs from the eager repair", p)
		}
	}
}

func underAny(p string, dirs map[string]bool) bool {
	for d := range dirs {
		if strings.HasPrefix(p+"/", d) {
			return true
		}
	}
	return false
}

func TestACrashMidPassLeavesAStoreTheNextOpenFinishes(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	total := len(tornLeaves(t, src))
	for _, k := range []int{0, 1, total / 2, total - 1} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			dir := lazyCopy(t, src)
			release := holdRepairPass(t)
			s, err := OpenStore(dir, narrowTestOptions())
			if err != nil {
				t.Fatal(err)
			}
			var done atomic.Int64
			s.Trunks.repairs.Load().testRepair = func(string) error {
				if done.Add(1) > int64(k) {
					return errors.New("crashed")
				}
				return nil
			}
			release()
			deadline := time.Now().Add(10 * time.Second)
			for done.Load() < int64(total) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if err := s.Abandon(); err != nil {
				t.Fatal(err)
			}
			if cause, ok := uncleanCause(dir); !ok || cause != causeCrash {
				t.Fatalf("after the crash the marker says %q (present %v)", cause, ok)
			}
			s2, err := OpenStore(dir, narrowTestOptions())
			if err != nil {
				t.Fatal(err)
			}
			waitRepaired(t, s2)
			if err := s2.Close(); err != nil {
				t.Fatal(err)
			}
			sameStoreFiles(t, want, dir)
		})
	}
}

func TestAStopWithRepairsPendingKeepsTheMarkerAndTheNextOpenServesThemRepaired(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	wantRows := oracleRows(t, want)
	dir := lazyCopy(t, src)

	holdRepairPass(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	trunks := conversationTrunks(s)
	if _, err := s.RecordsFrom(trunks[1], "translations", 0, 0); err != nil {
		t.Fatal(err)
	}
	left := s.RepairsPending()
	if left == 0 {
		t.Fatal("fixture: nothing left pending")
	}
	stopLog := captureLog(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := stopLog.pendingAtStop(); got != fmt.Sprint(left) {
		t.Fatalf("the stop logged %q pending, want %d", got, left)
	}
	body, err := os.ReadFile(uncleanPath(dir))
	if err != nil {
		t.Fatalf("a stop with repairs pending removed the marker: %v", err)
	}
	if string(body) != fmt.Sprintf("repairs-pending %d\n", left) {
		t.Fatalf("marker says %q", body)
	}
	if cause, _ := uncleanCause(dir); cause != causeLeftByStop {
		t.Fatalf("cause %q", cause)
	}

	release := holdRepairPass(t)
	openLog := captureLog(t)
	s2, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := openLog.openCause(); got != causeLeftByStop {
		t.Fatalf("the open logged cause %q", got)
	}
	if s2.RepairsPending() == 0 {
		t.Fatal("the next open has nothing pending")
	}
	sameRows(t, wantRows, storeRows(t, s2, conversationTrunks(s2)))
	release()
	waitRepaired(t, s2)
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if pathExists(uncleanPath(dir)) {
		t.Fatal("the marker survived a clean stop with every repair done")
	}
	sameStoreFiles(t, want, dir)
}

func TestAnyOtherMarkerContentIsACrash(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{"open\n", "", "repairs-pending\n", "garbage", "repairs-pendingx 3\n"} {
		if err := os.WriteFile(uncleanPath(dir), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if cause, ok := uncleanCause(dir); !ok || cause != causeCrash {
			t.Errorf("%q: cause %q present %v", body, cause, ok)
		}
	}
}

func TestAFailedLeafRepairRefusesThatLeafOnlyAndTheNextBorrowRetries(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	wantRows := oracleRows(t, want)
	dir := lazyCopy(t, src)
	release := holdRepairPass(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	trunks := conversationTrunks(s)
	bad := trunks[2]
	badKey := headNode(t, s, bad)
	var failing atomic.Bool
	failing.Store(true)
	s.Trunks.repairs.Load().testRepair = func(key string) error {
		if key == badKey && failing.Load() {
			return errors.New("injected I/O error")
		}
		return nil
	}
	if _, err := s.RecordsFrom(bad, "translations", 0, 0); err == nil || !strings.Contains(err.Error(), "injected I/O error") {
		t.Fatalf("a leaf whose repair failed was served: %v", err)
	}
	if _, err := s.RecordsFrom(trunks[3], "translations", 0, 0); err != nil {
		t.Fatalf("another leaf was refused: %v", err)
	}
	failing.Store(false)
	sameRows(t, wantRows, storeRows(t, s, trunks))
	release()
	waitRepaired(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sameStoreFiles(t, want, dir)
}

func TestBorrowsRacingThePassRepairEachLeafOnce(t *testing.T) {
	src := buildTornStore(t)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	wantRows := oracleRows(t, want)
	dir := lazyCopy(t, src)
	release := holdRepairPass(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	s.Trunks.repairs.Load().testRepair = func(key string) error {
		mu.Lock()
		seen[key]++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return nil
	}
	trunks := conversationTrunks(s)
	var wg sync.WaitGroup
	got := make([]rowsOf, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 0 {
				release()
			}
			got[i] = storeRows(t, s, trunks)
		}()
	}
	wg.Wait()
	waitRepaired(t, s)
	for _, g := range got {
		sameRows(t, wantRows, g)
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("leaf %s repaired %d times", k, n)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sameStoreFiles(t, want, dir)
}

func TestACleanStoreHasNothingPending(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if s.RepairsPending() != 0 || s.Trunks.repairs.Load() != nil {
		t.Fatal("a fresh store has repairs pending")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// FIGWAL_REPAIR_EQ_STORE: a real store, torn as b2 tore it, repaired lazily
// and held to the eager repair. FIGWAL_REPAIR_EQ_KEEP keeps the torn, eager
// and lazy copies under FIGWAL_REPAIR_EQ_WORK for the authority dump.
func TestTheLazyRepairMatchesTheEagerOneOnARealStore(t *testing.T) {
	src := os.Getenv("FIGWAL_REPAIR_EQ_STORE")
	if src == "" {
		t.Skip("FIGWAL_REPAIR_EQ_STORE not set")
	}
	work := t.TempDir()
	if d := os.Getenv("FIGWAL_REPAIR_EQ_WORK"); d != "" {
		var err error
		if work, err = os.MkdirTemp(d, "lazy-eq-"); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("FIGWAL_REPAIR_EQ_KEEP") == "" {
			t.Cleanup(func() { _ = os.RemoveAll(work) })
		} else {
			t.Logf("kept: %s (torn, eager, lazy)", work)
		}
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
	eager := filepath.Join(work, "eager")
	copyStore(t, torn, eager)
	eagerLog := runRepair(t, eager, cfg, repairCoherentCuts)
	lazy := filepath.Join(work, "lazy")
	copyStore(t, torn, lazy)
	log := captureLog(t)
	tr, err := openTrunks(lazy, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := newLeafRepairs(tr, causeCrash)
	tr.repairs.Store(r)
	stop := make(chan struct{})
	r.pass(stop, nil)
	if n := r.pending.Load(); n != 0 {
		t.Fatalf("%d leaves left pending", n)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(log.trimmed)
	t.Logf("trims: eager %d, lazy %d", len(eagerLog.trimmed), len(log.trimmed))
	if strings.Join(log.trimmed, "\n") != strings.Join(eagerLog.trimmed, "\n") {
		t.Fatal("trim actions differ")
	}
	sameStoreFiles(t, eager, lazy)
}

func withAZeroByteTranslationsDir(t *testing.T, src string) string {
	t.Helper()
	var bare string
	for _, d := range mustGlob(t, filepath.Join(src, "ir", "*")) {
		key := filepath.Base(d)
		if pathExists(filepath.Join(d, ".node")) && !pathExists(filepath.Join(src, "translations", key)) {
			bare = key
		}
	}
	if bare == "" {
		t.Fatal("fixture: no leaf without keyed rows")
	}
	empty := filepath.Join(src, "translations", bare)
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empty, segFileName(1, segment.JSONLCodec{})), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return bare
}

func TestPruningChannelDirsWithRepairsPendingRepairsFirst(t *testing.T) {
	src := buildTornStore(t)
	bare := withAZeroByteTranslationsDir(t, src)
	want, _ := eagerOracle(t, src, narrowTestOptions().config())
	prune := func(s *Store) {
		t.Helper()
		rep, err := s.PruneEmptyChannelDirs(false)
		if err != nil || rep.Skipped != "" {
			t.Fatalf("prune: %+v %v", rep, err)
		}
	}
	ws, err := OpenStore(want, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	prune(ws)
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}
	if pathExists(filepath.Join(want, "translations", bare)) {
		t.Fatal("fixture: the eager copy's empty dir survived the prune")
	}

	dir := lazyCopy(t, src)
	release := holdRepairPass(t)
	s, err := OpenStore(dir, narrowTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	trunks := conversationTrunks(s)
	if _, err := s.RecordsFrom(trunks[0], "translations", 0, 0); err != nil {
		t.Fatal(err)
	}
	prune(s)
	if n := s.RepairsPending(); n != 0 {
		t.Errorf("the prune left %d leaves to repair over the directories it removed", n)
	}
	release()
	waitRepaired(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sameStoreFiles(t, want, dir)
}

// The lazydirs take runs prune-channel-dirs three times with the daemon
// stopped: a dry run, the real run, a dry run that must read zero. Each
// opens the store, prunes and closes it.
func TestThePruneDryRealDrySequenceOverPendingRepairs(t *testing.T) {
	for _, marker := range []string{"open\n", "repairs-pending 3\n"} {
		t.Run(strings.TrimSpace(marker), func(t *testing.T) {
			src := buildTornStore(t)
			withAZeroByteTranslationsDir(t, src)
			dir := lazyCopy(t, src)
			if err := os.WriteFile(uncleanPath(dir), []byte(marker), 0o644); err != nil {
				t.Fatal(err)
			}
			run := func(dry bool) int {
				t.Helper()
				s, err := OpenStore(dir, narrowTestOptions())
				if err != nil {
					t.Fatal(err)
				}
				rep, err := s.PruneEmptyChannelDirs(dry)
				if err != nil || rep.Skipped != "" {
					t.Fatalf("prune dry=%v: %+v %v", dry, rep, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if pathExists(uncleanPath(dir)) {
					body, _ := os.ReadFile(uncleanPath(dir))
					t.Fatalf("prune dry=%v closed the store with the marker %q", dry, body)
				}
				n := 0
				for _, c := range rep.Counts {
					n += c.Removed
				}
				return n
			}
			first, real, second := run(true), run(false), run(true)
			if real == 0 {
				t.Fatal("fixture: nothing to prune")
			}
			if first != real {
				t.Errorf("the dry run would remove %d, the real run removed %d", first, real)
			}
			if second != 0 {
				t.Errorf("the dry run after the real one would remove %d", second)
			}
		})
	}
}
