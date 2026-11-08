package store

import (
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/tokens"
)

var metaHealFolded atomic.Int64

func MetaHealFolded() int64 { return metaHealFolded.Load() }

// healMeta advances the sidecar to the log tail if it lags. It runs from Open
// so the cost rides a read the caller was doing anyway; `list` never pays it.
func (b *XwalBackend) healMeta(ariaID string, log Log[message.Message]) {
	tail, ok := log.PeekTail()
	if !ok {
		return
	}
	c := b.metaCache(ariaID)
	c.mu.Lock() // the file write and the publish must stay in order
	defer c.mu.Unlock()
	st, err := c.loadOnce(b.metaPath(ariaID))
	if err != nil || st.Value == nil {
		return
	}
	// LastFigaroLT == 0 is a pre-watermark sidecar: its counts are absolute, so
	// folding from LT 1 would double them. Healing is a suffix, never a scan.
	if st.Value.LastFigaroLT == 0 || st.Value.LastFigaroLT >= tail.LT {
		return
	}
	meta := *st.Value
	folded := int64(0)
	for _, e := range log.ReadFrom(meta.LastFigaroLT+1, 0) {
		meta.Fold(e)
		folded++
	}
	metaHealFolded.Add(folded)
	if err := b.writeMetaLocked(ariaID, c, &meta); err != nil {
		return
	}
	slog.Info("meta healed", "aria", ariaID, "from", st.Value.LastFigaroLT, "to", meta.LastFigaroLT, "folded", folded)
}

func (meta *AriaMeta) Fold(e Entry[message.Message]) {
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
}

var metaIdentityHealed atomic.Int64

func MetaIdentityHealed() int64 { return metaIdentityHealed.Load() }

// healIdentity folds the bound form's identity keys into a sidecar below
// CurrentMetaVersion. Returns the upgraded copy, or nil; the write abandons
// when `from` was superseded while the bound form was folding.
func (b *XwalBackend) healIdentity(ariaID string, from *metaState, meta *AriaMeta) *AriaMeta {
	// Before the sidecar lock: FormState takes the store's own.
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

var mirroredIdentity = []struct {
	key          string
	field        func(*AriaMeta) *string
	keepOnRemove bool
}{
	{"mantra", func(m *AriaMeta) *string { return &m.Mantra }, false},
	{"system.cwd", func(m *AriaMeta) *string { return &m.Cwd }, false},
	{"system.provider", func(m *AriaMeta) *string { return &m.Provider }, true},
	{"system.model", func(m *AriaMeta) *string { return &m.Model }, true},
}

func (b *XwalBackend) identityCommitted(ariaID string, patch message.Patch, f *Form) {
	var touched []int
	for i, k := range mirroredIdentity {
		if patchReaches(patch, k.key) {
			touched = append(touched, i)
		}
	}
	if len(touched) == 0 {
		return
	}
	snap := f.Read().Snapshot
	if err := b.UpdateMeta(ariaID, func(m *AriaMeta) {
		for _, i := range touched {
			k := mirroredIdentity[i]
			v := snapString(snap, k.key)
			if v == "" && k.keepOnRemove {
				continue
			}
			*k.field(m) = v
		}
	}); err != nil {
		slog.Warn("identity: sidecar unwritable", "aria", ariaID, "err", err)
	}
}

func patchReaches(patch message.Patch, key string) bool {
	for _, e := range patch.Entries() {
		if e.Key == key || strings.HasPrefix(key, e.Key+".") || strings.HasPrefix(e.Key, key+".") {
			return true
		}
	}
	return false
}
