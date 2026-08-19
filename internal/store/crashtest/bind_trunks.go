package crashtest

import (
	"errors"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/store/xwal"
)

func isNoChannel(err error) bool {
	return errors.Is(err, xwal.ErrNoChannel) ||
		(err != nil && strings.Contains(err.Error(), "xwal: no channel"))
}

const (
	flushInterval = 120 * time.Millisecond
	flushSlack    = 500 * time.Millisecond
)

func storeOptions() xwal.StoreOptions {
	return xwal.StoreOptions{
		Main:         chanMain,
		SyncInterval: flushInterval,
		Reducers:     map[string]xwal.Reducer{chanState: {Reduce: lastWins, Initial: []byte("{}")}},
		SegmentSize:  4096,
	}
}

func lastWins(_, patch []byte) ([]byte, error) {
	return append([]byte(nil), patch...), nil
}

// Known contract gaps of the bound implementation, kept green so the
// harness stays a regression net; each class is documented in FINDINGS.md
// with a replay seed.
func storeGaps(md verifyMode) map[string]bool {
	gaps := map[string]bool{
		vAppendLost: true,
	}
	if md == modeCorrupt {
		for _, c := range []string{vGap, vHeadCut, vMismatch, vOrphan, vAhead, vRead, vFold, vTrunkLost, vOpenFailed} {
			gaps[c] = true
		}
	}
	return gaps
}

func forkWreckGaps() []string {
	return nil
}

type trunksStore struct {
	s *xwal.Store
}

func createStore(dir string) (Store, string, error) {
	s, err := xwal.OpenStore(dir, storeOptions())
	if err != nil {
		return nil, "", err
	}
	id, err := s.SpawnUnderRoot()
	if err != nil {
		s.Close()
		return nil, "", err
	}
	return &trunksStore{s: s}, id, nil
}

func openStore(dir string) (Store, error) {
	s, err := xwal.OpenStore(dir, storeOptions())
	if err != nil {
		return nil, err
	}
	return &trunksStore{s: s}, nil
}

func (b *trunksStore) Trunks() ([]string, error) {
	infos := b.s.ListLight()
	out := make([]string, 0, len(infos))
	for _, ti := range infos {
		out = append(out, ti.ID)
	}
	return out, nil
}

func (b *trunksStore) AppendMain(trunk string, payload []byte) (uint64, error) {
	return b.s.Append(trunk, chanMain, 0, payload, nil)
}

func (b *trunksStore) AppendChannel(trunk, channel string, mainLT uint64, payload []byte) (uint64, error) {
	return b.s.Append(trunk, channel, mainLT, payload, nil)
}

func (b *trunksStore) Kick() {
	b.s.Kick()
}

func (b *trunksStore) ForkAt(trunk string, atMainLT uint64) (string, error) {
	return b.s.Fork(trunk, atMainLT)
}

func (b *trunksStore) Detach(trunk string) error {
	node, ok := b.s.HeadNode(trunk)
	if !ok {
		return nil // trunk vanished under us; nothing to detach
	}
	return b.s.Detach(node)
}

func (b *trunksStore) ReadAll(trunk, channel string) ([]Rec, error) {
	rs, err := b.s.RecordsFrom(trunk, channel, 0, 0)
	if isNoChannel(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Rec, 0, len(rs))
	for _, r := range rs {
		out = append(out, Rec{ChanLT: r.ChannelLT, MainLT: r.MainLT, Payload: append([]byte(nil), r.Payload...)})
	}
	return out, nil
}

func (b *trunksStore) TailRecord(trunk, channel string) (Rec, bool, error) {
	r, ok, err := b.s.LatestChannelRecord(trunk, channel, 0)
	if isNoChannel(err) {
		return Rec{}, false, nil
	}
	if err != nil || !ok {
		return Rec{}, false, err
	}
	return Rec{ChanLT: r.ChannelLT, MainLT: r.MainLT, Payload: append([]byte(nil), r.Payload...)}, true, nil
}

func (b *trunksStore) MainTail(trunk string) (uint64, error) {
	infos, err := b.s.Channels(trunk)
	if err != nil {
		return 0, err
	}
	for _, ci := range infos {
		if ci.Name == chanMain {
			return ci.Last, nil
		}
	}
	return 0, nil
}

func (b *trunksStore) State(trunk string) ([]byte, error) {
	infos, err := b.s.Channels(trunk)
	if err != nil {
		return nil, err
	}
	last := uint64(0)
	for _, ci := range infos {
		if ci.Name == chanState {
			last = ci.Last
		}
	}
	if last == 0 {
		return nil, nil
	}
	return b.s.StateAt(trunk, chanState, last)
}

func (b *trunksStore) Close() error {
	return b.s.Close()
}
