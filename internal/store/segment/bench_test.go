package segment

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
)

// payloadFor returns a JSON-object payload of roughly the requested
// size, parameterized by i so each entry differs.
func payloadFor(i int, sizeClass string) []byte {
	switch sizeClass {
	case "small":
		return []byte(fmt.Sprintf(`{"i":%d,"name":"event"}`, i))
	case "medium":
		return []byte(fmt.Sprintf(
			`{"i":%d,"action":"tool_call","tool":"bash","args":["ls","-la","/var/log"],"caller":"agent-7","trace":"abcdef0123456789","tags":["fs","io","read"]}`,
			i))
	default:
		panic("unknown size class")
	}
}

func buildSegment(b *testing.B, codec SegmentCodec, sizeClass string, n int) *Segment {
	dir := b.TempDir()
	ext := codec.FileExt()
	path := filepath.Join(dir, "00000000000000000001"+ext)
	s, err := Create(path, codec, 1, 0)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := s.Append(payloadFor(i, sizeClass)); err != nil {
			b.Fatal(err)
		}
	}
	if err := s.Sync(); err != nil {
		b.Fatal(err)
	}
	return s
}

func benchRead(b *testing.B, codec SegmentCodec, sizeClass string) {
	const n = 1024
	s := buildSegment(b, codec, sizeClass, n)
	defer s.Close()
	rng := rand.New(rand.NewSource(1))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		idx := uint64(rng.Intn(n))
		if _, err := s.ReadIndex(idx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadBinarySmall(b *testing.B)  { benchRead(b, BinaryCodec{}, "small") }
func BenchmarkReadBinaryMedium(b *testing.B) { benchRead(b, BinaryCodec{}, "medium") }
func BenchmarkReadJSONLSmall(b *testing.B)   { benchRead(b, JSONLCodec{}, "small") }
func BenchmarkReadJSONLMedium(b *testing.B)  { benchRead(b, JSONLCodec{}, "medium") }
