package store

// Lazy self-healing for the _meta sidecar.

import (
	"log/slog"
	"sync/atomic"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/tokens"
)

// metaHealFolded counts entries folded by the healer, ever. It is the probe
// the bound test asserts on: healing an aria whose watermark trails by k must
// fold k entries, never the whole log.
var metaHealFolded atomic.Int64

// MetaHealFolded reports the number of IR entries the healer has folded.
func MetaHealFolded() int64 { return metaHealFolded.Load() }

// healMeta advances the sidecar to the log tail if it lags. Called from Open
// : the point where content is actually read: so the cost rides along with a
// read the caller was doing anyway, and `list` (Meta only) never pays it.
func (b *XwalBackend) healMeta(ariaID string, log Log[message.Message]) {
	tail, ok := log.PeekTail()
	if !ok {
		return
	}
	c := b.metaCache(ariaID)
	c.mu.Lock() // a writer: the file write and the publish must stay in order
	defer c.mu.Unlock()
	st, err := c.loadOnce(b.metaPath(ariaID))
	if err != nil || st.Value == nil {
		return
	}
	// LastFigaroLT == 0 means "no watermark" (a pre-watermark sidecar): the
	// counts are absolute, not resumable, so folding from LT 1 would double
	// them. Leave it to the owning agent; healing is a suffix, never a scan.
	if st.Value.LastFigaroLT == 0 || st.Value.LastFigaroLT >= tail.LT {
		return
	}
	meta := *st.Value
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
	if err := b.writeMetaLocked(ariaID, c, &meta); err != nil {
		return
	}
	slog.Info("meta healed", "aria", ariaID, "from", st.Value.LastFigaroLT, "to", meta.LastFigaroLT, "folded", folded)
}

// metaIdentityHealed counts sidecars the identity healer has upgraded, ever.
var metaIdentityHealed atomic.Int64

// MetaIdentityHealed reports how many sidecars were migrated on read.
func MetaIdentityHealed() int64 { return metaIdentityHealed.Load() }

// healIdentity folds a board's identity keys into a sidecar below
// CurrentMetaVersion and stamps it, on the read that wanted those fields.
// Returns the upgraded copy, or nil for the caller's own. `from` is the state
// it was read from: a write on the read path abandons when that state was
// superseded while the board was folding. See plans/lazy-store-passes.md.
func (b *XwalBackend) healIdentity(ariaID string, from *metaState, meta *AriaMeta) *AriaMeta {
	// Before the sidecar lock: this takes the store's own.
	snap, err := b.FormState(ariaID)
	if err != nil {
		return nil
	}
	get := func(key string) string { return snapString(snap, key) }
	up := *meta
	if up.Mantra == "" {
		up.Mantra = get("mantra")
	}
	if up.Cwd == "" {
		up.Cwd = get("system.cwd")
	}
	if up.OutfitName == "" {
		up.OutfitName = get(keyOutfitName)
		up.OutfitVersion = get(keyOutfitVer)
		if up.OutfitName == "" {
			up.OutfitName, up.OutfitVersion = get(keyLegacyName), get(keyLegacyVer)
		}
	}
	if up.Provider == "" {
		up.Provider = get("system.provider")
	}
	if up.Model == "" {
		up.Model = get("system.model")
	}

	c := b.metaCache(ariaID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Load() != from {
		return nil
	}
	if err := b.writeMetaLocked(ariaID, c, &up); err != nil {
		return nil
	}
	metaIdentityHealed.Add(1)
	return &up
}
