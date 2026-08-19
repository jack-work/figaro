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
