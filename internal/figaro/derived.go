package figaro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// Derived is the per-figaro materialized-view actor. One inbox, one
// goroutine, one tick at a time. Reads durable streams; writes
// AriaMeta + TranslationMeta as derived statistics. Configured
// fields (label, model, etc.) live in chalkboard.json — Derived
// doesn't touch them.
type Derived struct {
	id           string
	providerName string
	backend      store.Backend
	figStream    store.Stream[message.Message]
	translator   store.Stream[[]json.RawMessage]

	inbox chan tick
	done  chan struct{}
}

type tick struct {
	figaroLT uint64
}

// NewDerived spawns the actor. Returns nil when backend is nil
// (ephemeral arias don't materialize anything).
func NewDerived(
	ctx context.Context,
	id, providerName string,
	backend store.Backend,
	figStream store.Stream[message.Message],
	translator store.Stream[[]json.RawMessage],
) *Derived {
	if backend == nil {
		return nil
	}
	d := &Derived{
		id:           id,
		providerName: providerName,
		backend:      backend,
		figStream:    figStream,
		translator:   translator,
		inbox:        make(chan tick, 1),
		done:         make(chan struct{}),
	}
	go d.run(ctx)
	return d
}

// Tick coalesces. If a tick is already pending, the new one is
// dropped — the loop catches the latest durable state when it
// processes the pending one.
func (d *Derived) Tick(figaroLT uint64) {
	if d == nil {
		return
	}
	select {
	case d.inbox <- tick{figaroLT: figaroLT}:
	default:
	}
}

// Wait blocks until the loop exits (after ctx cancellation).
func (d *Derived) Wait() {
	if d == nil {
		return
	}
	<-d.done
}

func (d *Derived) run(ctx context.Context) {
	defer close(d.done)
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-d.inbox:
			d.process(t)
		}
	}
}

func (d *Derived) process(t tick) {
	now := time.Now().UnixMilli()

	msgs := unwrapMessages(d.figStream.Durable())
	aria := store.AriaMeta{
		MessageCount: len(msgs),
		LastActiveMS: now,
		LastFigaroLT: t.figaroLT,
	}
	for _, m := range msgs {
		if m.Role == message.RoleAssistant {
			aria.TurnCount++
		}
		if m.Usage != nil {
			aria.TokensIn += m.Usage.InputTokens
			aria.TokensOut += m.Usage.OutputTokens
			aria.CacheReadTokens += m.Usage.CacheReadTokens
			aria.CacheWriteTokens += m.Usage.CacheWriteTokens
		}
	}
	if err := d.backend.SetMeta(d.id, &aria); err != nil {
		fmt.Fprintf(os.Stderr, "derived %s: aria meta write: %v\n", d.id, err)
	}

	if d.translator != nil && d.providerName != "" {
		entries := d.translator.Durable()
		var totalBytes int
		var fp string
		var lastLT uint64
		for _, e := range entries {
			for _, p := range e.Payload {
				totalBytes += len(p)
			}
			if e.Fingerprint != "" {
				fp = e.Fingerprint
			}
			if e.LT > lastLT {
				lastLT = e.LT
			}
		}
		tm := store.TranslationMeta{
			Provider:     d.providerName,
			EntryCount:   len(entries),
			TotalBytes:   totalBytes,
			Fingerprint:  fp,
			LastTransLT:  lastLT,
			LastUpdateMS: now,
		}
		if err := d.backend.SetTranslationMeta(d.id, d.providerName, &tm); err != nil {
			fmt.Fprintf(os.Stderr, "derived %s: translation meta write: %v\n", d.id, err)
		}
	}
}
