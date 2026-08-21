// toolgap dumps the tail of an aria's fig IR and of its cached anthropic
// translation, to see which side lost the tool_use.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jack-work/figaro/internal/figaro/wire"
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
	tl, terr := b.OpenTranslator(aria, "anthropic")
	if terr != nil {
		fmt.Println("translator:", terr)
	} else {
		tr := tl.Read()
		fmt.Printf("anthropic translation rows: %d\n", len(tr))
		for _, e := range tr[max(0, len(tr)-4):] {
			fmt.Printf("  row LT %d  (%d payload parts)\n", e.LT, len(e.Payload))
			for _, part := range e.Payload {
				s := string(part)
				if len(s) > 220 {
					s = s[:220] + "..."
				}
				fmt.Printf("      %s\n", s)
			}
		}
	}

	l, err := b.OpenFigIR(aria)
	if err != nil {
		panic(err)
	}
	all := l.Read()
	fmt.Printf("fig IR entries: %d\n", len(all))
	for _, e := range all[max(0, len(all)-4):] {
		m := e.Payload
		fmt.Printf("  LT %d role=%s\n", e.LT, m.Role)
		for _, c := range m.Content {
			raw, _ := json.Marshal(c)
			s := string(raw)
			if len(s) > 150 {
				s = s[:150]
			}
			kind := "?"
			for _, k := range []string{"tool_use", "tool_result", "text", "thinking"} {
				if strings.Contains(s, k) {
					kind = k
					break
				}
			}
			fmt.Printf("      %-11s %s\n", kind, s)
		}
	}
}
