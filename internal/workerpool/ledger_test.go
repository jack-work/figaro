package workerpool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const broker = "service:broker-server@auth.kelliher.info"

func openLedger(t *testing.T, path string) *Ledger {
	t.Helper()
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestTheLedgerIsPrivateAndIsTheSameAfterARestart(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "slot", "ledger.json")
	l := openLedger(t, path)
	if l.Found() {
		t.Fatal("a ledger that was never written says it was found")
	}
	must(t, l.Begin("groups", "slot0", "msg:-100", 1, broker))
	must(t, l.Opens("groups", "msg:-100", 1, "groups/slot0-g1"))
	must(t, l.Minted("groups", "msg:-100", 1, "aaaa", "@bbbb"))
	for _, p := range []string{filepath.Dir(path), path} {
		if st, _ := os.Stat(p); st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v under umask 0: the ledger holds bearers", p, st.Mode().Perm())
		}
	}
	l = openLedger(t, path)
	got, ok := l.Held("groups", "msg:-100")
	if !l.Found() || !ok || got.State != Minting || got.Worker != "slot0" || got.Generation != 1 || got.Holder != broker || got.Rou2Lease != "groups/slot0-g1" || len(got.Minted) != 2 {
		t.Fatalf("after a restart: found %v, held %v, %+v", l.Found(), ok, got)
	}
}

// Deliver and say are refused from the moment a release starts, so the bearer
// has no use after it; the wipe is only true if it leaves the disk then.
func TestALeaseMovesOnlyForwardAndItsBearerLeavesTheDiskAtRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	l := openLedger(t, path)
	const bearer = "BEARERCANARY5f1c"
	must(t, l.Begin("groups", "slot0", "k", 1, broker))
	if err := l.Released("groups", "k", 1); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a lease still minting was marked released: %v", err)
	}
	must(t, l.GoLive("groups", "k", 1, bearer))
	if !holds(t, path, bearer) {
		t.Fatal("a live lease's bearer is not in the ledger, so a restart would lose it")
	}
	if err := l.GoLive("groups", "k", 1, "another"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a live lease was given a second bearer: %v", err)
	}
	if err := l.Opens("groups", "k", 1, "x"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a live lease was given another rou2 lease: %v", err)
	}
	row, err := l.Release("groups", "k", 1)
	if err != nil || row.State != Releasing || row.SayBearer != "" {
		t.Fatalf("release: %+v, %v", row, err)
	}
	if holds(t, path, bearer) {
		t.Fatal("the bearer is still on disk after the release began")
	}
	if _, err := l.Release("groups", "k", 1); err != nil {
		t.Fatalf("a release asked for twice, as recovery does: %v", err)
	}
	if err := l.GoLive("groups", "k", 1, bearer); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a releasing lease went live again: %v", err)
	}
	if err := l.Minted("groups", "k", 1, "cccc"); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a mint was recorded under a lease being released, so its minter would not know to stop: %v", err)
	}
	must(t, l.Released("groups", "k", 1))
	if _, held := l.Held("groups", "k"); held {
		t.Fatal("a released lease is still held")
	}
	got, ok := l.Find("groups", "k", 1)
	if !ok || got.State != Released || got.Holder != broker || len(got.Minted) != 0 {
		t.Fatalf("the released row a late retry is answered from: %v %+v", ok, got)
	}
	for name, err := range map[string]error{
		"release":  second(l.Release("groups", "k", 1)),
		"live":     l.GoLive("groups", "k", 1, bearer),
		"minted":   l.Minted("groups", "k", 1, "dddd"),
		"released": l.Released("groups", "k", 1),
	} {
		if !errors.Is(err, ErrWrongState) {
			t.Errorf("%s on a released lease: %v", name, err)
		}
	}
	if _, err := l.Release("groups", "k", 2); !errors.Is(err, ErrNoSuchLease) {
		t.Fatalf("a release for a generation never leased: %v", err)
	}
}

func second[T any](_ T, err error) error { return err }

