package form_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
)

// Benchmark fixtures. Three boards, all deterministic:
//
//   default: the real thing. A verbatim capture of the user's default
//             outfit board (26 skill envelopes + system.credo + the
//             system.* scalars), committed as
//             testdata/board-default.json. See
//             testdata/board-default.provenance.md for exactly how it
//             was captured; nothing here reads live config.
//   large: synthetic: 5,000 keys, values averaging ~2KB, with a few
//             64KB blobs. The "synthetically large aria".
//   huge: synthetic: 50,000 keys, small values.
//
// Every fixture is built once per process (sync.OnceValue) and handed
// to buildBoard (the seam) to become a Snapshot.

const defaultBoardPath = "testdata/board-default.json"

var defaultBoardMap = sync.OnceValue(func() map[string]json.RawMessage {
	data, err := os.ReadFile(defaultBoardPath)
	if err != nil {
		panic(fmt.Sprintf("read %s: %v", defaultBoardPath, err))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		panic(fmt.Sprintf("parse %s: %v", defaultBoardPath, err))
	}
	return m
})

const (
	largeKeys     = 5_000
	largeAvgValue = 2 << 10 // ~2KB
	largeBlobs    = 4
	largeBlobSize = 64 << 10
	hugeKeys      = 50_000
)

var largeBoardMap = sync.OnceValue(func() map[string]json.RawMessage {
	return syntheticMap(largeKeys, largeAvgValue, largeBlobs, largeBlobSize, 1)
})

var hugeBoardMap = sync.OnceValue(func() map[string]json.RawMessage {
	return syntheticMap(hugeKeys, 48, 0, 0, 2)
})

// syntheticMap builds n keys whose values are JSON objects averaging
// roughly avg bytes, plus `blobs` values of blobSize bytes. Values are
// content envelopes ({"content":…,"filePath":…}) so the shape matches
// what the outfit loader actually puts on a board.
func syntheticMap(n, avg, blobs, blobSize int, seed int64) map[string]json.RawMessage {
	rng := rand.New(rand.NewSource(seed))
	m := make(map[string]json.RawMessage, n)
	for i := range n {
		size := avg
		if avg > 64 {
			// Vary between 0.25x and 1.75x the average.
			size = avg/4 + rng.Intn(3*avg/2)
		}
		m[syntheticKey(i)] = envelope(filler(rng, size), i)
	}
	for i := range blobs {
		m[fmt.Sprintf("blobs.blob%02d", i)] = envelope(filler(rng, blobSize), i)
	}
	return m
}

func syntheticKey(i int) string {
	return fmt.Sprintf("ns%02d.key%06d", i%50, i)
}

func envelope(body string, i int) json.RawMessage {
	b, err := json.Marshal(struct {
		Content  string `json:"content"`
		FilePath string `json:"filePath"`
	}{Content: body, FilePath: fmt.Sprintf("/home/figaro/synthetic/%06d.md", i)})
	if err != nil {
		panic(err)
	}
	return b
}

const fillerAlphabet = "abcdefghijklmnopqrstuvwxyz ABCDEFGHIJKLMNOPQRSTUVWXYZ 0123456789 \n"

func filler(rng *rand.Rand, size int) string {
	var sb strings.Builder
	sb.Grow(size)
	for sb.Len() < size {
		sb.WriteByte(fillerAlphabet[rng.Intn(len(fillerAlphabet))])
	}
	return sb.String()
}

// fixture couples a name with its map, so benchmarks can loop over the
// three boards uniformly.
type fixture struct {
	name string
	m    func() map[string]json.RawMessage
}

func fixtures() []fixture {
	return []fixture{
		{"default", defaultBoardMap},
		{"large", largeBoardMap},
		{"huge", hugeBoardMap},
	}
}

// board is the Snapshot for a fixture, built through the seam.
func (f fixture) board() form.Snapshot { return buildBoard(f.m()) }

// mutated returns a copy of the fixture map with the first `n` keys (in
// sorted order) carrying different values. n == -1 means every key.
func (f fixture) mutated(n int) map[string]json.RawMessage {
	src := f.m()
	out := make(map[string]json.RawMessage, len(src))
	keys := sortedKeys(src)
	changed := 0
	for _, k := range keys {
		v := src[k]
		if n < 0 || changed < n {
			out[k] = mutateValue(v)
			changed++
			continue
		}
		out[k] = v
	}
	return out
}

// sampleKeys returns up to n keys spread across the sorted key space,
// for lookup benchmarks.
func (f fixture) sampleKeys(n int) []string {
	keys := sortedKeys(f.m())
	if len(keys) <= n {
		return keys
	}
	out := make([]string, 0, n)
	stride := len(keys) / n
	for i := 0; i < len(keys) && len(out) < n; i += stride {
		out = append(out, keys[i])
	}
	return out
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// mutateValue returns a value that is byte-different from v but of
// comparable size, so Diff sees a real change without changing the
// board's memory profile.
func mutateValue(v json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(v, &s) == nil {
		b, _ := json.Marshal(s + "!")
		return b
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) == nil && len(obj) > 0 {
		obj["mutated"] = json.RawMessage(`true`)
		b, _ := json.Marshal(obj)
		return b
	}
	return json.RawMessage(`"mutated"`)
}
