package store

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The refcount ledger: the last few moves of every libretto's count, kept so
// that a refusal can EXPLAIN ITSELF.
//
// "release below zero" means some release had no matching retain, and that is
// an UNDER-count: the direction the reconciliation sweep cannot repair,
// because recomputing from the boards would simply count the survivor. It has
// appeared once, under a loaded full gate, and four green runs since have
// said nothing about it -- which is the problem with hunting a rare race by
// repetition: a green run is not evidence, and the run that fails is the one
// you were not watching.
//
// So the failure carries its own diagnosis. The cost is a mutex and a struct
// copy on an operation that already performs an fsync, which is nothing, and
// it is paid in production deliberately: the run that matters is the one on
// somebody's real store at 3am, not the one under a test harness.
const librettoLedgerSize = 64

type refMove struct {
	when     time.Time
	libretto string
	delta    int
	from, to int
	refused  bool
	caller   uintptr
}

var (
	ledgerMu   sync.Mutex
	ledger     [librettoLedgerSize]refMove
	ledgerNext int
)

func recordRefMove(m refMove) {
	m.when = time.Now()
	ledgerMu.Lock()
	ledger[ledgerNext%librettoLedgerSize] = m
	ledgerNext++
	ledgerMu.Unlock()
}

// LibrettoLedger renders the recent moves, oldest first. Exported so a test
// or a doctor command can read it without reaching into the package.
func LibrettoLedger() string {
	ledgerMu.Lock()
	n := ledgerNext
	snap := ledger
	ledgerMu.Unlock()

	start := 0
	if n > librettoLedgerSize {
		start = n - librettoLedgerSize
	}
	var b strings.Builder
	for i := start; i < n; i++ {
		m := snap[i%librettoLedgerSize]
		where := "?"
		if fn := runtime.FuncForPC(m.caller); fn != nil {
			file, line := fn.FileLine(m.caller)
			where = fmt.Sprintf("%s:%d", trimPath(file), line)
		}
		verb := "retain"
		if m.delta < 0 {
			verb = "release"
		}
		if m.refused {
			verb += " REFUSED"
		}
		fmt.Fprintf(&b, "  %s %-16s %s %d->%d  %s\n",
			m.when.Format("15:04:05.000"), verb, m.libretto, m.from, m.to, where)
	}
	return b.String()
}

func trimPath(p string) string {
	if i := strings.LastIndex(p, "/internal/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
