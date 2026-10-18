package angelus

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/handle"
)

// A sender is named the way a listing names it: the shortest handle that is
// unique in this store. A caller's own label is not an aria and gets no name,
// and neither does an aria this store does not hold, even when its words are
// those of one it does.
func TestASenderIsNamedAsTheListingNamesIt(t *testing.T) {
	h, _ := resolveHandlers(t)
	o1, o2 := "abcde123", "abcde987"
	long := "5a6b7c8d" + strings.Repeat("e", 24)
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{o1, o2, long}}
	a := h.angelus

	for _, c := range []struct{ sender, want string }{
		{long, handle.Words(long, 2)},
		{o1, handle.Words(o1, 3)},
		{"aria " + long, handle.Words(long, 2)},
		{"Jack", ""},
		{"5a6b7c8d" + strings.Repeat("0", 24), ""},
		{"", ""},
	} {
		if got := a.senderName(c.sender); got != c.want {
			t.Errorf("senderName(%q) = %q, want %q", c.sender, got, c.want)
		}
	}
}
