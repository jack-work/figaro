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

// BenchmarkTranslations is the ASSEMBLY read, and it is O(history) per send by
// design: the provider asks the log for the whole conversation and marshals
// it. What it costs is SLICE HEADERS, not payloads -- the rows are shared
// with the log, never copied -- which is the quantity to read off B/op.
//
// IT STOPS AT 10,000 BECAUSE THE FIXTURE IS QUADRATIC, NOT THE READ:
// MemLog.Append copies the entries slice AND REBUILDS byFigaroLT on every
// append, so building the log costs 121 ms at 1,000, 5.01 s at 10,000 and
// 2 m 20 s at 50,000 on this machine.
func BenchmarkTranslations(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			rows := benchRowLog(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				perMessage, lts := Translations(rows)
				if len(perMessage) != n || len(lts) != n {
					b.Fatalf("got %d rows, want %d", len(perMessage), n)
				}
			}
		})
	}
}
