package crashtest

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	vOpenFailed = "open-failed"
	vPanic      = "panic"
	vRead       = "read-failed"
	vGap        = "chanlt-gap"
	vHeadCut    = "head-cut"
	vChecksum   = "checksum"
	vMismatch   = "prefix-mismatch"
	vSyncedLost = "durable-lost"
	vAppendLost = "append-lost"
	vOrphan     = "orphan-related"
	vAhead      = "reducible-ahead"
	vFold       = "state-fold"
	vTrunkLost  = "trunk-lost"
	vForkLost   = "trunk-lost-mid-fork"
	vChild      = "child-error"
)

type violation struct {
	class  string
	trunk  string
	ch     string
	detail string
}

func (v violation) String() string {
	return fmt.Sprintf("[%s] trunk=%s chan=%s %s", v.class, v.trunk, v.ch, v.detail)
}

type entry struct {
	mainLT   uint64
	q        uint64
	hash     uint64
	stamp    int64
	g        string
	reported bool
}

type chanModel struct {
	base   uint64
	recs   []entry
	synced uint64
}

func (cm *chanModel) lastLT() uint64 {
	if len(cm.recs) == 0 {
		return 0
	}
	return cm.base + uint64(len(cm.recs)) - 1
}

type model struct {
	// lenient re-bases a channel instead of failing when the child numbers
	// above the model. Only modeCorrupt sets it: corrupting a shared prefix
	// retroactively invalidates every descendant's fork base, which is the
	// point of the mode. modeKill stays strict, and that strictness is what
	// caught every real fork-base defect.
	lenient         bool
	trunks          map[string]map[string]*chanModel
	baselineOnly    map[string]bool
	pendingFork     map[string]uint64
	closeBegunStamp int64
	closedClean     bool
}

func newModel() *model {
	return &model{
		trunks:       map[string]map[string]*chanModel{},
		baselineOnly: map[string]bool{},
		pendingFork:  map[string]uint64{},
	}
}

func (m *model) chanModel(trunk, ch string) *chanModel {
	tm := m.trunks[trunk]
	if tm == nil {
		tm = map[string]*chanModel{}
		m.trunks[trunk] = tm
	}
	cm := tm[ch]
	if cm == nil {
		cm = &chanModel{}
		tm[ch] = cm
	}
	return cm
}

func (m *model) apply(line string) error {
	f := strings.Fields(line)
	if len(f) == 0 {
		return fmt.Errorf("empty report line")
	}
	switch f[0] {
	case "ready":
		return nil
	case "a":
		if len(f) != 7 {
			return fmt.Errorf("bad append line %q", line)
		}
		trunk, ch := f[1], f[2]
		q, err1 := strconv.ParseUint(f[3], 10, 64)
		mlt, err2 := strconv.ParseUint(f[4], 10, 64)
		clt, err3 := strconv.ParseUint(f[5], 10, 64)
		stamp, err4 := strconv.ParseInt(f[6], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return fmt.Errorf("bad append line %q", line)
		}
		if m.baselineOnly[trunk] {
			return nil
		}
		cm := m.chanModel(trunk, ch)
		if cm.base == 0 && len(cm.recs) == 0 {
			cm.base = clt
		}
		if clt != cm.base+uint64(len(cm.recs)) {
			if !m.lenient {
				return fmt.Errorf("model divergence on %q: next chanLT %d, child reports %d",
					line, cm.base+uint64(len(cm.recs)), clt)
			}
			cm.base, cm.recs, cm.synced = clt, nil, 0
		}
		cm.recs = append(cm.recs, entry{mainLT: mlt, q: q, stamp: stamp, g: trunk, reported: true})
		return nil
	case "d":
		if len(f) != 4 {
			return fmt.Errorf("bad durable line %q", line)
		}
		trunk, ch := f[1], f[2]
		n, err := strconv.ParseUint(f[3], 10, 64)
		if err != nil {
			return fmt.Errorf("bad durable line %q", line)
		}
		if m.baselineOnly[trunk] {
			return nil
		}
		cm := m.chanModel(trunk, ch)
		if n > cm.synced {
			cm.synced = n
		}
		return nil
	case "cb":
		if len(f) != 2 {
			return fmt.Errorf("bad close-begin line %q", line)
		}
		stamp, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return fmt.Errorf("bad close-begin line %q", line)
		}
		m.closeBegunStamp = stamp
		return nil
	case "cd":
		m.closedClean = true
		return nil
	case "fb":
		if len(f) != 3 {
			return fmt.Errorf("bad fork-begin line %q", line)
		}
		at, err := strconv.ParseUint(f[2], 10, 64)
		if err != nil {
			return fmt.Errorf("bad fork-begin line %q", line)
		}
		m.pendingFork[f[1]] = at
		return nil
	case "fd":
		if len(f) != 4 {
			return fmt.Errorf("bad fork-done line %q", line)
		}
		delete(m.pendingFork, f[1])
		m.baselineOnly[f[3]] = true
		return nil
	default:
		return fmt.Errorf("unknown report line %q", line)
	}
}

