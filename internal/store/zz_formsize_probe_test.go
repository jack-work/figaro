package store

import (
	"os"
	"sort"
	"testing"
)

// HOW LONG IS A FORM CHANNEL? A catch-up that keeps no representation must
// rebuild the board AS IT STOOD AT A RECORD, and without a memo that is a
// fold from zero over the form's patches -- once per catch-up, which is once
// per turn. Whether that is trivial or a rescan is this number.
func TestFormChannelLengths(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store")
	}
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	var lens []int
	for _, n := range be.Nodes() {
		v, err := be.FormVersion(n.ID)
		if err != nil {
			continue
		}
		lens = append(lens, int(v))
	}
	if len(lens) == 0 {
		t.Skip("no forms")
	}
	sort.Ints(lens)
	at := func(p float64) int { return lens[int(float64(len(lens)-1)*p)] }
	sum := 0
	for _, v := range lens {
		sum += v
	}
	t.Logf("form channels: n=%d  min=%d  p50=%d  p90=%d  p99=%d  max=%d  mean=%.1f",
		len(lens), lens[0], at(0.50), at(0.90), at(0.99), lens[len(lens)-1], float64(sum)/float64(len(lens)))
}
