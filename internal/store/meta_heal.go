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
	if err := writeJSON(b.metaPath(ariaID), &meta); err != nil {
		return
	}
	slog.Info("meta healed", "aria", ariaID, "from", st.Value.LastFigaroLT, "to", meta.LastFigaroLT, "folded", folded)
	c.state.Store(&metaState{Value: &meta})
}
