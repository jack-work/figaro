package anthropic

// THE SPLICE GATE. The rows on disk are already the wire's messages array,
// and the assembler currently json.Unmarshals every one into nativeMessage
// and re-encodes it on every request. Splicing them verbatim deletes that
// pass -- but it also changes WHAT REACHES THE MODEL, because a decode into
// a typed struct silently DROPS every field the struct does not name and
// REORDERS the ones it does.
//
// So this is not a refactor to be reasoned about. It is a byte comparison
// against the rows in a REAL store, and it must be run before the splice
// lands:
//
//	box=$(mktemp -d); chmod 700 "$box"
//	cp -a --reflink=auto ~/.local/state/figaro/arias "$box/arias"
//	FIGARO_PROBE_ROOT=$box/arias go test ./internal/provider/anthropic \
//	    -run RowsRoundTrip -v
//
// WHAT A FAILURE MEANS: not that the splice is wrong, but that the current
// assembler is EDITING the conversation on its way out -- and then the
// question is which of the two the model should have been seeing.

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

func TestRowsRoundTripThroughTheAssembler(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store")
	}
	be, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	type diff struct {
		aria   string
		lt     uint64
		before int
		after  int
		reason string
	}
	var (
		arias, rows, identical, sameValue int
		droppedTrue, droppedFalse         int
		diffs                             []diff
		lostKeys                          = map[string]int{}
	)
	for _, node := range be.Nodes() {
		log, err := be.OpenTranslator(node.ID, "anthropic")
		if err != nil {
			continue
		}
		entries := log.Read()
		if len(entries) == 0 {
			continue
		}
		arias++
		for _, e := range entries {
			for _, raw := range e.Payload {
				if len(raw) == 0 {
					continue
				}
				rows++
				var nm nativeMessage
				if err := json.Unmarshal(raw, &nm); err != nil {
					diffs = append(diffs, diff{node.ID, e.FigaroLT, len(raw), 0, "unmarshal failed: " + err.Error()})
					continue
				}
				back, err := json.Marshal(nm)
				if err != nil {
					diffs = append(diffs, diff{node.ID, e.FigaroLT, len(raw), 0, "remarshal failed"})
					continue
				}
				if string(back) == string(raw) {
					identical++
					continue
				}
				// ORDER OR CONTENT? A byte difference cannot tell them
				// apart, and only one of the two is a defect. Compare the
				// two as VALUES, and when they disagree, name the paths.
				var a, bv any
				_ = json.Unmarshal(raw, &a)
				_ = json.Unmarshal(back, &bv)
				if reflect.DeepEqual(a, bv) {
					sameValue++
					continue
				}
				for _, path := range paths(a, bv, "") {
					lostKeys[path]++
				}
				// IS THE DROPPED FIELD FALSE, OR TRUE? omitempty drops a
				// false bool, which is semantically identical -- and a
				// dropped TRUE is a tool failure the model is never told
				// about. The count above cannot tell them apart.
				for _, blk := range blocks(a) {
					v, ok := blk["is_error"]
					if !ok {
						continue
					}
					if b, isBool := v.(bool); isBool && b {
						droppedTrue++
					} else {
						droppedFalse++
					}
				}
				diffs = append(diffs, diff{node.ID, e.FigaroLT, len(raw), len(back), "VALUE differs"})
			}
		}
	}

	t.Logf("arias with anthropic rows: %d · rows: %d", arias, rows)
	t.Logf("BYTE-identical through the assembler:  %d (%.1f%%)", identical, 100*float64(identical)/float64(max(rows, 1)))
	t.Logf("VALUE-identical (same JSON, reordered): %d (%.1f%%)", sameValue, 100*float64(sameValue)/float64(max(rows, 1)))
	t.Logf("VALUE-DIFFERENT (the assembler edits the message): %d", rows-identical-sameValue)
	t.Logf("is_error blocks on differing rows: %d FALSE (omitempty, harmless) · %d TRUE (a tool failure the model is not told about)",
		droppedFalse, droppedTrue)
	if len(lostKeys) > 0 {
		keys := make([]string, 0, len(lostKeys))
		for k := range lostKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sort.Slice(keys, func(i, j int) bool { return lostKeys[keys[i]] > lostKeys[keys[j]] })
		for i, k := range keys {
			if i >= 12 {
				break
			}
			t.Logf("  PATH THE ASSEMBLER CHANGES: %-40s on %d rows", k, lostKeys[k])
		}
	}
	for i, d := range diffs {
		if i >= 5 {
			t.Logf("  ... and %d more differing rows", len(diffs)-5)
			break
		}
		t.Logf("  DIFFERS aria=%s figLT=%d %d B -> %d B (%s)", d.aria, d.lt, d.before, d.after, d.reason)
	}
	if rows == 0 {
		t.Skip("no anthropic rows in this store copy; the probe measured nothing")
	}
}

// blocks is the content array of a decoded message, as maps.
func blocks(v any) []map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := m["content"].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, b := range arr {
		if bm, ok := b.(map[string]any); ok {
			out = append(out, bm)
		}
	}
	return out
}

// paths names where two decoded values disagree, one line per leaf.
func paths(a, b any, at string) []string {
	if reflect.DeepEqual(a, b) {
		return nil
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		var out []string
		for k, av := range am {
			bv, present := bm[k]
			if !present {
				out = append(out, at+"."+k+" (DROPPED)")
				continue
			}
			out = append(out, paths(av, bv, at+"."+k)...)
		}
		for k := range bm {
			if _, present := am[k]; !present {
				out = append(out, at+"."+k+" (ADDED)")
			}
		}
		return out
	}
	as, aok2 := a.([]any)
	bs, bok2 := b.([]any)
	if aok2 && bok2 {
		if len(as) != len(bs) {
			return []string{at + " (LENGTH " + strconv.Itoa(len(as)) + "->" + strconv.Itoa(len(bs)) + ")"}
		}
		var out []string
		for i := range as {
			out = append(out, paths(as[i], bs[i], at+"[]")...)
		}
		return out
	}
	return []string{at + " (VALUE)"}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
