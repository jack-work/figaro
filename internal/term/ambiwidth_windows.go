//go:build windows

package term

import "time"

// ProbeAmbiguousWide is not attempted on Windows: the console host answers DSR
// inconsistently across conhost and Windows Terminal, and a probe that hangs at
// startup is worse than the mismatch it is looking for. FIGARO_AMBIGUOUS_WIDE
// remains available there.
func ProbeAmbiguousWide(time.Duration) (wide, ok bool) { return false, false }

// MeasureDrawn is not attempted on Windows for the same reason as
// ProbeAmbiguousWide: the console host answers DSR inconsistently, and a
// diagnostic that hangs is worse than one that says "unknown".
func MeasureDrawn(string, time.Duration) (int, bool) { return 0, false }
