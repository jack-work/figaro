package provider

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

func benchRowBody(i int) json.RawMessage {
	body, err := json.Marshal(map[string]any{
		"role": "user",
		"content": []map[string]any{{
			"type": "text",
			"text": "turn body " + strconv.Itoa(i) + " with enough text to be a plausible message on the wire",
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func benchRowLog(b *testing.B, n int) *store.MemLog[[]json.RawMessage] {
	b.Helper()
	rows := store.NewMemLog[[]json.RawMessage]()
	for i := 0; i < n; i++ {
		if _, err := rows.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: uint64(i + 1),
			Payload:  []json.RawMessage{benchRowBody(i)},
		}); err != nil {
			b.Fatal(err)
		}
	}
	return rows
}

// BenchmarkTranslations is the O(history) assembly read; B/op should be slice
// headers, not payloads. It stops at 10,000 because MemLog.Append is quadratic.
func BenchmarkTranslations(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			rows := benchRowLog(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				perMessage, lts := CollectRows(TranslationRows(rows, 0))
				if len(perMessage) != n || len(lts) != n {
					b.Fatalf("got %d rows, want %d", len(perMessage), n)
				}
			}
		})
	}
}
