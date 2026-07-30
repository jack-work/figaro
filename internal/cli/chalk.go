package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/chalk"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// runChalk is `figaro chalk <id>` — a live view of one aria's chalkboard.
//
// It is the smallest honest consumer of the chalkboard stream, and exists as
// much to prove the contract as to be useful: catch up to a versioned snapshot,
// follow figaro.chalk frames, re-catch-up on desync, and repaint from the
// key-scoped hook rather than on a timer. If this command is correct, a UI built
// the same way is correct.
//
// The keys a change touches are flashed for a moment, so a watcher sees WHAT
// moved rather than just that the board is different. That is the whole reason
// the hook reports keys instead of a bare signal.
func runChalk(loaded *config.Loaded, ariaID string, jsonOut bool, showSystem bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, sigCancel := signal.NotifyContext(ctx, os.Interrupt)
	defer sigCancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resolvedID, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false)
	if err != nil {
		die("%s", err)
	}

	view := &chalkView{id: resolvedID, jsonOut: jsonOut, showSystem: showSystem}
	client := chalk.New()

	// The desync path must not run on the notification goroutine: a catch-up is
	// an RPC, and issuing one from inside the notify callback would block the
	// reader that has to deliver its reply. Hand it to the main loop instead.
	resync := make(chan uint64, 1)
	client.OnDesync = func(since uint64) {
		select {
		case resync <- since:
		default: // one pending catch-up is as good as two
		}
	}
	client.OnChange = view.render

	var fcli *figaro.Client
	fcli, err = figaro.DialClient(ep, func(method string, params json.RawMessage) {
		if method != rpc.MethodChalkFrame {
			return
		}
		var f rpc.ChalkFrame
		if err := json.Unmarshal(params, &f); err != nil {
			return
		}
		client.Apply(f.Version, f.Patch)
	})
	if err != nil {
		die("connect to aria %s: %s", resolvedID, err)
	}
	defer fcli.Close()

	catchUp := func() {
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		defer rcancel()
		resp, err := fcli.Chalkboard(rctx)
		if err != nil {
			die("read chalkboard: %s", err)
		}
		client.Adopt(resp.Snapshot, resp.Version)
	}
	catchUp()

	for {
		select {
		case <-ctx.Done():
			return
		case <-fcli.Done():
			return
		case <-resync:
			catchUp()
		}
	}
}

// chalkView paints the board. Not safe for concurrent use; the client
// serializes OnChange, and the resync path re-enters through it too.
type chalkView struct {
	mu         sync.Mutex
	id         string
	jsonOut    bool
	showSystem bool
	first      bool
}

func (v *chalkView) render(changed []string, snap chalkboard.Snapshot) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.jsonOut {
		// One object per change, so the stream is greppable and pipe-friendly:
		// a consumer wants the delta, not a re-dump of the whole board.
		b, _ := json.Marshal(struct {
			Aria    string   `json:"aria_id"`
			Changed []string `json:"changed"`
			Board   any      `json:"board"`
		}{v.id, changed, jsonBoard(snap, v.showSystem)})
		fmt.Println(string(b))
		return
	}

	touched := map[string]bool{}
	for _, k := range changed {
		touched[k] = true
	}

	var keys []string
	for k := range snap.All() {
		if !v.showSystem && strings.HasPrefix(k, "system.") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	w := term.Width()
	if w <= 0 {
		w = 80
	}

	// Redraw in place. The board is small and bounded; a full repaint of a
	// dozen rows is cheaper than the bookkeeping to patch individual lines,
	// and the hook already told us which rows to mark.
	if v.first {
		fmt.Print("\x1b[H\x1b[2J")
	}
	v.first = true
	fmt.Printf("\x1b[H\x1b[2J%s\r\n\r\n", term.Dim(fmt.Sprintf("chalkboard %s · %d keys · ^C to stop", v.id, len(keys))))
	for _, k := range keys {
		raw, _ := snap.Get(k)
		line := fmt.Sprintf("  %-24s %s", k, oneLine(string(raw)))
		if len(line) > w {
			line = line[:w]
		}
		if touched[k] {
			fmt.Printf("\x1b[7m%s\x1b[0m\r\n", line) // reverse-video the row that moved
		} else {
			fmt.Printf("%s\r\n", line)
		}
	}
	// A removal has no row left to mark, so name it explicitly or the change is
	// invisible — the one case where "what changed" cannot be shown in place.
	var gone []string
	for _, k := range changed {
		if _, ok := snap.Get(k); !ok {
			gone = append(gone, k)
		}
	}
	if len(gone) > 0 {
		sort.Strings(gone)
		fmt.Printf("\r\n%s\r\n", term.Dim("removed: "+strings.Join(gone, ", ")))
	}
}

// jsonBoard is the board as a plain map, honouring the system filter.
func jsonBoard(snap chalkboard.Snapshot, showSystem bool) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range snap.All() {
		if !showSystem && strings.HasPrefix(k, "system.") {
			continue
		}
		out[k] = v
	}
	return out
}

// oneLine flattens a value to a single row. Every row must be one physical
// line: a multi-line value smuggling a newline through would desync the
// in-place repaint, which is the same invariant the incipit painter keeps.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
