package store

// Lazy self-healing for the _meta sidecar.

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

// healIdentity folds a board's identity keys into a sidecar written before
// the metadata-only dormant listing carried them, and stamps the version.
// Returns the upgraded copy, or nil to leave the caller's own.
//
// It rides the READ, because the reader that wants these fields is `list`,
// which deliberately opens no content (so the count healer on OpenFigIR
// cannot serve it). One aria pays one board fold, once, the first time
// anybody looks at it; an aria nobody lists pays nothing, ever.
//
// The stamp -- not the emptiness of the fields -- is the completion marker.
// The boot pass this replaces asked "are mantra, cwd and outfit all empty?",
// which cannot distinguish a sidecar that was never migrated from an aria
// that is genuinely blank, so a blank aria was re-folded at every boot for
// the life of the store.
// The caller passes the state it read FROM, because this healer is a write
// on the read path: between its board fold and its own publish, an ordinary
// SetMeta may have superseded what it based the upgrade on, and republishing
// then would put the memo back to the older value. It abandons instead --
// nothing is lost, because every SetMeta stamps, so the value that won is
// already current.
func (b *XwalBackend) healIdentity(ariaID string, from *metaState, meta *AriaMeta) *AriaMeta {
	// The board read happens BEFORE the sidecar lock: it opens the node and
	// takes the store's own lock, and a healer must never hold one to wait
	// for the other.
	snap, err := b.FormState(ariaID)
	if err != nil {
		// A board that cannot be read is not a migration that succeeded.
		// Leaving it unstamped costs one retry on the next read.
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
		return nil // somebody published while we folded; theirs is newer
	}
	if err := b.writeMetaLocked(ariaID, c, &up); err != nil {
		return nil
	}
	metaIdentityHealed.Add(1)
	return &up
}

