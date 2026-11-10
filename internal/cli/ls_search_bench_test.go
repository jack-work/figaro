package cli

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func copied(level map[string]any, copy int) map[string]any {
	out := map[string]any{}
	for k, v := range level {
		r, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name := fmt.Sprintf("%s-%d", k, copy)
		sum := md5.Sum([]byte(name))
		c := map[string]any{"id": hex.EncodeToString(sum[:]), "state": r["state"]}
		if at, ok := r["active_at"].(float64); ok {
			c["active_at"] = at - float64(copy)*3_600_000
		}
		if m, ok := r["mantra"].(string); ok {
			c["mantra"] = m
		}
		if forks, ok := r["forks"].(map[string]any); ok {
			c["forks"] = copied(forks, copy)
		}
		out[name] = c
	}
	return out
}

// The fixture's 59 figaros copied 25 times: a store of 1475, the attended
// figaro's own form mounted under its row as :ls opens it.
func lsStore(b *testing.B) (form.Snapshot, []ariaRow, string, form.Snapshot) {
	arias, attended := lsSearchFixture(b)
	top := arias["arias"].(map[string]any)
	all := map[string]any{}
	for c := range 25 {
		for k, v := range copied(top, c) {
			all[k] = v
		}
	}
	doc := map[string]any{"arias": all}
	rows := ariaRows(all, "arias")
	sum := md5.Sum([]byte("hydrant-foggy-0"))
	return snapOfJSON(doc), rows, hex.EncodeToString(sum[:]), snapOfJSON(attended)
}

// open is :ls on a 1475-figaro store: the index, @arias and the attended
// form mounted, the first frame drawn. keystroke is one letter typed into
// its search, and the frame after it.
func BenchmarkLsSearch(b *testing.B) {
	arias, rows, attendedID, attended := lsStore(b)
	if len(rows) < 1400 {
		b.Fatalf("store holds %d figaros", len(rows))
	}
	frame := func(d *pit) {
		d.fit(200)
		d.lines(200, 40)
	}
	b.Run("open", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			_, v := lsRig(arias, rows, attendedID, attended)
			var d pit
			d.showLive(pitTroupe, v)
			frame(&d)
		}
	})
	b.Run("keystroke", func(b *testing.B) {
		_, v := lsRig(arias, rows, attendedID, attended)
		var d pit
		d.showLive(pitTroupe, v)
		frame(&d)
		var typed []string
		for _, q := range []string{"lyric map", "build-queue", "rossini", "netmon"} {
			for k := 1; k <= len(q); k++ {
				typed = append(typed, q[:k])
			}
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			v.SetQuery(typed[i%len(typed)])
			frame(&d)
		}
	})
}
