// wedgescan looks for arias whose log ENDS in questions nobody answered: one
// or more input records after the last output the aria ever produced. That is
// the on-disk signature of an aria that accepts prompts and runs no turn.
//
// Read-only: it dials the running angelus and reads IR.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

type row struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Msgs     int    `json:"message_count"`
	LastLT   uint64 `json:"last_figaro_lt"`
	Mantra   string `json:"mantra"`
	LastSeen int64  `json:"last_active"`
}

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var rows []row
	if err := json.Unmarshal(raw, &rows); err != nil {
		panic(err)
	}
	ep := transport.UnixEndpoint(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "figaro", "angelus.sock"))
	cli, err := sdk.DialAngelus(ep)
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	type finding struct {
		id       string
		trailing int
		closed   bool
		asks     []string
		when     int64
	}
	var found []finding
	scanned := 0
	for _, r := range rows {
		if r.Kind != "conversation" || r.Msgs < 2 {
			continue
		}
		scanned++
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		// The tail is enough: a wedge is a suffix property.
		from := uint64(0)
		res, err := cli.IRBefore(ctx, r.ID, 0, 1<<62, 30)
		cancel()
		if err != nil || len(res.Entries) == 0 {
			continue
		}
		_ = from
		var trailing []string
		closed := false
		// Walk the tail backwards: count input prose records above the last
		// output. A tool_result is not a question; only prose from a sender is.
		for i := len(res.Entries) - 1; i >= 0; i-- {
			var m message.Message
			if json.Unmarshal(res.Entries[i].Payload, &m) != nil {
				continue
			}
			if m.Role == message.RoleOutput {
				break
			}
			isAsk, isClosed := false, false
			var text string
			for _, c := range m.Content {
				switch c.Type {
				case message.ContentProse:
					isAsk = true
					text = c.Text
				case message.ContentToolResult:
					if strings.Contains(c.Text, "closed without a result") {
						isClosed = true
					}
				}
			}
			if isClosed {
				closed = true
			}
			if isAsk {
				if len(text) > 70 {
					text = text[:70]
				}
				trailing = append(trailing, strings.ReplaceAll(text, "\n", " "))
			}
		}
		if len(trailing) >= 2 {
			found = append(found, finding{id: r.ID, trailing: len(trailing), closed: closed, asks: trailing, when: r.LastSeen})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].when > found[j].when })
	fmt.Printf("scanned %d conversations; %d end in %d+ unanswered questions\n", scanned, len(found), 2)
	for _, f := range found {
		fmt.Printf("\n%s  trailing=%d  toolClosed=%v  last_active=%s\n", f.id, f.trailing, f.closed,
			time.UnixMilli(f.when).Format("2006-01-02 15:04"))
		for i := len(f.asks) - 1; i >= 0; i-- {
			fmt.Printf("   ? %s\n", f.asks[i])
		}
	}
}