// R1 rests on a generation naming one lease for good. The released rows are
// trimmed, so the floor is kept apart from them.
func TestAWorkerAndAKeyHoldOneLeaseAndNoGenerationIsUsedTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	l := openLedger(t, path)
	must(t, l.Begin("groups", "slot0", "a", 1, broker))
	if err := l.Begin("groups", "slot0", "b", 2, broker); !errors.Is(err, ErrWorkerLeased) {
		t.Errorf("a second lease of a leased slot: %v", err)
	}
	if err := l.Begin("groups", "slot1", "a", 1, broker); !errors.Is(err, ErrKeyLeased) {
		t.Errorf("a second lease for a key that holds one: %v", err)
	}
	must(t, l.Begin("other", "slot1", "a", 1, broker))
	for gen := uint64(1); gen <= releasedKept+3; gen++ {
		if gen > 1 {
			must(t, l.Begin("groups", "slot0", "a", gen, broker))
		}
		_, err := l.Release("groups", "a", gen)
		must(t, err)
		must(t, l.Released("groups", "a", gen))
	}
	if n := len(l.Leases()); n != releasedKept+1 {
		t.Fatalf("the ledger keeps %d rows, want %d released and the one live", n, releasedKept)
	}
	if _, ok := l.Find("groups", "a", 1); ok {
		t.Fatal("the oldest released row was kept past the bound")
	}
	l = openLedger(t, path)
	if got := l.Floor("slot0"); got != releasedKept+3 {
		t.Fatalf("the floor of slot0 after its rows were trimmed and a restart: %d", got)
	}
	for _, gen := range []uint64{1, releasedKept + 3} {
		if err := l.Begin("groups", "slot0", "c", gen, broker); !errors.Is(err, ErrGenerationUsed) {
			t.Fatalf("generation %d of slot0 leased a second time: %v", gen, err)
		}
	}
	must(t, l.Begin("groups", "slot0", "c", releasedKept+4, broker))
}

func TestALeaseKeyCannotCarryProse(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "ledger.json"))
	for _, key := range []string{"", "two words", "line\nbreak", "quote\"", strings.Repeat("k", 129), "naïve"} {
		if err := l.Begin("groups", "slot0", key, 1, broker); err == nil {
			t.Errorf("a lease took the key %q", key)
		}
	}
	must(t, l.Begin("groups", "slot0", "chat:-1001234567890", 1, broker))
}

// A caller mints only after the ledger says yes. If the write failed and the
// row were kept in memory, a crash would leave a figaro no ledger names.
func TestAWriteThatFailsChangesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "slot")
	path := filepath.Join(dir, "ledger.json")
	l := openLedger(t, path)
	must(t, l.Begin("groups", "slot0", "a", 1, broker))
	must(t, os.Chmod(dir, 0o500))
	defer os.Chmod(dir, 0o700)
	if err := l.Begin("groups", "slot1", "b", 1, broker); err == nil {
		t.Fatal("the ledger said yes with nowhere to write")
	}
	if _, held := l.Held("groups", "b"); held {
		t.Fatal("a lease the disk does not hold is held in memory")
	}
	if _, err := l.Page("groups", "a", 1, PurgeFailed, ""); err == nil {
		t.Fatal("a page was recorded with nowhere to write")
	}
	if got, _ := l.Pages(context.Background(), "groups", nil, 0); len(got) != 0 {
		t.Fatalf("a page the disk does not hold is returned: %+v", got)
	}
	if l.Floor("slot1") != 0 {
		t.Fatal("the floor moved for a lease that was never written")
	}
}

func TestALedgerThisBuildDidNotWriteIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"not json":         "minting slot0\n",
		"another version":  `{"version": 2, "leases": [], "floors": {}, "pages": [], "last_page": {}}`,
		"no version":       `{"leases": []}`,
		"an unknown field": `{"version": 1, "leases": [], "transcripts": ["hello"]}`,
	} {
		path := filepath.Join(t.TempDir(), "ledger.json")
		must(t, os.WriteFile(path, []byte(body), 0o600))
		if _, err := OpenLedger(path); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	path := filepath.Join(t.TempDir(), "ledger.json")
	stale := filepath.Join(filepath.Dir(path), ".ledger.json.tmp123")
	must(t, os.WriteFile(stale, []byte(`{"say_bearer": "left by a crash"}`), 0o600))
	openLedger(t, path)
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("a half-written ledger from before a crash, which can hold a bearer, was left on disk")
	}
}

