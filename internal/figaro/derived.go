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
// goroutine, one tick processed at a time. After every condense /
// endTurn, the agent calls Tick(figLT); the loop coalesces redundant
// ticks (channel buffer 1, non-blocking send) and re-derives both
// the aria meta and the per-translator meta from durable state.
//
// Reads are concurrent-safe (FileStream's mutex serializes them
// against concurrent writes). Writes go through Backend.SetMeta /
// SetTranslationMeta which use atomic write-then-rename.
type Derived struct {
	id            string
	providerName  string
	backend       store.Backend
	figStream     store.Stream[message.Message]
	translator    store.Stream[[]json.RawMessage]
	configured    func() ConfiguredMeta // snapshot of agent-owned fields

	inbox chan tick
	done  chan struct{}
}

// ConfiguredMeta is the slice of AriaMeta the agent owns. Derived
// reads it via the configured() snapshot to avoid touching agent
// state directly.
type ConfiguredMeta struct {
	Provider string
	Model    string
	Cwd      string
	Root     string
	Label    string
}

type tick struct {
	figaroLT uint64 // watermark: durable state through this LT
}

// NewDerived spawns the actor. Returns nil when backend is nil
// (ephemeral arias don't materialize anything).
func NewDerived(
	ctx context.Context,
	id, providerName string,
	backend store.Backend,
	figStream store.Stream[message.Message],
	translator store.Stream[[]json.RawMessage],
	configured func() ConfiguredMeta,
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
		configured:   configured,
		inbox:        make(chan tick, 1),
		done:         make(chan struct{}),
	}
	go d.run(ctx)
	return d
}

// Tick coalesces. If a tick is already pending, the new one is
// dropped (the loop will pick up the latest durable state when it
// processes the pending one).
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
	cfg := d.configured()
	now := time.Now().UnixMilli()

	msgs := unwrapMessages(d.figStream.Durable())
	aria := store.AriaMeta{
		Provider:     cfg.Provider,
		Model:        cfg.Model,
		Cwd:          cfg.Cwd,
		Root:         cfg.Root,
		Label:        cfg.Label,
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
