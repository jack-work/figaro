package store

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// THE CENSUS THAT DECIDES THE DOOR'S FAN-OUT.
//
// Rows are per provider, so a door writing rows at record time must decide
// WHICH translator channels to write. "Every channel the aria already has" is
// the rule that keeps the store ignorant of providers -- but Gluck's caution
// is the requirement: a channel that EXISTS is not evidence a provider is IN
// USE. An aria that ever spoke to a provider keeps that channel forever, so
// the set only grows, and a door that writes to all of them does encoder work
// on the write path for providers nobody has used in months.
//
// This counts, per aria: how many translator channels exist, and how many are
// LIVE -- their newest row within reach of the fig IR tail. If the median aria
// has one live channel and a few fossils, the discriminator has to be
// recency, not existence.
//
// SKIPPED UNLESS FIGARO_REAL_STORE POINTS AT A COPY OF A STORE.
func TestTranslatorChannelCensus(t *testing.T) {
	root := os.Getenv("FIGARO_REAL_STORE")
	if root == "" {
		t.Skip("set FIGARO_REAL_STORE to a store root to run this instrument")
	}

	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	ids := be.ConversationIDs()
	if len(ids) == 0 {
		t.Skip("no arias in the store")
	}

	// An aria's channel is LIVE if its newest row names a fig IR record within
	// this many records of the aria's own tail. A turn is several records, so
	// this is a few turns of slack, not a knife edge.
	const liveWithin = 50

	var (
		existing   []int
		live       []int
		perChannel = map[string]int{}
		liveChan   = map[string]int{}
		noChannels int
		examined   int
	)
	for _, id := range ids {
		ir, err := be.Open(id)
		if err != nil {
			continue
		}
		tail, ok := ir.PeekTail()
		if !ok {
			continue
		}
		examined++

		infos, err := be.store.trunks.Channels(id)
		if err != nil {
			continue
		}
		var have, alive int
		for _, info := range infos {
			if !strings.HasPrefix(info.Name, "translations-v2/") {
				continue
			}
			provider := strings.TrimPrefix(info.Name, "translations-v2/")
			rows, err := be.OpenTranslation(id, provider)
			if err != nil {
				continue
			}
			rowTail, ok := rows.PeekTail()
			if !ok {
				continue // a channel with no rows at all is not even a fossil
			}
			have++
			perChannel[provider]++
			if rowTail.FigaroLT+liveWithin >= tail.LT {
				alive++
				liveChan[provider]++
			}
		}
		if have == 0 {
			noChannels++
			continue
		}
		existing = append(existing, have)
		live = append(live, alive)
	}

	report := func(label string, xs []int) {
		if len(xs) == 0 {
			t.Logf("%-22s no data", label)
			return
		}
		sort.Ints(xs)
		sum := 0
		for _, x := range xs {
			sum += x
		}
		at := func(q float64) int { return xs[min(len(xs)-1, int(q*float64(len(xs))))] }
		t.Logf("%-22s n=%d  p50=%d p90=%d p99=%d max=%d  mean=%.2f",
			label, len(xs), at(0.50), at(0.90), at(0.99), xs[len(xs)-1], float64(sum)/float64(len(xs)))
	}

	t.Logf("arias examined=%d, with no translator rows at all=%d", examined, noChannels)
	report("channels per aria", append([]int(nil), existing...))
	report("LIVE channels per aria", append([]int(nil), live...))

	fossils := 0
	for i := range existing {
		fossils += existing[i] - live[i]
	}
	t.Logf("fossil channels (exist, not live) across the store: %d", fossils)
	for name, n := range perChannel {
		t.Logf("  channel %-24s exists on %d arias, live on %d", name, n, liveChan[name])
	}
}
