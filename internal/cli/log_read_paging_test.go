package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/rpc"
)

type daemonShapedLogReads struct {
	fakeLogReads
	cap int
}

func (f *daemonShapedLogReads) IRFrom(_ context.Context, _ string, from uint64) (*rpc.IRResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &rpc.IRResponse{}
	n := uint64(len(f.msgs))
	if from < 1 {
		from = 1
	}
	for lt := from; lt <= n && len(out.Entries) <= f.cap; lt++ {
		raw, _ := json.Marshal(f.msgs[lt-1])
		out.Entries = append(out.Entries, rpc.IREntry{LT: lt, Payload: raw})
	}
	if len(out.Entries) > f.cap {
		out.NextFrom = out.Entries[f.cap].LT
		out.Entries = out.Entries[:f.cap]
	}
	return out, nil
}

var _ interface {
	Read(context.Context, rpc.ReadRequest) (aria.Page, error)
} = (*daemonShapedLogReads)(nil)

func TestReadUntilPagesLikeTheDaemonDoes(t *testing.T) {
	f := &daemonShapedLogReads{cap: 3}
	for i := 1; i <= 10; i++ {
		f.msgs = append(f.msgs, entryMsg(uint64(i), "e"))
	}
	for _, c := range []struct{ from, until uint64 }{{1, 9}, {0, 9}, {2, 10}, {4, 9}} {
		entries, err := readEntries(context.Background(), f, "log", c.from, c.until)
		if err != nil {
			t.Fatal(err)
		}
		lo := c.from
		if lo < 1 {
			lo = 1
		}
		var got []uint64
		for _, e := range entries {
			got = append(got, e.Turn)
		}
		if uint64(len(got)) != c.until-lo {
			t.Fatalf("[%d,%d) read turns %v, want %d distinct turns", c.from, c.until, got, c.until-lo)
		}
		for i, turn := range got {
			if turn != lo+uint64(i) {
				t.Fatalf("[%d,%d) read turns %v, want %d..%d in order", c.from, c.until, got, lo, c.until-1)
			}
		}
	}
}
