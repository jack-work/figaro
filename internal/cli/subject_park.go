package cli

import (
	"time"

	"github.com/jack-work/figaro/internal/mark"
	"github.com/jack-work/figaro/sdk/aria"
)

// Parked arias: history is append-only below the live edge and a fork point is
// sealed, so a parked store is always a true prefix of its aria, short but never
// wrong; every path that adopts one reads forward and never reconciles.

const parkedSubjects = 3

type parkedSubject struct {
	id     string
	client *aria.Client
	rows   map[sliceKey]cachedMessage
	sticky map[sliceKey]stickyQuestion
	// Both fold states travel together, or a switch opens a block of the wrong
	// aria.
	expand map[nodeRef]bool
	adorn  map[nodeRef]bool
	from   aria.Anchor
	offset int
	follow bool
	at     time.Time
	bound  *formMirror
}

func (p *parkedSubject) messages() int {
	if p == nil || p.client == nil {
		return 0
	}
	return p.client.Count()
}

// What is parked must be inert and must not be the live object: the switch clones
// from it (Client.CloneBelow, transcript.retarget), and Detach drops callbacks so
// a late fold cannot reach a renderer showing something else.
func (t *transcript) park(id string) *parkedSubject {
	if id == "" || t.client == nil || t.client.Count() == 0 {
		return nil
	}
	t.client.Detach()
	return &parkedSubject{
		id: id, client: t.client,
		rows: t.rowCache, sticky: t.stickyCache, expand: t.expanded, adorn: t.metaOpen,
		from: t.from, offset: t.offset, follow: t.follow, at: time.Now(),
	}
}

// here: the window on screen already stands in the shared prefix, so the position
// stays and the remembered one is discarded.
func (t *transcript) adopt(p *parkedSubject, status *sessionStatus, here bool, base int) {
	t.client = p.client
	t.setStatus(status)
	t.rowCache, t.stickyCache = p.rows, p.sticky
	t.expanded, t.metaOpen = p.expand, p.adorn
	if t.metaOpen == nil {
		t.metaOpen = map[nodeRef]bool{}
	}
	if t.rowCache == nil {
		t.rowCache = map[sliceKey]cachedMessage{}
	}
	if t.stickyCache == nil {
		t.stickyCache = map[sliceKey]stickyQuestion{}
	}
	if t.expanded == nil {
		t.expanded = map[nodeRef]bool{}
	}
	if !here {
		t.from, t.offset, t.follow = p.from, p.offset, p.follow
	}
	t.kept = here || !t.follow
	t.tailTuned, t.tailWant = false, 0
	t.openMemo = openMemo{}
	// A cue in the shared prefix points at the same node here (see retarget).
	if !here || !t.selection.below(base) {
		t.selection = nodeSelection{}
	}
	if !here || !t.visual.below(base) {
		t.visual = visualSelection{}
	}
	t.index = lineIndex{}
	t.lineKey = t.lineKey[:0]
	t.frameRefs = t.frameRefs[:0]
	t.jump, t.jumpNote = nil, ""
	t.search = nil
	t.inSearch, t.query, t.matchQuery = false, "", ""
	t.pending = ""
	t.prev = nil
	t.invalidateWindow()
	if t.active {
		t.client.SetClosedLimit(0)
	}
}

// Caller holds the render lock.
func (in *interactiveInput) parkSubject(id string) {
	p := in.lt.tr.park(id)
	if p == nil {
		return
	}
	if in.parked == nil {
		in.parked = map[string]*parkedSubject{}
	}
	p.bound = in.bound
	if _, dup := in.parked[id]; !dup {
		in.parkOrder = append(in.parkOrder, id)
	}
	in.parked[id] = p
	// The bound is a count of arias; the oldest visit goes first.
	for len(in.parkOrder) > parkedSubjects {
		oldest := in.parkOrder[0]
		in.parkOrder = in.parkOrder[1:]
		if gone := in.parked[oldest]; gone != nil {
			mark.Mark("hop.unpark", "aria", oldest, "msgs", gone.messages())
		}
		delete(in.parked, oldest)
	}
	// Only the newest parked aria keeps its rows: they cost several times the
	// conversation and can be rebuilt from what is beside them.
	for _, older := range in.parkOrder[:max(len(in.parkOrder)-1, 0)] {
		if o := in.parked[older]; o != nil {
			o.rows, o.sticky = nil, nil
		}
	}
	mark.Mark("hop.park", "aria", id, "msgs", p.messages(), "held", len(in.parked))
}

// Caller holds the render lock.
func (in *interactiveInput) takeParked(id string) *parkedSubject {
	p := in.parked[id]
	if p == nil {
		return nil
	}
	delete(in.parked, id)
	for i, held := range in.parkOrder {
		if held == id {
			in.parkOrder = append(in.parkOrder[:i], in.parkOrder[i+1:]...)
			break
		}
	}
	return p
}

// A lineage epoch move is the one thing that makes a parked store's coordinates untrue.
func (in *interactiveInput) dropParked() {
	in.parked = nil
	in.parkOrder = nil
}
