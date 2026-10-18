package angelus

import (
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

type idMatcher interface {
	MatchID(prefix string, limit int) []string
}

func (h *handlers) fillNames(rows []rpc.FigaroInfoResponse) {
	if h.angelus.Backend == nil {
		return
	}
	for i := range rows {
		if rows[i].Node == "" {
			rows[i].Short, rows[i].Handle = names(h.angelus.Backend, rows[i].ID)
		}
	}
}

func names(m idMatcher, id string) (short, words string) {
	if !rpc.IsHexID(id) {
		return "", ""
	}
	sigil := len(id) - len(strings.TrimPrefix(id, "@"))
	if len(id)-sigil < rpc.OldIDLen {
		return "", ""
	}
	short = id
	for k := sigil + rpc.ShortIDLen; k < len(id); k++ {
		if k-sigil == rpc.OldIDLen && len(id)-sigil > rpc.OldIDLen {
			continue
		}
		if len(m.MatchID(id[:k], 2)) == 1 {
			short = id[:k]
			break
		}
	}
	for n := handle.MinWords; n <= handle.MaxWords; n++ {
		w := handle.Words(id, n)
		ref, _ := handle.Parse(w)
		hits := 0
		for _, c := range m.MatchID(ref.HexPrefix(), -1) {
			if ref.Matches(c) {
				hits++
			}
		}
		if hits == 1 {
			return short, w
		}
	}
	return short, ""
}
