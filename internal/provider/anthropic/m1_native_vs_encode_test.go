package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func blockTypes(raw []byte) string {
	var m struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return "?"
	}
	out := ""
	for _, b := range m.Content {
		var t string
		json.Unmarshal(b["type"], &t)
		out += t + ","
	}
	return out
}

func firstDiffKey(a, b []byte) string {
	var ma, mb struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	if json.Unmarshal(a, &ma) != nil || json.Unmarshal(b, &mb) != nil {
		return "unparseable"
	}
	if len(ma.Content) != len(mb.Content) {
		return fmt.Sprintf("block count %d vs %d", len(ma.Content), len(mb.Content))
	}
	for i := range ma.Content {
		keys := map[string]bool{}
		for k := range ma.Content[i] {
			keys[k] = true
		}
		for k := range mb.Content[i] {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			if !bytes.Equal(ma.Content[i][k], mb.Content[i][k]) {
				var t string
				json.Unmarshal(ma.Content[i]["type"], &t)
				return fmt.Sprintf("block %d (%s) key %q: %d vs %d bytes", i, t, k, len(ma.Content[i][k]), len(mb.Content[i][k]))
			}
		}
	}
	return "same blocks, different envelope or key order"
}

func TestM1NativeRowsEqualEncode(t *testing.T) {
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
	type tally struct{ same, diff, norow int }
	by := map[string]*tally{}
	why := map[string]int{}
	var encodeNS time.Duration
	var encoded, figaros int
	for _, id := range b.ConversationIDs() {
		ir, err := b.OpenFigIR(id)
		if err != nil {
			continue
		}
		rows, err := b.OpenTranslator(id, "anthropic")
		if err != nil {
			continue
		}
		byLT := map[uint64]store.Entry[[]json.RawMessage]{}
		for _, r := range rows.Read() {
			byLT[r.FigaroLT] = r
		}
		if len(byLT) == 0 {
			continue
		}
		figaros++
		for _, e := range ir.Read() {
			if e.Payload.Role != message.RoleOutput {
				continue
			}
			msg := e.Payload
			msg.LogicalTime = e.LT
			start := time.Now()
			enc, err := a.encode(msg, form.Snapshot{})
			encodeNS += time.Since(start)
			encoded++
			r, ok := byLT[e.LT]
			key := r.Fingerprint
			if !ok {
				key = "(no row)"
			}
			if by[key] == nil {
				by[key] = &tally{}
			}
			if !ok {
				by[key].norow++
				continue
			}
			if err != nil {
				why["encode error"]++
				by[key].diff++
				continue
			}
			if len(enc) == len(r.Payload) && func() bool {
				for i := range enc {
					if !bytes.Equal(enc[i], r.Payload[i]) {
						return false
					}
				}
				return true
			}() {
				by[key].same++
				continue
			}
			by[key].diff++
			if key == a.Fingerprint() {
				if len(enc) != len(r.Payload) {
					why[fmt.Sprintf("payload parts %d vs %d", len(r.Payload), len(enc))]++
				} else {
					d := firstDiffKey(r.Payload[len(r.Payload)-1], enc[len(enc)-1])
					if len(d) > 11 && d[:11] == "block count" {
						d = "row[" + blockTypes(r.Payload[len(r.Payload)-1]) + "] vs encode[" + blockTypes(enc[len(enc)-1]) + "]"
					}
					why[d]++
				}
			}
		}
	}
	fmt.Printf("M1 figaros with anthropic rows: %d; assistant records encoded: %d in %v (%.3f ms each)\n", figaros, encoded, encodeNS, float64(encodeNS.Microseconds())/1000/float64(max(encoded, 1)))
	var keys []string
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("M1 fingerprint %-22s identical=%d different=%d\n", k, by[k].same, by[k].diff+by[k].norow)
	}
	var ws []string
	for k := range why {
		ws = append(ws, k)
	}
	sort.Slice(ws, func(i, j int) bool { return why[ws[i]] > why[ws[j]] })
	for i, k := range ws {
		if i >= 12 {
			break
		}
		fmt.Printf("M1 current-fingerprint difference x%d: %s\n", why[k], k)
	}
}
