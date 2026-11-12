package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// countedRows counts the rows a reader is handed, whichever way it asks.
type countedRows struct {
	store.Log[[]json.RawMessage]
	handed *int
}

func (c countedRows) PeekTail() (store.Entry[[]json.RawMessage], bool) {
	e, ok := c.Log.PeekTail()
	if ok {
		*c.handed++
	}
	return e, ok
}

func (c countedRows) Lookup(figaroLT uint64) (store.Entry[[]json.RawMessage], bool) {
	e, ok := c.Log.Lookup(figaroLT)
	if ok {
		*c.handed++
	}
	return e, ok
}

func (c countedRows) ReadPage(from, before uint64, n int) ([]store.Entry[[]json.RawMessage], int) {
	page, total := c.Log.ReadPage(from, before, n)
	*c.handed += len(page)
	return page, total
}

func (c countedRows) ReadFrom(figaroLT uint64, n int) []store.Entry[[]json.RawMessage] {
	out := c.Log.ReadFrom(figaroLT, n)
	*c.handed += len(out)
	return out
}

func (c countedRows) Read() []store.Entry[[]json.RawMessage] {
	out := c.Log.Read()
	*c.handed += len(out)
	return out
}

// What the write path pays the first time it encodes for an aria: OnAppend
// seeds the deriver under its lock, from the rows and the log. One iteration
// is one seeding plus one trivial encode, over xwal-backed logs whose form
// changes every other record. The tail is the user's row in the first case
// and the assistant's in the others, where the seed has to step back a row.
// rows/op is how many rows the seeding was handed.
func BenchmarkFirstEncodeSeedsTheDeriver(b *testing.B) {
	for _, records := range []int{241, 242, 2000} {
		b.Run(fmt.Sprintf("records=%d", records), func(b *testing.B) {
			backend, err := store.NewXwalBackend(b.TempDir(), 4096)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { backend.Close() })
			f := &batchFixture{backend: backend}
			outfit, err := backend.CreateOutfit("bench", form.Build(form.Snapshot{}, map[string]json.RawMessage{
				"skills.x": json.RawMessage(`1`),
			}, nil))
			if err != nil {
				b.Fatal(err)
			}
			if f.aria, err = backend.CreateConversation(outfit); err != nil {
				b.Fatal(err)
			}
			if f.log, err = backend.OpenFigIR(f.aria); err != nil {
				b.Fatal(err)
			}
			for i := 0; i < records; i++ {
				if i%2 == 0 {
					if _, err := backend.ApplyForm(f.aria, setKey("mantra", fmt.Sprintf("before record %d", i))); err != nil {
						b.Fatal(err)
					}
				}
				role := message.RoleInput
				if i%2 == 1 {
					role = message.RoleOutput
				}
				if _, err := f.log.Append(store.Entry[message.Message]{Payload: message.Message{
					Role:      role,
					Content:   []message.Content{message.TextContent(fmt.Sprintf("record %d %s", i, strings.Repeat("x", (i*37)%400)))},
					Timestamp: int64(1000 + i),
				}}); err != nil {
					b.Fatal(err)
				}
			}
			encode := func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
				if msg.Timestamp%7 == 0 {
					return nil, nil
				}
				return []json.RawMessage{json.RawMessage(`{"role":"` + string(msg.Role) + `"}`)}, nil
			}
			rows, err := backend.OpenTranslator(f.aria, batchedChannel)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := CatchUp(CatchUpConfig{
				Log: f.log, Translator: rows, Form: boundFormOf{f}, Fingerprint: "test/v1", Provider: batchedChannel, Encode: encode,
			}); err != nil {
				b.Fatal(err)
			}
			tail, ok := f.log.PeekTail()
			if !ok {
				b.Fatal("empty log")
			}
			if row, ok := rows.PeekTail(); !ok || len(row.Payload) == 0 || row.FigaroLT != tail.LT {
				b.Fatalf("the newest row must carry bytes for the newest record: %+v", row)
			}
			handed := 0
			on := NewOnAppend(batchedChannel, encode,
				func() string { return "test/v1" },
				func(string) Form { return boundFormOf{f} },
				func(string) map[string]Form { return nil },
				func(aria string) (store.Log[[]json.RawMessage], error) {
					opened, err := backend.OpenTranslator(aria, batchedChannel)
					return countedRows{Log: opened, handed: &handed}, err
				},
			)
			b.ReportAllocs()
			b.ResetTimer()
			handed = 0
			for i := 0; i < b.N; i++ {
				on.Forget(f.aria)
				if _, _, err := on.EncodeEntry(f.aria, f.log, tail); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(handed)/float64(b.N), "rows/op")
		})
	}
}