type verifyMode int

const (
	modeKill verifyMode = iota
	modeCorrupt
)

func verify(dir string, m *model, salt string, md verifyMode, cutoff int64) (vs []violation) {
	defer func() {
		if r := recover(); r != nil {
			vs = append(vs, violation{class: vPanic, detail: fmt.Sprintf("%v", r)})
		}
	}()
	st, err := openStore(dir)
	if err != nil {
		return append(vs, violation{class: vOpenFailed, detail: err.Error()})
	}
	defer st.Close()

	live, err := st.Trunks()
	if err != nil {
		return append(vs, violation{class: vOpenFailed, detail: fmt.Sprintf("list: %v", err)})
	}
	liveSet := map[string]bool{}
	for _, tr := range live {
		liveSet[tr] = true
		if m.trunks[tr] == nil && !m.baselineOnly[tr] {
			m.baselineOnly[tr] = true
		}
	}
	for tr := range m.trunks {
		if !liveSet[tr] {
			if at, ok := m.pendingFork[tr]; ok {
				vs = append(vs, violation{class: vForkLost, trunk: tr,
					detail: fmt.Sprintf("trunk missing after reopen; fork at main LT %d was in flight", at)})
			} else {
				vs = append(vs, violation{class: vTrunkLost, trunk: tr, detail: "known trunk missing after reopen"})
			}
		}
	}

	for _, tr := range live {
		vs = append(vs, verifyTrunk(st, m, tr, salt, md, cutoff)...)
	}
	return vs
}

func verifyTrunk(st Store, m *model, trunk, salt string, md verifyMode, cutoff int64) (vs []violation) {
	mainLast := uint64(0)
	baseline := m.baselineOnly[trunk]
	for _, ch := range allChans {
		recs, err := st.ReadAll(trunk, ch)
		if err != nil {
			vs = append(vs, violation{class: vRead, trunk: trunk, ch: ch, detail: err.Error()})
			continue
		}
		contiguous := true
		for i, r := range recs {
			if r.ChanLT != recs[0].ChanLT+uint64(i) {
				vs = append(vs, violation{class: vGap, trunk: trunk, ch: ch,
					detail: fmt.Sprintf("record %d has chanLT %d, run starts at %d", i, r.ChanLT, recs[0].ChanLT)})
				contiguous = false
				break
			}
			if ch == chanMain && r.MainLT != r.ChanLT {
				vs = append(vs, violation{class: vMismatch, trunk: trunk, ch: ch,
					detail: fmt.Sprintf("main identity broken at %d: mainLT %d", r.ChanLT, r.MainLT)})
			}
		}
		if ch == chanMain && len(recs) > 0 {
			mainLast = recs[len(recs)-1].ChanLT
		}
		if len(recs) > 0 {
			tail := recs[len(recs)-1].MainLT
			if ch == chanNotes && tail > mainLast {
				vs = append(vs, violation{class: vOrphan, trunk: trunk, ch: ch,
					detail: fmt.Sprintf("related tail references main %d, main tail %d", tail, mainLast)})
			}
			// Contract (memory-first v0.8.1): reducible channels get one
			// turn of slack: a patch keyed mainTail+1 survives by design.
			if ch == chanState && tail > mainLast+1 {
				vs = append(vs, violation{class: vAhead, trunk: trunk, ch: ch,
					detail: fmt.Sprintf("reducible tail references main %d, main tail %d", tail, mainLast)})
			}
		}
		if baseline {
			for _, r := range recs {
				if p, ours, valid := decodePayload(r.Payload, salt); ours && !valid {
					vs = append(vs, violation{class: vChecksum, trunk: trunk, ch: ch,
						detail: fmt.Sprintf("chanLT %d bad checksum (g=%s q=%d)", r.ChanLT, p.G, p.Q)})
				}
			}
			continue
		}
		if contiguous {
			vs = append(vs, verifyModeled(trunk, ch, recs, m.chanModel(trunk, ch), salt, md, cutoff)...)
		}
	}
	if _, err := st.State(trunk); err != nil {
		vs = append(vs, violation{class: vFold, trunk: trunk, ch: chanState, detail: err.Error()})
	}
	return vs
}

