package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

var errFollowedAriaGone = errors.New("the aria closed or the daemon went away")

func runReadFollow(loaded *config.Loaded, ref string, from uint64, asJSON bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	id, err := resolveReadTarget(ctx, acli, ref)
	if err != nil {
		return err
	}
	ep := ariaAddr(id)
	probe, err := sdk.DialAria(ep, nil)
	if err != nil {
		return err
	}
	snap, ferr := probe.Form(ctx)
	probe.Close()
	if ferr != nil {
		return fmt.Errorf("read %s: %w", id, ferr)
	}
	if !form.IsLog(snap.Snapshot) {
		return fmt.Errorf("read --follow follows an aria log, and %s is not one: use figaro listen to watch a conversation", id)
	}

	pokes := make(chan struct{}, 1)
	poke := func() {
		select {
		case pokes <- struct{}{}:
		default:
		}
	}
	f, err := follow(ctx, ep, func(method string, _ json.RawMessage) {
		if method == rpc.MethodAriaFrame || method == rpc.MethodTurnDone {
			poke()
		}
	}, func(*sdk.Aria) { poke() })
	if err != nil {
		return err
	}
	defer f.Close()

	if stdinIsAFeed() {
		go func() {
			io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}
	return followEntries(ctx, angelusEntries{acli}, id, from, pokes, f.Done(), stdout, asJSON)
}

func stdinIsAFeed() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice == 0
}

func followEntries(ctx context.Context, r entryReader, id string, from uint64, pokes <-chan struct{}, gone <-chan struct{}, w io.Writer, asJSON bool) error {
	next := from
	if next < 1 {
		next = 1
	}
	drain := func() error {
		entries, err := readEntries(ctx, r, id, next, 0)
		if err != nil {
			return err
		}
		if err := writeEntries(w, entries, asJSON); err != nil {
			return err
		}
		if n := len(entries); n > 0 {
			next = entries[n-1].Turn + 1
		}
		return nil
	}
	finish := func(err error) error {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if err := drain(); err != nil {
		return finish(err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-gone:
			if ctx.Err() != nil {
				return nil
			}
			if err := drain(); err != nil {
				return finish(errFollowedAriaGone)
			}
			return errFollowedAriaGone
		case <-pokes:
			if err := drain(); err != nil {
				return finish(err)
			}
		}
	}
}