func TestAPageIsHeldUntilItsExactIdIsSeen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.json")
	l := openLedger(t, path)
	ids := func(pool string, seen ...uint64) string {
		t.Helper()
		got, err := l.Pages(ctx, pool, seen, 0)
		must(t, err)
		var out []uint64
		for _, p := range got {
			if p.Pool != pool {
				t.Fatalf("pool %s was given a page of %s", pool, p.Pool)
			}
			out = append(out, p.ID)
		}
		return fmt.Sprint(out)
	}
	for i, kind := range []PageKind{SuccessionFailed, SuccessionBound, PurgeFailed} {
		id, err := l.Page("groups", "a", 4, kind, "0123abcd")
		if err != nil || id != uint64(i+1) {
			t.Fatalf("page %d: id %d, %v", i+1, id, err)
		}
	}
	if id, _ := l.Page("other", "z", 1, LeaseClosed, ""); id != 1 {
		t.Fatalf("another pool's first page has id %d", id)
	}
	if got := ids("groups", 3); got != "[1 2]" {
		t.Fatalf("after seeing 3 alone: %s; a later id must not acknowledge an earlier one", got)
	}
	if got := ids("groups"); got != "[1 2]" {
		t.Fatalf("asked again with nothing seen: %s", got)
	}
	if got := ids("other", 1, 2); got != "[]" {
		t.Fatalf("the other pool after seeing its page: %s", got)
	}
	if got := ids("groups"); got != "[1 2]" {
		t.Fatalf("another pool's acknowledgement reached this one: %s", got)
	}
	l = openLedger(t, path)
	if got := ids("groups", 99); got != "[1 2]" {
		t.Fatalf("after a restart, and seeing an id never given: %s", got)
	}
	if got := ids("groups", 2, 1); got != "[]" {
		t.Fatalf("after seeing both: %s", got)
	}
	l = openLedger(t, path)
	if id, _ := l.Page("groups", "a", 4, LeaseExhausted, ""); id != 4 {
		t.Fatalf("the next page after every one was seen, and a restart, has id %d: broker dedupes on the id", id)
	}
}

// B7 for pages: broker forwards them to Gluck, so nothing a guest typed or an
// upstream quoted may ride along. There is no text field to put it in.
func TestAPageHasNoFieldThatCarriesText(t *testing.T) {
	const canary = "ZQXPAGECANARY the guest said this"
	path := filepath.Join(t.TempDir(), "ledger.json")
	l := openLedger(t, path)
	for name, err := range map[string]error{
		"as the kind":   second(l.Page("groups", "a", 1, PageKind(canary), "")),
		"as the key":    second(l.Page("groups", canary, 1, PurgeFailed, "")),
		"as the figaro": second(l.Page("groups", "a", 1, PurgeFailed, canary)),
		"in no pool":    second(l.Page("", "a", 1, PurgeFailed, "")),
	} {
		if err == nil {
			t.Errorf("%s: recorded", name)
		} else if strings.Contains(err.Error(), "ZQX") {
			t.Errorf("%s: the refusal quotes what it refused: %v", name, err)
		}
	}
	if _, err := l.Page("groups", "a", 1, PurgeFailed, "0123abcd"); err != nil {
		t.Fatal(err)
	}
	got, _ := l.Pages(context.Background(), "groups", nil, 0)
	raw, _ := json.Marshal(got)
	var fields []map[string]json.RawMessage
	must(t, json.Unmarshal(raw, &fields))
	if len(fields) != 1 {
		t.Fatalf("pages: %s", raw)
	}
	for k := range fields[0] {
		if !strings.Contains(" id at pool key generation kind figaro dropped ", " "+k+" ") {
			t.Errorf("a page carries %q, which the note does not list", k)
		}
	}
	if strings.Contains(string(raw), "ZQX") || holds(t, path, "ZQX") {
		t.Fatal("the canary reached a page or the ledger")
	}
}