func verifyModeled(trunk, ch string, recs []Rec, cm *chanModel, salt string, md verifyMode, cutoff int64) (vs []violation) {
	last := uint64(0)
	if len(recs) > 0 {
		last = recs[len(recs)-1].ChanLT
		if cm.base != 0 && recs[0].ChanLT != cm.base {
			vs = append(vs, violation{class: vHeadCut, trunk: trunk, ch: ch,
				detail: fmt.Sprintf("channel starts at %d, expected %d", recs[0].ChanLT, cm.base)})
			return vs
		}
	}
	if md == modeKill {
		// WHAT THE STORE SAID IT HAD WRITTEN, and nothing else. cm.synced is
		// the child's last durability receipt; the wall-clock cutoff is kept
		// only for a clean Close, which makes everything durable by contract.
		required := cm.synced
		if cutoff == math.MaxInt64 {
			required = cm.lastLT()
		}
		if last < required {
			vs = append(vs, violation{class: vSyncedLost, trunk: trunk, ch: ch,
				detail: fmt.Sprintf("durable through %d (baseline %d), tail is %d", required, cm.synced, last)})
		} else if last < cm.lastLT() {
			vs = append(vs, violation{class: vAppendLost, trunk: trunk, ch: ch,
				detail: fmt.Sprintf("appended through %d, survived %d (durable bound %d)", cm.lastLT(), last, required)})
		}
	}
	extras := 0
	for _, r := range recs {
		idx := int(r.ChanLT - cm.base)
		if idx >= len(cm.recs) {
			extras++
			p, ours, valid := decodePayload(r.Payload, salt)
			if !ours || !valid || p.G != trunk || p.C != ch || p.M != r.MainLT {
				vs = append(vs, violation{class: vChecksum, trunk: trunk, ch: ch,
					detail: fmt.Sprintf("unreported tail chanLT %d invalid: ours=%v valid=%v payload=%.80s",
						r.ChanLT, ours, valid, r.Payload)})
			}
			continue
		}
		e := cm.recs[idx]
		if !e.reported {
			if recHash(r.MainLT, r.Payload) != e.hash {
				vs = append(vs, violation{class: vMismatch, trunk: trunk, ch: ch,
					detail: fmt.Sprintf("baseline record at chanLT %d changed", r.ChanLT)})
			}
			continue
		}
		p, ours, valid := decodePayload(r.Payload, salt)
		if !ours || !valid {
			vs = append(vs, violation{class: vChecksum, trunk: trunk, ch: ch,
				detail: fmt.Sprintf("chanLT %d: ours=%v valid=%v payload=%.80s", r.ChanLT, ours, valid, r.Payload)})
			continue
		}
		if p.G != e.g || p.C != ch || p.Q != e.q || p.M != e.mainLT || r.MainLT != e.mainLT {
			vs = append(vs, violation{class: vMismatch, trunk: trunk, ch: ch,
				detail: fmt.Sprintf("chanLT %d: got g=%s c=%s q=%d m=%d/%d want g=%s q=%d m=%d",
					r.ChanLT, p.G, p.C, p.Q, p.M, r.MainLT, e.g, e.q, e.mainLT)})
		}
	}
	// A per-trunk single-threaded child can have at most one op completed
	// but not yet reported per channel when killed.
	if extras > 1 {
		vs = append(vs, violation{class: vMismatch, trunk: trunk, ch: ch,
			detail: fmt.Sprintf("%d unreported surplus records", extras)})
	}
	return vs
}

func reconcile(dir, salt string, md verifyMode) (*model, []violation) {
	m := newModel()
	m.lenient = md == modeCorrupt
	st, err := openStore(dir)
	if err != nil {
		return m, []violation{{class: vOpenFailed, detail: "reconcile: " + err.Error()}}
	}
	defer st.Close()
	trunks, err := st.Trunks()
	if err != nil {
		return m, []violation{{class: vOpenFailed, detail: "reconcile list: " + err.Error()}}
	}
	var vs []violation
	for _, tr := range trunks {
		for _, ch := range allChans {
			recs, err := st.ReadAll(tr, ch)
			if err != nil {
				vs = append(vs, violation{class: vRead, trunk: tr, ch: ch, detail: "reconcile: " + err.Error()})
				continue
			}
			cm := m.chanModel(tr, ch)
			for i, r := range recs {
				if i == 0 {
					cm.base = r.ChanLT
				} else if r.ChanLT != cm.base+uint64(i) {
					vs = append(vs, violation{class: vGap, trunk: tr, ch: ch,
						detail: fmt.Sprintf("reconcile: record %d has chanLT %d, base %d", i, r.ChanLT, cm.base)})
					break
				}
				cm.recs = append(cm.recs, entry{mainLT: r.MainLT, hash: recHash(r.MainLT, r.Payload)})
			}
			cm.synced = cm.lastLT()
		}
	}
	return m, vs
}
