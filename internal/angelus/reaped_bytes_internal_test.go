package angelus

import (
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

type unreadableSizes struct {
	store.Backend
	fail bool
}

func (u *unreadableSizes) OwnBytesOf(ids []string) (map[string]int64, error) {
	if u.fail {
		return nil, errors.New("xwal: store closing")
	}
	out := map[string]int64{}
	for _, id := range ids {
		out[id] = 100
	}
	return out, nil
}

func TestASizeTheStoreCouldNotReadIsMeasuredAgainNotCachedAsZero(t *testing.T) {
	be := &unreadableSizes{fail: true}
	a := &Angelus{Backend: be}
	groups := []reapedGroup{{root: "r", atMS: 1, nodes: []string{"r", "c"}}}
	if got := a.reapedGroupBytes(groups)["r"]; got != 0 {
		t.Fatalf("an unreadable store reported %d bytes", got)
	}
	be.fail = false
	if got := a.reapedGroupBytes(groups)["r"]; got != 200 {
		t.Fatalf("after the store reads again the group holds %d bytes, want 200: a failed read was cached", got)
	}
}
