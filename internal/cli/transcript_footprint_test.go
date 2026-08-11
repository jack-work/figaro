package cli

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
)

// rowCacheFootprint accounts for the bytes the transcript's row cache keeps
// alive: the row slices themselves plus the string data they point at. It is
// the memory half of the transcript's cost: the half that does not show up in
// -benchmem, because it is retained rather than churned.
func (t *transcript) rowCacheFootprint() (rows, structBytes, textBytes, escBytes int) {
	for _, msg := range t.rowCache {
		rows += len(msg.rows)
		structBytes += cap(msg.rows) * rowStructSize
		for _, r := range msg.rows {
			textBytes += len(r.text)
			for i := 0; i < len(r.text); {
				if r.text[i] == 0x1b {
					j := skipANSI(r.text, i)
					escBytes += j - i
					i = j
					continue
				}
				i++
			}
		}
	}
	return rows, structBytes, textBytes, escBytes
}

const rowStructSize = 32 // string header (16) + nodeRef{int,int} (16)

// BenchmarkTranscriptRowCacheFootprint reports retained row-cache memory for a
// heavy transcript rather than per-op churn. Run with -benchtime 1x; the
// interesting output is the custom metrics, not ns/op.
//
//	rows        physical rows held in the cache
//	struct_B    []transcriptRow backing arrays
//	text_B      row string data
//	esc_B       of which ANSI escape sequences (glamour styles per cell, so
//	            this is ~76% of the text and is the largest remaining lever
//	            on retained memory)
//	B/row       total retained bytes per row
func BenchmarkTranscriptRowCacheFootprint(b *testing.B) {
	for _, outputLines := range []int{20, 200} {
		b.Run(fmt.Sprintf("out%d", outputLines), func(b *testing.B) {
			for range b.N {
				tr, _ := heavyTranscript(b, 200, outputLines)
				tr.scrollBy(-1)
				rows, structBytes, textBytes, escBytes := tr.rowCacheFootprint()
				b.ReportMetric(float64(rows), "rows")
				b.ReportMetric(float64(structBytes), "struct_B")
				b.ReportMetric(float64(textBytes), "text_B")
				b.ReportMetric(float64(escBytes), "esc_B")
				if rows > 0 {
					b.ReportMetric(float64(structBytes+textBytes)/float64(rows), "B/row")
				}
			}
		})
	}
}

// TestTranscriptRowCacheFootprintIsBounded is the regression guard: the row
// cache must stay proportional to the retained window, not to the aria. It
// also documents the shape of the retained memory for whoever reads the
// footprint benchmark.
func TestTranscriptRowCacheFootprintIsBounded(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, 200)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 200)}}
	}
	client.Apply(aria.Page{Parts: committed})
	tr := newTranscript(io.Discard, 100, 40, &ariaView{settings: &renderSettings{}}, client, "bench", time.Unix(0, 0))
	tr.enter()
	tr.scrollBy(-1)

	if got := len(tr.rowCache); got > transcriptPageSize*transcriptPageLimit {
		t.Fatalf("row cache holds %d messages, want <= %d", got, transcriptPageSize*transcriptPageLimit)
	}
	rows, structBytes, textBytes, escBytes := tr.rowCacheFootprint()
	if rows == 0 {
		t.Fatal("row cache is empty")
	}
	// The backing arrays must not carry append slack: renderMsgBase clips
	// them, and this is what keeps that honest.
	if float64(structBytes) > 1.1*float64(rows*rowStructSize) {
		t.Errorf("row slices carry %d B of backing array for %d rows (%d B exact); append slack is retained",
			structBytes, rows, rows*rowStructSize)
	}
	t.Logf("rows=%d struct=%d B text=%d B (esc=%d B, %.0f%%) total=%d B (%.0f B/row)",
		rows, structBytes, textBytes, escBytes,
		100*float64(escBytes)/float64(textBytes),
		structBytes+textBytes, float64(structBytes+textBytes)/float64(rows))
}
