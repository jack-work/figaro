package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGoldenFramesMatchPreSGRAtTheCellLevel is the proof that comes with the
// regenerated golden frames. Axis E rewrote testdata/transcript_frames*.golden
// : that is the point of it, the bytes are supposed to shrink: so the golden
// files can no longer vouch for themselves. The pre-collapse frames are kept
// beside them as *.pre-sgr.golden, and this test replays both through the VT
// model: same rows, same sections, and every row cell-identical in character,
// rendition, cursor, erase background and final rendition.
//
// A golden rewrite you cannot check is just a golden deletion.
func TestGoldenFramesMatchPreSGRAtTheCellLevel(t *testing.T) {
	for _, name := range []string{"transcript_frames", "transcript_frames_color"} {
		t.Run(name, func(t *testing.T) {
			before := readGoldenFrames(t, name+".pre-sgr.golden")
			after := readGoldenFrames(t, name+".golden")
			if len(before) != len(after) {
				t.Fatalf("%s: %d lines before, %d after: the frames changed shape, not just their bytes",
					name, len(before), len(after))
			}
			var beforeBytes, afterBytes int
			for i := range before {
				b, a := before[i], after[i]
				if strings.HasPrefix(b, "## ") || strings.HasPrefix(a, "## ") {
					if a != b {
						t.Fatalf("line %d: section %q became %q", i+1, b, a)
					}
					continue
				}
				rowBefore, rowAfter := unquoteGoldenRow(t, i, b), unquoteGoldenRow(t, i, a)
				if d := sgrCellDiff(rowBefore, rowAfter); d != "" {
					t.Fatalf("line %d renders differently: %s\nbefore: %q\n after: %q", i+1, d, rowBefore, rowAfter)
				}
				beforeBytes += len(rowBefore)
				afterBytes += len(rowAfter)
			}
			if afterBytes >= beforeBytes {
				t.Errorf("%s: %d B -> %d B: nothing was collapsed", name, beforeBytes, afterBytes)
			}
			t.Logf("%s: %d rows, %d B -> %d B (%.2fx smaller), cell-identical",
				name, len(before), beforeBytes, afterBytes, float64(beforeBytes)/float64(afterBytes))
		})
	}
}

func readGoldenFrames(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func unquoteGoldenRow(t *testing.T, line int, s string) string {
	t.Helper()
	row, err := strconv.Unquote(s)
	if err != nil {
		t.Fatalf("line %d: %v", line+1, err)
	}
	return row
}
