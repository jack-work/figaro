// forkscan asks one question of a real store: for every fork pair, does the
// PARENT's log hold the question the CHILD asked first?
//
// Read-only. It dials the running angelus and reads IR; it writes nothing.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

type row struct {
	ID         string `json:"id"`
	Parent     string `json:"parent"`
	BranchedLT uint64 `json:"branched_lt"`
	Kind       string `json:"kind"`
}

// text decodes one IR entry's payload far enough to read what was said.
func text(raw json.RawMessage) (string, message.Role) {
	var m message.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", ""
	}
	var sb strings.Builder
	for _, c := range m.Content {
		sb.WriteString(c.Text)
	}
	return strings.TrimSpace(sb.String()), m.Role
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
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = "/run/user/1000"
	}
	ep := transport.UnixEndpoint(filepath.Join(rt, "figaro", "angelus.sock"))
	cli, err := sdk.DialAngelus(ep)
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	isAria := func(s string) bool {
		if len(s) != 8 {
			return false
		}
		for _, r := range s {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
		return true
	}

	pairs := 0
	hits := 0
	for _, r := range rows {
		if r.Kind != "conversation" || !isAria(r.Parent) {
			continue
		}
		pairs++
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		// The child's first own records: everything it wrote after the cut.
		child, err := cli.IR(ctx, r.ID, r.BranchedLT, 40)
		if err != nil {
			cancel()
			fmt.Printf("skip %s: %v\n", r.ID, err)
			continue
		}
		var firstAsk string
		for _, e := range child.Entries {
			t, role := text(e.Payload)
			if role == message.RoleInput && t != "" {
				firstAsk = t
				break
			}
		}
		if firstAsk == "" {
			cancel()
			continue
		}
		needle := firstAsk
		if len(needle) > 120 {
			needle = needle[:120]
		}
		// The parent, from the cut onward: the only place the child's words
		// could have landed.
		parent, err := cli.IRAll(ctx, r.Parent, r.BranchedLT)
		cancel()
		if err != nil {
			fmt.Printf("skip parent %s: %v\n", r.Parent, err)
			continue
		}
		for _, e := range parent.Entries {
			t, role := text(e.Payload)
			if strings.Contains(t, needle) {
				hits++
				fmt.Printf("HIT parent=%s child=%s branched_lt=%d parentLT=%d role=%v\n  ask=%q\n",
					r.Parent, r.ID, r.BranchedLT, e.LT, role, needle)
				break
			}
		}
	}
	fmt.Printf("pairs=%d hits=%d\n", pairs, hits)
}
