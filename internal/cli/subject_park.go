package cli

import (
	"time"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/mark"
)

// Hot trees: the arias this session has already shown, kept warm.
//
// A reader browsing a fork tree moves between a handful of arias and moves
// BACK. Retention (see plans/prefix-retention.md) makes the outward hop cheap
// by keeping the prefix two arias share; this keeps the whole of the aria that
// was on screen a moment ago, so the hop back costs one read of whatever
// arrived while we were away, and often nothing at all.
//
// THE LAW THAT MAKES A PARKED STORE SAFE IS THE ONE THAT MAKES RETENTION SAFE.
// History is append-only below the live edge and a fork point is sealed, so
// what a parked store holds is always a true PREFIX of the aria it names. It
// can be short, never wrong. Every path that adopts one therefore reads
// forward from what it holds and never reconciles.

// parkedSubjects is how many arias stay warm. The jumplist's own working set:
// a reader hopping a fork tree turns around inside three or four.
const parkedSubjects = 3

// parkedSubject is one aria as this session last had it: its store, the rows
// drawn from that store, and where the reader was standing.
type parkedSubject struct {
	id     string
	client *aria.Client
	rows   map[sliceKey]cachedMessage
	sticky map[sliceKey]stickyQuestion
	// expand and adorn are the fold states, both keyed by node reference: a
	// switch that carried one and not the other would open a block of the aria
	// it arrived at because the aria it left had that one open.
	expand map[nodeRef]bool
	adorn  map[nodeRef]bool
	from   aria.Anchor
	offset int
	follow bool
	at     time.Time
}

// messages is the size of what is parked, in the unit eviction works in.
func (p *parkedSubject) messages() int {
	if p == nil || p.client == nil {
		return 0
	}
	return p.client.Count()
}

// park takes the subject off the pager and puts it on the shelf.
//
// WHAT IS PARKED MUST BE INERT AND MUST NOT BE THE LIVE OBJECT. It is not the
// live object because the switch that follows mints its own client from this
// one (Client.CloneBelow) and its own cache maps (transcript.retarget) rather
// than editing these in place; the version that edited in place gave the shelf
// back half a parent and half its branch. Detach is the other half of the same
// rule: a copy on a shelf keeps no callbacks, so a fold that finds its way
// into it cannot reach a renderer that is showing something else by now.
func (t *transcript) park(id string) *parkedSubject {
	if id == "" || t.client == nil || t.client.Count() == 0 {
		return nil
	}
	t.client.Detach()
	return &parkedSubject{
		id: id, client: t.client,
		rows: t.rowCache, sticky: t.stickyCache, expand: t.expanded, adorn: t.adorned,
		from: t.from, offset: t.offset, follow: t.follow, at: time.Now(),
	}
}

// adopt puts a parked subject back on the pager. here says the window on
// screen already stands in the prefix this aria shares with the one being
// left, in which case it stays exactly where it is and the remembered position
// is discarded.
func (t *transcript) adopt(p *parkedSubject, status *sessionStatus, here bool, base int) {
	t.client = p.client
	if status != nil {
		t.status = status
	}
	t.rowCache, t.stickyCache = p.rows, p.sticky
	t.expanded, t.adorned = p.expand, p.adorn
	if t.adorned == nil {
		t.adorned = map[nodeRef]bool{}
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
	// A cue standing in the shared prefix points at the same node here; see
	// retarget, where the same rule keeps the gutter from blinking off under a
	// reader who has not moved.
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
	t.pendG, t.pendF = false, false
	t.prev = nil
	t.invalidateWindow()
	if t.active {
		t.client.SetClosedLimit(0)
	}
}

// parkSubject shelves the aria on screen under id. Caller holds the render
// lock.
func (in *interactiveInput) parkSubject(id string) {
	p := in.lt.tr.park(id)
	if p == nil {
		return
	}
	if in.parked == nil {
		in.parked = map[string]*parkedSubject{}
	}
	if _, dup := in.parked[id]; !dup {
		in.parkOrder = append(in.parkOrder, id)
	}
	in.parked[id] = p
	// THE BOUND IS A COUNT OF ARIAS, and the oldest visit goes first. The rows
	// are the per-aria cost; the prose itself is shared with whatever else
	// holds those nodes, because a Message carries a slice of them and Go
	// strings are immutable.
	for len(in.parkOrder) > parkedSubjects {
		oldest := in.parkOrder[0]
		in.parkOrder = in.parkOrder[1:]
		if gone := in.parked[oldest]; gone != nil {
			mark.Mark("hop.unpark", "aria", oldest, "msgs", gone.messages())
		}
		delete(in.parked, oldest)
	}
	// ONLY THE NEWEST PARKED ARIA KEEPS ITS ROWS. Measured, the rows of a
	// 40-turn aria cost five times the conversation in it: they are the
	// wrapped, decorated strings of every line. They are also the one thing on
	// the shelf that can be rebuilt from what is beside it, so they are what
	// the bound spends first. The aria a reader is most likely to return to is
	// the one they just left, and it keeps them.
	for _, older := range in.parkOrder[:max(len(in.parkOrder)-1, 0)] {
		if o := in.parked[older]; o != nil {
			o.rows, o.sticky = nil, nil
		}
	}
	mark.Mark("hop.park", "aria", id, "msgs", p.messages(), "held", len(in.parked))
}

// takeParked removes and returns the shelved copy of id, if there is one.
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

// dropParked forgets every shelved aria. The lineage epoch moving is the one
// thing that can make a parked store's coordinates untrue: the shape of the
// tree changed under it.
func (in *interactiveInput) dropParked() {
	in.parked = nil
	in.parkOrder = nil
}
