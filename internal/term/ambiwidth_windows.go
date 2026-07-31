//go:build windows

package term

import "time"

// ProbeAmbiguousWide is not attempted on Windows: the console host answers DSR
// inconsistently across conhost and Windows Terminal, and a probe that hangs at
// startup is worse than the mismatch it is looking for. FIGARO_AMBIGUOUS_WIDE
// remains available there.
func ProbeAmbiguousWide(time.Duration) (wide, ok bool) { return false, false }
