package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// CRUD on the queue — the messages an aria has accepted but not yet answered.
//
// Create is `figaro send`: a queued message IS a submitted prompt, so there is
// no second door. What is here is read, update, delete.
//
// Every mutation is a READ-THEN-WRITE. Queue ids restart whenever the agent is
// rebuilt, so an id is only meaningful paired with the generation it was read
// from; the CLI reads the queue, takes that epoch, and hands it back with the
// mutation. If the agent restarted in between, the server refuses the whole
// request as stale and says so — rather than deleting whatever holds that
// number now.

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

// queueSession is one resolved aria plus its client, since every queue verb
// needs both and half of them need two round trips.
type queueSession struct {
	id   string
	cli  *figaro.Client
	done func()
}

func openQueueSession(loaded *config.Loaded, ariaID string) (*queueSession, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	acli := mustConnectAngelus(loaded)
	resolvedID, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		acli.Close()
		cancel()
		die("%s", err)
	}
	fcli, derr := figaro.DialClient(ep, func(string, json.RawMessage) {})
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

// runQueueList shows what the aria is going to be asked next.
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
		fmt.Fprintln(os.Stdout, string(out))
		return
	}

	if len(queue) == 0 {
		fmt.Printf("queue %s: empty\n", s.id)
		return
	}
	fmt.Printf("queue %s — %s waiting\n", s.id, plural(len(queue), "message"))
	width := termWidth() - 24
	if width < 20 {
		width = 20
	}
	for _, p := range queue {
		text := queueRowText(p.Text)
		if p.Text == "" {
			// A carrier renders as nothing; say what it is instead of
			// printing a blank row the user cannot act on.
			text = term.Dim("(form only)")
		}
		if len(text) > width {
			text = text[:width] + "…"
		}
		age := ""
		if p.At > 0 {
			age = time.Since(time.UnixMilli(p.At)).Truncate(time.Second).String()
		}
		fmt.Printf("  %-4d %-11s %-7s %s\n", p.ID, p.State, age, text)
		if len(p.Merged) > 0 {
			fmt.Printf("       %s\n", term.Dim(fmt.Sprintf("folded in by an interrupt: %v", p.Merged)))
		}
	}
}

// runQueueRemove asks the aria to drop queued messages.
func runQueueRemove(loaded *config.Loaded, ariaID string, ids []uint64, all, asJSON bool) {
	s, ctx, cancel := openQueueSession(loaded, ariaID)
	defer cancel()
	defer s.done()

	req := rpc.QueueDeleteRequest{IDs: ids, All: all}
	if !all {
		// Read first: the mutation must name the generation its ids came from.
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

// runQueueEdit rewrites one queued message.
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

// reportQueueResults prints one line per requested id and sets the exit code
// from the outcomes.
//
// A REFUSAL IS NOT AN ERROR AND IS NOT A SUCCESS. The server legitimately
// declines to delete a message it has already committed; that is an answer,
// it is printed in full, and it leaves a non-zero status so a script notices —
// but it is never dressed up as a transport failure, and it is never swallowed.
func reportQueueResults(ariaID, epoch string, results []rpc.QueueResult, asJSON bool) {
	if asJSON {
		out, err := json.Marshal(queueResultJSON{Aria: ariaID, Epoch: epoch, Results: results})
		if err != nil {
			die("encode: %s", err)
		}
		fmt.Fprintln(os.Stdout, string(out))
	} else if len(results) == 0 {
		fmt.Println("nothing was queued")
	} else {
		for _, r := range results {
			switch r.Outcome {
			case rpc.QueueRejected:
				line := fmt.Sprintf("  %-4d rejected: %s", r.ID, r.Reason)
				if r.Detail != "" {
					line += " — " + r.Detail
				}
				if r.Into != 0 {
					line += fmt.Sprintf(" (try %d)", r.Into)
				}
				fmt.Fprintln(os.Stderr, line)
			default:
				fmt.Printf("  %-4d %s\n", r.ID, r.Outcome)
			}
		}
	}
	for _, r := range results {
		if r.Outcome == rpc.QueueRejected {
			// Runtime outcome, not misuse: exit 1, never 2. Through exitNow,
			// not the raw exitProcess: os.Exit runs no defers, so every abrupt
			// exit owes the terminal-restore hooks a chance to run.
			exitNow(1)
			return
		}
	}
}

// parseQueueIDs turns the argv tail into ids, rejecting anything that is not
// one rather than guessing.
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

// queueEditText joins the replacement text, whether or not argv used `--`.
func queueEditText(args []string) string {
	for i, a := range args {
		if a == "--" {
			return strings.Join(args[i+1:], " ")
		}
	}
	return strings.Join(args, " ")
}
