package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type nopWriter struct{ n int }

func (w *nopWriter) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }

// The audit must fire on ink past the edge, fire on PADDING past the edge
// (which wraps just as loudly), and stay silent on a row that fits exactly.
//
// It must also be free when unarmed: auditWriter returns the original writer,
// not a wrapper, so an unset env var costs one getenv per session and nothing
// per row.
func TestWidthAuditReportsOnlyRealOverruns(t *testing.T) {
	log := filepath.Join(t.TempDir(), "audit.log")
	t.Setenv("FIGARO_WIDTH_AUDIT", log)

	size := func() (int, int) { return 20, 40 }
	w := auditWriter(&nopWriter{}, size)

	w.Write([]byte("12345678901234567890\r\n"))                      // exactly 20: fits
	w.Write([]byte("\x1b[38;5;252m12345678901234567890\x1b[0m\r\n")) // 20 + SGR: fits
	w.Write([]byte("123456789012345678901234\r\n"))                  // 24 cells of ink
	w.Write([]byte("hello               \r\n"))                      // 20: fits
	w.Write([]byte("hello                         \r\n"))            // padded to 30

	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("audit wrote no log: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "OVER INK: width=20 ioctl=") || !strings.Contains(got, "startcol=0 ends=24") {
		t.Fatalf("ink overrun not reported:\n%s", got)
	}
	if !strings.Contains(got, "OVER PADDING: width=20") {
		t.Fatalf("padding overrun not reported:\n%s", got)
	}
	if n := strings.Count(got, "OVER "); n != 2 {
		t.Fatalf("want exactly 2 reports (the two overruns), got %d:\n%s", n, got)
	}
}

// A row can fit on its own and still run off the edge, because it did not start
// at column 1. Measuring the row alone is a second way to be blind: the row is
// innocent, the width is innocent, and the screen is wrong by exactly the
// offset — which is what "a couple of characters beyond the edge" looks like.
//
// CANARY (watched): ignore the start column (measure the row alone) and the
// second write below stops being reported.
func TestWidthAuditCountsTheColumnARowStartsAt(t *testing.T) {
	log := filepath.Join(t.TempDir(), "audit.log")
	t.Setenv("FIGARO_WIDTH_AUDIT", log)
	w := auditWriter(&nopWriter{}, func() (int, int) { return 20, 40 })

	w.Write([]byte("\rshort\r\n"))           // 5 cells from column 0: fits
	w.Write([]byte("\x1b[1;15Hsevencl\r\n")) // 7 cells from column 14: ends at 21

	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("audit wrote no log: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "startcol=14 ends=21") {
		t.Fatalf("a row overflowing because of where it STARTS was not reported:\n%s", got)
	}
	if strings.Contains(got, "%!q(MISSING)") {
		t.Fatalf("report is malformed:\n%s", got)
	}
}

// With no env var set, an overrun still leaves a receipt — and a healthy
// session creates no file at all.
//
// Four reports of this bug produced one reproduction, because every route to
// evidence went through the reporter remembering to arm something first.
// figaro keeps the receipt itself now: the file is opened lazily, on the first
// overrun, so the common case touches nothing.
func TestOverrunsAreRecordedWithoutBeingAsked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIGARO_CACHE_DIR", dir)
	t.Setenv("FIGARO_WIDTH_AUDIT", "")
	log := filepath.Join(dir, "width-overruns.log")

	w := auditWriter(&nopWriter{}, func() (int, int) { return 20, 40 })
	w.Write([]byte("12345678901234567890\r\n")) // exactly 20: fits
	if _, err := os.Stat(log); err == nil {
		t.Fatal("a healthy session must not create a report file")
	}

	w.Write([]byte("123456789012345678901234\r\n")) // 24 cells
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("an overrun left no receipt: %v", err)
	}
	if !strings.Contains(string(b), "ends=24") {
		t.Fatalf("receipt does not name the overrun:\n%s", b)
	}
}

// FIGARO_WIDTH_AUDIT=off must return the writer untouched: somebody who does
// not want the measurement should not pay for it.
func TestOverrunRecordingCanBeTurnedOff(t *testing.T) {
	t.Setenv("FIGARO_CACHE_DIR", t.TempDir())
	t.Setenv("FIGARO_WIDTH_AUDIT", "off")
	inner := &nopWriter{}
	if got := auditWriter(inner, func() (int, int) { return 20, 40 }); got != inner {
		t.Fatalf("audit=off still wrapped the writer: %T", got)
	}
}
