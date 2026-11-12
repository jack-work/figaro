package anthropic

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func TestM2RebuildCost(t *testing.T) {
	dir := os.Getenv("M1_STORE")
	if dir == "" {
		t.Skip("M1_STORE not set")
	}
	b, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a := &Anthropic{}
	type cost struct {
		records, images, bytes int
		read, work             time.Duration
	}
	var all []cost
	for _, id := range b.ConversationIDs() {
		ir, err := b.OpenFigIR(id)
		if err != nil {
			continue
		}
		var c cost
		start := time.Now()
		entries := ir.Read()
		c.read = time.Since(start)
		start = time.Now()
		for _, e := range entries {
			if e.Payload.Role == message.RoleGenesis {
				continue
			}
			msg := e.Payload
			msg.LogicalTime = e.LT
			for _, blk := range msg.Content {
				if blk.Type == message.ContentImage {
					c.images++
				}
			}
			msg, _ = provider.SendableImages(msg)
			enc, err := a.encode(msg, form.Snapshot{})
			if err != nil {
				continue
			}
			for _, r := range enc {
				c.bytes += len(r)
			}
			if _, err := store.FigaroHash(e.Payload); err != nil {
				continue
			}
			c.records++
		}
		c.work = time.Since(start)
		if c.records > 0 {
			all = append(all, c)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].work < all[j].work })
	pick := func(q float64) cost { return all[int(q*float64(len(all)-1))] }
	for _, q := range []float64{0.5, 0.9, 1} {
		c := pick(q)
		fmt.Printf("M2 q=%.2f records=%d images=%d row bytes=%.1fMB read IR=%v encode+fit+hash=%v (%.3f ms/record)\n", q, c.records, c.images, float64(c.bytes)/1e6, c.read.Round(time.Millisecond), c.work.Round(time.Millisecond), float64(c.work.Microseconds())/1000/float64(c.records))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].images < all[j].images })
	c := all[len(all)-1]
	fmt.Printf("M2 most images: records=%d images=%d row bytes=%.1fMB read IR=%v encode+fit+hash=%v\n", c.records, c.images, float64(c.bytes)/1e6, c.read.Round(time.Millisecond), c.work.Round(time.Millisecond))
	var tr, tb int
	var tw time.Duration
	for _, c := range all {
		tr += c.records
		tb += c.bytes
		tw += c.work
	}
	fmt.Printf("M2 all %d figaros: records=%d row bytes=%.1fMB encode+fit+hash=%v\n", len(all), tr, float64(tb)/1e6, tw.Round(time.Millisecond))
}
