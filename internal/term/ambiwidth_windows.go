//go:build windows

package term

import "time"

// The console host answers DSR inconsistently across conhost and Windows
// Terminal, and a probe that hangs at startup is worse than a mismatch.
func ProbeAmbiguousWide(time.Duration) (wide, ok bool) { return false, false }

func MeasureDrawn(string, time.Duration) (int, bool) { return 0, false }
