package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/term"
)

// CRUD on the queue: the messages an aria has accepted but not yet answered.

type queueJSON struct {
	Aria  string             `json:"aria"`
	Epoch string             `json:"epoch"`
	Queue []rpc.QueuedPrompt `json:"queue"`
}

type queueResultJSON struct {
	Aria    string            `json:"aria"`
	Epoch   string            `json:"epoch"`
	Results []rpc.QueueResult `json:"results"`
}

type queueSession struct {
	id   string
	cli  *sdk.Aria
	done func()
}

func openQueueSession(loaded *config.Loaded, ariaID string) (*queueSession, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	acli := mustConnectAngelus(loaded)
	resolvedID, ep, err := resolveFigaroTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		acli.Close()
		cancel()
		die("%s", err)
	}
	fcli, derr := sdk.DialAria(ep, func(string, json.RawMessage) {})
	if derr != nil {
		acli.Close()
		cancel()
		die("connect figaro: %s", derr)
	}
	return &queueSession{
		id:  resolvedID,
		cli: fcli,
		done: func() {
			fcli.Close()
			acli.Close()
		},
	}, ctx, cancel
}

func runQueueList(loaded *config.Loaded, ariaID string, asJSON bool) {
	s, ctx, cancel := openQueueSession(loaded, ariaID)
	defer cancel()
	defer s.done()

	resp, err := s.cli.QueuedAll(ctx)
	if err != nil {
		die("queue %s: %s", s.id, err)
	}
	queue := resp.Prompts
	if queue == nil {
		queue = []rpc.QueuedPrompt{}
	}

	if asJSON {
		out, merr := json.Marshal(queueJSON{Aria: s.id, Epoch: resp.Epoch, Queue: queue})
		if merr != nil {
			die("encode: %s", merr)
		}
		fmt.Fprintln(stdout, string(out))
		return
	}

	if len(queue) == 0 {
		fmt.Fprintf(stdout, "queue %s: empty\n", ariaName(s.id))
		return
	}
	fmt.Fprintf(stdout, "queue %s: %s waiting\n", ariaName(s.id), plural(len(queue), "message"))
	width := termWidth() - 24
	if width < 20 {
		width = 20
	}
	for _, p := range queue {
		text := queueRowText(p.Text)
		if p.Text == "" {
			text = term.Dim("(form only)")
		}
		if len(text) > width {
			text = text[:width] + "…"
		}
		age := ""
		if p.At > 0 {
			age = time.Since(time.UnixMilli(p.At)).Truncate(time.Second).String()
		}
		fmt.Fprintf(stdout, "  %-4d %-11s %-7s %s\n", p.ID, p.State, age, text)
		if len(p.Merged) > 0 {
			fmt.Fprintf(stdout, "       %s\n", term.Dim(fmt.Sprintf("folded in by an interrupt: %v", p.Merged)))
		}
	}
}

func runQueueRemove(loaded *config.Loaded, ariaID string, ids []uint64, all, asJSON bool) {
	s, ctx, cancel := openQueueSession(loaded, ariaID)
	defer cancel()
	defer s.done()

	req := rpc.QueueDeleteRequest{IDs: ids, All: all}
	if !all {
		// The mutation must name the epoch its ids came from.
		listed, err := s.cli.QueuedAll(ctx)
		if err != nil {
			die("queue rm %s: %s", s.id, err)
		}
		req.Epoch = listed.Epoch
	}

	resp, err := s.cli.DeleteQueued(ctx, req)
	if err != nil {
		die("queue rm %s: %s", s.id, err)
	}
	reportQueueResults(s.id, resp.Epoch, resp.Results, asJSON)
}

func runQueueEdit(loaded *config.Loaded, ariaID string, id uint64, text string, asJSON bool) {
	s, ctx, cancel := openQueueSession(loaded, ariaID)
	defer cancel()
	defer s.done()

	listed, err := s.cli.QueuedAll(ctx)
	if err != nil {
		die("queue edit %s: %s", s.id, err)
	}
	resp, err := s.cli.UpdateQueued(ctx, rpc.QueueUpdateRequest{
		Epoch: listed.Epoch, ID: id, Text: text,
	})
	if err != nil {
		die("queue edit %s: %s", s.id, err)
	}
	reportQueueResults(s.id, resp.Epoch, []rpc.QueueResult{resp.Result}, asJSON)
}

func reportQueueResults(ariaID, epoch string, results []rpc.QueueResult, asJSON bool) {
	if asJSON {
		out, err := json.Marshal(queueResultJSON{Aria: ariaID, Epoch: epoch, Results: results})
		if err != nil {
			die("encode: %s", err)
		}
		fmt.Fprintln(stdout, string(out))
	} else if len(results) == 0 {
		fmt.Fprintln(stdout, "nothing was queued")
	} else {
		for _, r := range results {
			switch r.Outcome {
			case rpc.QueueRejected:
				line := fmt.Sprintf("  %-4d rejected: %s", r.ID, r.Reason)
				if r.Detail != "" {
					line += ": " + r.Detail
				}
				if r.Into != 0 {
					line += fmt.Sprintf(" (try %d)", r.Into)
				}
				fmt.Fprintln(stderrw, line)
			default:
				fmt.Fprintf(stdout, "  %-4d %s\n", r.ID, r.Outcome)
			}
		}
	}
	for _, r := range results {
		if r.Outcome == rpc.QueueRejected {
			// exitNow, not exitProcess: the terminal-restore hooks
			// must run.
			exitNow(1)
			return
		}
	}
}

func parseQueueIDs(args []string) []uint64 {
	ids := make([]uint64, 0, len(args))
	for _, a := range args {
		n, err := strconv.ParseUint(a, 10, 64)
		if err != nil || n == 0 {
			dieUsage("queue rm: %q is not a queue id (ids are the numbers in `figaro queue`)", a)
		}
		ids = append(ids, n)
	}
	return ids
}

func queueEditText(args []string) string {
	for i, a := range args {
		if a == "--" {
			return strings.Join(args[i+1:], " ")
		}
	}
	return strings.Join(args, " ")
}
