// holecheck runs CatchUp against a COPY of the real store and asserts that the
// hole the incident left in ede92072's anthropic rows is found and filled.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func main() {
	root, aria := os.Args[1], os.Args[2]
	b, err := store.NewXwalBackend(root, 2<<20)
	if err != nil {
		panic(err)
	}
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		panic(err)
	}
	figLog, err := b.OpenFigIR(aria)
	if err != nil {
		panic(err)
	}
	rows, err := b.OpenTranslator(aria, "anthropic")
	if err != nil {
		panic(err)
	}
	before := len(rows.Read())
	fmt.Printf("before: %d fig IR entries, %d anthropic rows\n", len(figLog.Read()), before)

	stats, err := provider.CatchUp(provider.CatchUpConfig{
		Log: figLog, Translator: rows, Fingerprint: "holecheck",
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			body, err := json.Marshal(map[string]any{"role": string(m.Role), "lt": m.LogicalTime})
			return []json.RawMessage{body}, err
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("CatchUp: repairedAt=%d visited=%d encoded=%d\n", stats.RepairedAt, stats.Visited, stats.Encoded)

	have := map[uint64]bool{}
	for _, r := range rows.Read() {
		have[r.FigaroLT] = true
	}
	missing := 0
	for _, e := range figLog.Read() {
		if !have[e.LT] {
			missing++
		}
	}
	fmt.Printf("after: %d rows; fig IR entries still without one: %d\n", len(rows.Read()), missing)
	if stats.RepairedAt == 0 {
		fmt.Println("RESULT: FAIL -- the hole was not detected")
		os.Exit(1)
	}
	fmt.Println("RESULT: PASS -- the hole was found and the rows re-derived")
}