func TestPagesWaitsForAnEventOrForItsTimeAndOneCallWaitsPerPool(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "ledger.json"))
	ctx := context.Background()
	for _, wait := range []time.Duration{MaxPageWait + time.Second, -time.Second} {
		if _, err := l.Pages(ctx, "groups", nil, wait); !errors.Is(err, ErrPageWait) {
			t.Errorf("a wait of %v: %v", wait, err)
		}
	}
	type answer struct {
		pages []Page
		err   error
		took  time.Duration
	}
	call := func(ctx context.Context, wait time.Duration) chan answer {
		out := make(chan answer, 1)
		start := time.Now()
		go func() {
			p, err := l.Pages(ctx, "groups", nil, wait)
			out <- answer{p, err, time.Since(start)}
		}()
		return out
	}
	waitingOn := func(n int) {
		t.Helper()
		for range 400 {
			l.mu.Lock()
			got := len(l.waiting)
			l.mu.Unlock()
			if got == n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("never saw %d waiting calls", n)
	}

	if a := <-call(ctx, 60*time.Millisecond); a.err != nil || len(a.pages) != 0 || a.took < 60*time.Millisecond {
		t.Fatalf("with nothing to say: %+v", a)
	}

	first := call(ctx, 30*time.Second)
	waitingOn(1)
	second := call(ctx, 30*time.Second)
	if a := <-first; a.err != nil || len(a.pages) != 0 || a.took > 10*time.Second {
		t.Fatalf("the older wait when a newer call arrived: %+v", a)
	}
	waitingOn(1)
	if _, err := l.Page("other", "z", 1, LeaseClosed, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-second:
		t.Fatalf("another pool's page woke this pool's call: %+v", a)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := l.Page("groups", "a", 1, PurgeFailed, ""); err != nil {
		t.Fatal(err)
	}
	if a := <-second; a.err != nil || len(a.pages) != 1 || a.took > 10*time.Second {
		t.Fatalf("the waiting call when a page was recorded: %+v", a)
	}
	waitingOn(0)
	if a := <-call(ctx, 30*time.Second); len(a.pages) != 1 || a.took > 10*time.Second {
		t.Fatalf("a call that finds a page held should not wait: %+v", a)
	}
	got, _ := l.Pages(ctx, "groups", []uint64{1}, 0)
	if len(got) != 0 {
		t.Fatalf("after seeing it: %+v", got)
	}
	gone, cancel := context.WithCancel(ctx)
	third := call(gone, 30*time.Second)
	waitingOn(1)
	cancel()
	if a := <-third; !errors.Is(a.err, context.Canceled) {
		t.Fatalf("a caller that hung up: %+v", a)
	}
	waitingOn(0)
}

func TestAPoolHoldsABoundedNumberOfUnseenPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	file := ledgerFile{Version: ledgerVersion, LastPage: map[string]uint64{"groups": pagesHeld}}
	for i := uint64(1); i <= pagesHeld; i++ {
		file.Pages = append(file.Pages, Page{ID: i, Pool: "groups", Key: "a", Generation: 1, Kind: SuccessionBound})
	}
	raw, _ := json.Marshal(file.clone())
	must(t, os.WriteFile(path, raw, 0o600))
	l := openLedger(t, path)
	if _, err := l.Page("groups", "a", 1, PurgeFailed, ""); !errors.Is(err, ErrPagesFull) {
		t.Fatalf("page %d with none seen: %v", pagesHeld+1, err)
	}
	if _, err := l.Page("other", "a", 1, PurgeFailed, ""); err != nil {
		t.Fatalf("one pool's backlog stopped another's page: %v", err)
	}
	if _, err := l.Page("groups", "a", 1, LeaseClosed, ""); !errors.Is(err, ErrPagesFull) {
		t.Fatalf("a second page over the bound: %v", err)
	}
	l = openLedger(t, path)
	if got := l.Dropped("groups"); got != 2 {
		t.Fatalf("the pool refused two pages and, after a restart, counts %d: a refusal only in a log is one nobody is paged about", got)
	}
	if _, err := l.Pages(context.Background(), "groups", []uint64{7}, 0); err != nil {
		t.Fatal(err)
	}
	if id, err := l.Page("groups", "a", 1, PurgeFailed, ""); err != nil || id != pagesHeld+1 {
		t.Fatalf("after one was seen: id %d, %v", id, err)
	}
	got, _ := l.Pages(context.Background(), "groups", nil, 0)
	if last := got[len(got)-1]; last.Dropped != 2 || l.Dropped("groups") != 0 {
		t.Fatalf("the first page recorded after the refusals says %d were dropped, and the pool still counts %d", last.Dropped, l.Dropped("groups"))
	}
	if id, err := l.Page("other", "a", 1, PurgeFailed, ""); err != nil || id != 2 {
		t.Fatalf("another pool's next page: id %d, %v", id, err)
	} else if other, _ := l.Pages(context.Background(), "other", nil, 0); other[1].Dropped != 0 {
		t.Fatalf("a pool that dropped nothing says it dropped %d", other[1].Dropped)
	}
}
