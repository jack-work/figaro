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
	if !strings.Contains(got, "OVER INK: width=20 ink=24") {
		t.Fatalf("ink overrun not reported:\n%s", got)
	}
	if !strings.Contains(got, "OVER PADDING: width=20") {
		t.Fatalf("padding overrun not reported:\n%s", got)
	}
	if n := strings.Count(got, "OVER "); n != 2 {
		t.Fatalf("want exactly 2 reports (the two overruns), got %d:\n%s", n, got)
	}
}

// Unarmed, the writer is returned untouched — no wrapper, no measurement.
func TestWidthAuditIsFreeWhenUnarmed(t *testing.T) {
	t.Setenv("FIGARO_WIDTH_AUDIT", "")
	inner := &nopWriter{}
	if got := auditWriter(inner, func() (int, int) { return 20, 40 }); got != inner {
		t.Fatalf("unarmed audit wrapped the writer: %T", got)
	}
}
