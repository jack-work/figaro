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
	return short, uniqueWords(m, id)
}

func uniqueWords(m idMatcher, id string) string {
	for n := handle.MinWords; n <= handle.MaxWords; n++ {
		w := handle.Words(id, n)
		ref, _ := handle.Parse(w)
		hits, mine := 0, false
		for _, c := range m.MatchID(ref.HexPrefix(), -1) {
			if ref.Matches(c) {
				hits++
				mine = mine || c == id
			}
		}
		if hits == 1 && mine {
			return w
		}
	}
	return ""
}

func (a *Angelus) senderName(sender string) string {
	id := strings.TrimPrefix(sender, rpc.AriaLabelPrefix)
	if a.Backend == nil || !rpc.IsHexID(id) || len(strings.TrimPrefix(id, "@")) < rpc.OldIDLen {
		return ""
	}
	return uniqueWords(a.Backend, id)
}
