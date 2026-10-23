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

// Three deterministic bound forms: default is a real capture, large and huge are
// synthetic.

const defaultBoundFormPath = "testdata/bound-default.json"

var defaultBoundFormMap = sync.OnceValue(func() map[string]json.RawMessage {
	data, err := os.ReadFile(defaultBoundFormPath)
	if err != nil {
		panic(fmt.Sprintf("read %s: %v", defaultBoundFormPath, err))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		panic(fmt.Sprintf("parse %s: %v", defaultBoundFormPath, err))
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

var largeBoundFormMap = sync.OnceValue(func() map[string]json.RawMessage {
	return syntheticMap(largeKeys, largeAvgValue, largeBlobs, largeBlobSize, 1)
})

var hugeBoundFormMap = sync.OnceValue(func() map[string]json.RawMessage {
	return syntheticMap(hugeKeys, 48, 0, 0, 2)
})

// syntheticMap values are content envelopes, the shape the outfit loader
// puts on a bound form.
func syntheticMap(n, avg, blobs, blobSize int, seed int64) map[string]json.RawMessage {
	rng := rand.New(rand.NewSource(seed))
	m := make(map[string]json.RawMessage, n)
	for i := range n {
		size := avg
		if avg > 64 {
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

type fixture struct {
	name string
	m    func() map[string]json.RawMessage
}

func fixtures() []fixture {
	return []fixture{
		{"default", defaultBoundFormMap},
		{"large", largeBoundFormMap},
		{"huge", hugeBoundFormMap},
	}
}

func (f fixture) bound() form.Snapshot { return buildBoundForm(f.m()) }

// mutated changes the first n keys in sorted order; -1 means every key.
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

// mutateValue keeps the value's size comparable.
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
