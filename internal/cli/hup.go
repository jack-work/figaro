package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

// The two hangups. They differ by ONE thing: what becomes of the messages
// queued behind the turn, and that is why they are two verbs rather than one
// verb with a negated flag:

// hangupJSON is the one object -j prints. It is the whole answer: which aria,
// whether a turn was actually stopped, what happened to the queue, and the
// queue itself: verbatim for `cut`, so `figaro cut -j > lost.json` is a save.
type hangupJSON struct {
	Aria    string             `json:"aria"`
	Cleared bool               `json:"cleared"`
	Epoch   string             `json:"epoch,omitempty"`
	Queue   []rpc.QueuedPrompt `json:"queue"`
}

// runHangup sends figaro.interrupt with an explicit queue disposition: the
// same RPC Ctrl-C fires inside a send stream. With no id, the pid-bound aria
// is used.
func runHangup(loaded *config.Loaded, ariaID string, disposition rpc.QueueDisposition, asJSON bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resolvedID, ep, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		die("%s", err)
	}

	fcli, derr := sdk.DialAria(ep, func(string, json.RawMessage) {})
	if derr != nil {
		die("connect figaro: %s", derr)
	}
	defer fcli.Close()

	resp, err := fcli.Hangup(ctx, disposition)
	if err != nil {
		die("%s %s: %s", verbFor(disposition), resolvedID, err)
	}

	queue := resp.Queue
	if queue == nil {
		queue = []rpc.QueuedPrompt{}
	}
	if asJSON {
		// Exactly one object on stdout, and nothing else.
		out, merr := json.Marshal(hangupJSON{
			Aria:    resolvedID,
			Cleared: resp.Cleared,
			Epoch:   resp.Epoch,
			Queue:   queue,
		})
		if merr != nil {
			die("encode: %s", merr)
		}
		fmt.Fprintln(os.Stdout, string(out))
		return
	}

	fmt.Printf("%s %s\n", verbFor(disposition), resolvedID)
	// Say what happened to the queue even when it is empty: the difference
	// between the two verbs is invisible otherwise, and a user who typed `cut`
	// deserves to see that it had nothing to discard rather than wonder.
	switch {
	case resp.Cleared && len(queue) == 0:
		fmt.Println("  queue: nothing was waiting")
	case resp.Cleared:
		fmt.Printf("  queue: %s discarded (re-run with -j to keep %s)\n",
			queueCount(queue), theyThem(len(queue)))
		for _, p := range queue {
			fmt.Printf("    %d  %s\n", p.ID, queueRowText(p.Text))
		}
	case len(queue) == 0:
		fmt.Println("  queue: nothing was waiting")
	default:
		fmt.Printf("  queue: %s kept, to be answered next\n", queueCount(queue))
		for _, p := range queue {
			fmt.Printf("    %d  %s\n", p.ID, queueRowText(p.Text))
		}
	}
}

func verbFor(disposition rpc.QueueDisposition) string {
	if disposition == rpc.QueueClear {
		return "cut"
	}
	return "hup"
}

// queueCount counts messages, and says when one of them is several folded
// together. "1 message kept" after queueing three is true and baffling; the
// fold is the reason, so the fold is named.
func queueCount(queue []rpc.QueuedPrompt) string {
	folded := 0
	for _, p := range queue {
		folded += len(p.Merged)
	}
	out := plural(len(queue), "message")
	if folded > 0 {
		out += fmt.Sprintf(" (folded from %d)", len(queue)+folded)
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func theyThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// queueRowText renders a queued message on one row: a combined message is
// several lines and the listing is a manifest, not a transcript.
func queueRowText(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i] + " …"
		}
	}
	return s
}
