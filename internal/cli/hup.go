package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

// hangupJSON carries the queue verbatim, so `figaro cut -j > lost.json` is a save.
type hangupJSON struct {
	Aria    string             `json:"aria"`
	Cleared bool               `json:"cleared"`
	Epoch   string             `json:"epoch,omitempty"`
	Queue   []rpc.QueuedPrompt `json:"queue"`
}

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
		out, merr := json.Marshal(hangupJSON{
			Aria:    resolvedID,
			Cleared: resp.Cleared,
			Epoch:   resp.Epoch,
			Queue:   queue,
		})
		if merr != nil {
			die("encode: %s", merr)
		}
		fmt.Fprintln(stdout, string(out))
		return
	}

	fmt.Fprintf(stdout, "%s %s\n", verbFor(disposition), resolvedID)
	// Say what happened to the queue even when empty: it is the only visible
	// difference between the verbs.
	switch {
	case resp.Cleared && len(queue) == 0:
		fmt.Fprintln(stdout, "  queue: nothing was waiting")
	case resp.Cleared:
		fmt.Fprintf(stdout, "  queue: %s discarded (re-run with -j to keep %s)\n",
			queueCount(queue), theyThem(len(queue)))
		for _, p := range queue {
			fmt.Fprintf(stdout, "    %d  %s\n", p.ID, queueRowText(p.Text))
		}
	case len(queue) == 0:
		fmt.Fprintln(stdout, "  queue: nothing was waiting")
	default:
		fmt.Fprintf(stdout, "  queue: %s kept, to be answered next\n", queueCount(queue))
		for _, p := range queue {
			fmt.Fprintf(stdout, "    %d  %s\n", p.ID, queueRowText(p.Text))
		}
	}
}

func verbFor(disposition rpc.QueueDisposition) string {
	if disposition == rpc.QueueClear {
		return "cut"
	}
	return "hup"
}

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

func queueRowText(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i] + " …"
		}
	}
	return s
}
