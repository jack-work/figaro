package store

// Lazy self-healing for the _meta sidecar.
//
// The sidecar is a checkpoint, not a mirror: the agent rewrites it at turn
// boundaries (Agent.publishMetadata), so a RUNNING aria is legitimately
// behind its log between checkpoints, and a crash mid-turn leaves it behind
// for good. AriaMeta.LastFigaroLT is the watermark that says how far the
// checkpoint got.
//
// healMeta closes that gap on the READ path only — no startup sweep, no
// background job. It folds exactly the entries after the watermark with the
// same rules as Agent.refreshMetrics, so the two paths cannot drift, and it
// rewrites the sidecar. When the watermark is already at the tail (212 of 213
// arias in the author's store) it does nothing at all.
//
// What it does NOT touch: mantra/cwd/outfit/provider/model (chalkboard- and
// agent-owned, unknowable from an IR suffix — metaBackfill owns those) and
// LastActiveMS (the IR carries no wall clock).

import (
	"log/slog"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tokens"
)

// metaHealFolded counts entries folded by the healer, ever. It is the probe
// the bound test asserts on: healing an aria whose watermark trails by k must
// fold k entries, never the whole log.
var metaHealFolded atomic.Int64

// MetaHealFolded reports the number of IR entries the healer has folded.
func MetaHealFolded() int64 { return metaHealFolded.Load() }

// healMeta advances the sidecar to the log tail if it lags. Called from Open
// — the point where content is actually read — so the cost rides along with a
// read the caller was doing anyway, and `list` (Meta only) never pays it.
func (b *XwalBackend) healMeta(ariaID string, log Log[message.Message]) {
	tail, ok := log.PeekTail()
	if !ok {
		return
	}
	c := b.metaCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := b.loadMetaLocked(ariaID, c); err != nil || c.value == nil {
		return
	}
	// LastFigaroLT == 0 means "no watermark" (a pre-watermark sidecar): the
	// counts are absolute, not resumable, so folding from LT 1 would double
	// them. Leave it to the owning agent; healing is a suffix, never a scan.
	if c.value.LastFigaroLT == 0 || c.value.LastFigaroLT >= tail.LT {
		return
	}
	meta := *c.value
	folded := int64(0)
	for _, e := range log.ReadFrom(meta.LastFigaroLT+1, 0) {
		m := e.Payload
		if m.Usage != nil {
			meta.TokensIn += m.Usage.InputTokens
			meta.TokensOut += m.Usage.OutputTokens
			meta.CacheReadTokens += m.Usage.CacheReadTokens
			meta.CacheWriteTokens += m.Usage.CacheWriteTokens
			meta.ContextTokens = tokens.ContextFromUsage(m.Usage)
			meta.ContextExact = true
		} else {
			meta.ContextTokens += tokens.EstimateMessage(m)
			meta.ContextExact = false
		}
		if !message.IsCeremonial(m) {
			meta.MessageCount++
		}
		if m.Role == message.RoleOutput {
			meta.TurnCount++
		}
		meta.LastFigaroLT = e.LT
		folded++
	}
	metaHealFolded.Add(folded)
	if err := writeJSON(b.metaPath(ariaID), &meta); err != nil {
		return
	}
	slog.Info("meta healed", "aria", ariaID, "from", c.value.LastFigaroLT, "to", meta.LastFigaroLT, "folded", folded)
	c.value = &meta
}
