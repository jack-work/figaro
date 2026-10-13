package cli

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
)

// Retained row-cache bytes, which -benchmem does not show.
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

// string header (16) + nodeRef{int,int} (16)
const rowStructSize = 32

// Run with -benchtime 1x; the custom metrics are the output.
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

func TestTranscriptRowCacheFootprintIsBounded(t *testing.T) {
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	committed := make([]aria.TurnPart, 200)
	for i := range committed {
		committed[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true, Nodes: heavyNodes(i+1, 200)}}
	}
	client.Apply(aria.Page{Parts: committed}, aria.Notify)
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
	// renderMsgBase clips the backing arrays: no append slack retained.
	if float64(structBytes) > 1.1*float64(rows*rowStructSize) {
		t.Errorf("row slices carry %d B of backing array for %d rows (%d B exact); append slack is retained",
			structBytes, rows, rows*rowStructSize)
	}
	t.Logf("rows=%d struct=%d B text=%d B (esc=%d B, %.0f%%) total=%d B (%.0f B/row)",
		rows, structBytes, textBytes, escBytes,
		100*float64(escBytes)/float64(textBytes),
		structBytes+textBytes, float64(structBytes+textBytes)/float64(rows))
}
