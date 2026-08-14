package store

// The libretto: a studied form's state, materialized, shared by every figaro
// observing it.
//
// One per studied FORM, not per observer (Gluck, 2026-08-12). Named
// `@libretto::<formid>` so a form can find its own libretto and an observer
// can derive the name from the id it studies. It does not fork, it is
// refcounted by the figaros studying it, and it holds a COPY rather than a
// pointer into the source's history (durable-forms §12.3): deriving a
// pointer is not derivation, and a range into someone else's log couples
// retention to observation through the back door.
//
// What the copy buys, in one line each: the translator never touches a
// source form; a studied form becomes freely deletable, because the libretto
// records the death and keeps the copy; and the render's special cases turn
// into ordinary state.
//
// It lives on a reserved STUMP for the same reason the topology form does -
// a stump is the one node figwal names by a string the caller chooses.
//
// WHAT THIS FILE IS NOT. It is the libretto and its fold, nothing else. The
// study verb's two-participant write (§12.2.1), the fork/import/kill
// refcount participants (§12.2.2) and the IR's per-libretto cursors (§12.5)
// are the wiring, they live in files another aria owns, and they are the
// next worker's job.

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// librettoPrefix names the reserved stump. An outfit stump is "@" plus a hex
// content hash, so a name carrying "::" cannot collide with one.
const librettoPrefix = "@libretto::"

// Bookkeeping keys. They are under system. because a whole-form mirror
// copies the source's keys verbatim into this document, and the source is a
// board whose ordinary keys are arbitrary strings. system.libretto.* is the
// one namespace a board cannot write (CheckWritable refuses it), which is
// what makes the collision impossible rather than unlikely.
const (
	KeyLibrettoRefs  = "system.libretto.refs"
	KeyLibrettoAlive = "system.libretto.alive"
	KeyLibrettoAt    = "system.libretto.at"
)

// LibrettoID is the reserved stump name for a studied form. Deterministic
// from the source id, which is the whole point: nothing has to be looked up
// and nothing has to be stored to find it.
func LibrettoID(sourceFormID string) string {
	return librettoPrefix + strings.TrimPrefix(sourceFormID, "@")
}

// SourceOfLibretto is the inverse, for the reconciliation sweep and for any
// listing that wants to explain a stump it did not create.
func SourceOfLibretto(librettoID string) (string, bool) {
	rest, ok := strings.CutPrefix(librettoID, librettoPrefix)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// Libretto is a derived form with exactly one source.
type Libretto struct {
	form *Form
	// source is the bare id, which names the stump (an unbound form's
	// sigil is stripped at open).
	source string

	mu   sync.Mutex
	sub  *Subscription
	stop chan struct{}
	done chan struct{}
}

// OpenLibretto mints the reserved stump if it is absent and replays the form.
// It does not begin following the source; see Follow.
func OpenLibretto(s *XwalStore, sourceFormID string) (*Libretto, error) {
	stump := LibrettoID(sourceFormID)
	if err := s.ensureStump(stump); err != nil {
		return nil, err
	}
	f, err := OpenForm(&stumpFormLog{store: s, stump: stump})
	if err != nil {
		return nil, err
	}
	l := &Libretto{form: f, source: strings.TrimPrefix(sourceFormID, "@")}
	// A fresh libretto is alive and unobserved. Written once, so a reader can
	// tell "never studied" from "studied and dropped" without inferring it
	// from an absence.
	if _, ok := l.formState().Get(KeyLibrettoAlive); !ok {
		if _, _, err := l.form.ApplyEffectPrivileged(librettoPatch(map[string]any{
			KeyLibrettoAlive: true,
			KeyLibrettoRefs:  0,
		}), 0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return l, nil
}

// formState is the published snapshot without its version.
func (l *Libretto) formState() form.Snapshot {
	snap, _ := l.form.Snapshot()
	return snap
}

// ID is the libretto's own form id (its stump name).
func (l *Libretto) ID() string { return LibrettoID(l.source) }

// Following reports whether this libretto is subscribed to its source. False
// means the source could not be opened -- deleted, most often -- and the copy
// is standing still, which is exactly what it should do.
func (l *Libretto) Following() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sub != nil
}

// State is the materialized copy, bookkeeping included.
func (l *Libretto) State() form.Snapshot { return l.formState() }

// Version is the libretto's own form version: the cursor an IR record
// stamps (durable-forms §12.5).
func (l *Libretto) Version() uint64 { return l.form.Read().Version }

// PatchesBetween is the copy's own patch view, (after, upTo], for the
// translator. It must come from THIS instance: a second Form over the same
// stump replays once and never hears the fold again, so its reader is
// orphaned at the version it opened at -- silently, and forever, because the
// per-LT cache makes whichever rendering ran first permanent.
func (l *Libretto) PatchesBetween(after, upTo uint64) []VersionedPatch {
	return l.form.PatchesBetween(after, upTo)
}

// Refs is how many figaros are studying the source.
func (l *Libretto) Refs() int { return intOf(l.formState(), KeyLibrettoRefs) }

// At is the source version last folded in.
func (l *Libretto) At() uint64 { return uint64(intOf(l.formState(), KeyLibrettoAt)) }

// Retain and Release move the refcount. They are ordinary privileged patches
// on this form's own actor, so they serialize against the fold rather than
// racing it -- which is what makes "refs reached zero" a fact rather than a
// sample.
func (l *Libretto) Retain() (int, error)  { return l.addRefs(1) }
func (l *Libretto) Release() (int, error) { return l.addRefs(-1) }

func (l *Libretto) addRefs(delta int) (int, error) {
	caller, _, _, _ := runtime.Caller(2)
	for {
		// ONE READ, NOT TWO. Form.Snapshot() publishes the state and its
		// version from a single atomic load; taking them separately lets a
		// writer land between the two, and then the count is computed from
		// the OLD state while the conditional apply quotes the NEW version --
		// so the guard passes and the update is lost.
		//
		// A lost retain over-counts (a leak the sweep repairs). A lost
		// release under-counts, and the count then drifts low until some
		// later, legitimate release finds zero and is refused. That is the
		// "release below zero" that appeared once under a loaded gate and in
		// none of the runs that chased it: the window between two atomic
		// loads is exactly as rare as that.
		at := l.form.Read()
		from := intOf(at.Snapshot, KeyLibrettoRefs)
		next := from + delta
		if next < 0 {
			// A double drop is a bug in the caller, not a reason to invent a
			// negative refcount that reclamation would never collect.
			//
			// THE LEDGER COMES WITH IT. This is an under-count, the direction
			// the sweep cannot repair, and it has appeared once in a loaded
			// run and never again in four. A message that says only "below
			// zero" leaves the next person doing what I did -- running it
			// again and learning nothing -- so the refusal carries the moves
			// that led to it.
			recordRefMove(refMove{libretto: l.ID(), delta: delta, from: from, to: next, refused: true, caller: caller})
			return from, fmt.Errorf(
				"libretto %s: release below zero\nrecent refcount moves (oldest first):\n%s",
				l.ID(), LibrettoLedger())
		}
		_, _, err := l.form.ApplyEffectPrivileged(
			librettoPatch(map[string]any{KeyLibrettoRefs: next}), at.Version)
		if err == nil {
			recordRefMove(refMove{libretto: l.ID(), delta: delta, from: from, to: next, caller: caller})
			return next, nil
		}
		if !errors.Is(err, ErrFormMoved) {
			return 0, err
		}
	}
}

// Reclaimable reports that nobody is STUDYING this any more. It is necessary
// for reclamation and it is NOT sufficient, which is worth stating where the
// method is rather than where the sweep is:
//
// an IR record stamps the libretto version it was rendered against (§12.5),
// so an aria that studied a form and dropped it still references this
// libretto for the whole of its history. Unlinking on refs==0 would make
// those records unrenderable -- the exact coupling the COPY was introduced
// to remove, reintroduced from the other end.
//
// So reclamation needs a second question, "does any surviving IR reference
// it", and that question has no answer in this package today. Until it does,
// this is the flag a caller may act on for a libretto whose observers are
// gone AND whose referencing arias are gone with them -- which today means
// only the delete path.
func (l *Libretto) Reclaimable() bool { return l.Refs() == 0 }

// Follow subscribes to the source and folds its patches in, forever, until
// Close. Register-then-read: the subscription carries the snapshot it was
// registered at, so nothing between the two is missed.
//
// The first fold writes the source's whole state, because a libretto that
// starts mid-history would render a form that never existed.
func (l *Libretto) Follow(src *Form) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sub != nil {
		return nil // already following
	}
	sub := src.SubscribeFrom(256)
	// Seed only when the copy is BEHIND. Re-attaching an already-current
	// libretto -- after a restart, or after a source came back -- must not
	// write the whole state again, or every boot appends a seed record per
	// libretto forever.
	if l.At() < sub.At {
		if err := l.seed(sub.Snap, sub.At); err != nil {
			sub.Close()
			return err
		}
	}
	l.sub = sub
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	go l.fold(sub, l.stop, l.done)
	return nil
}

// seed writes the source's whole state as one patch, plus the cursor.
func (l *Libretto) seed(snap form.Snapshot, at uint64) error {
	p := snap.AsPatch()
	set := make(map[string]json.RawMessage, len(p.Set)+1)
	for k, v := range p.Set {
		if isLibrettoKey(k) {
			continue // never mirror another libretto's bookkeeping
		}
		set[k] = v
	}
	raw, err := json.Marshal(at)
	if err != nil {
		return err
	}
	set[KeyLibrettoAt] = raw
	_, _, err = l.form.ApplyEffectPrivileged(message.Patch{Set: set}, 0)
	return err
}

// fold is the libretto's own consumer. It never blocks the source's writer:
// the subscription drops events for a reader that falls behind and says so,
// and the answer to a drop is to read the snapshot again rather than to make
// the writer wait.
func (l *Libretto) fold(sub *Subscription, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			// COALESCE. The copy is durable, so every fold is an fsync, and a
			// studied form under a burst would otherwise pay one per source
			// patch on top of its own. Whatever is already queued is applied
			// as ONE patch: the mirror's contract is the STATE, not the
			// number of records it took to get there, and the cursor is the
			// last version folded either way.
			batch := append(make([]Event, 0, 8), ev)
			for draining := true; draining; {
				select {
				case next, ok := <-sub.C:
					if !ok {
						draining = false
						break
					}
					batch = append(batch, next)
				default:
					draining = false
				}
			}
			if dead := l.applyBatch(sub, batch); dead {
				// THE SOURCE DIED, so stop listening -- wym.md:21, and the
				// half of it that was never built. A subscription outliving
				// its source pins that Form resident forever: the idle sweep
				// refuses to evict anything subscribed, correctly, so the
				// corpse of every studied-and-deleted form would be held for
				// the daemon's life.
				//
				// Detaching from THIS goroutine, so it cannot join itself:
				// Close() waits on `done`, and `done` is closed by our own
				// defer.
				l.detach(sub)
				return
			}
		}
	}
}

// detach ends the subscription from inside the fold. Following() reports
// false afterwards, which is what lets a later verb re-attach if a form with
// this id ever comes back.
func (l *Libretto) detach(sub *Subscription) {
	l.mu.Lock()
	if l.sub == sub {
		l.sub, l.stop, l.done = nil, nil, nil
	}
	l.mu.Unlock()
	sub.Close()
}

// applyBatch folds a run of source events into ONE patch on the copy.
//
// Later events win, per key, in both directions: a key set and then removed
// leaves a removal, and one removed and then set leaves a set. Getting that
// backwards is how a mirror ends up holding a value the source does not.
// It reports whether the source is DEAD, which ends the following.
func (l *Libretto) applyBatch(sub *Subscription, batch []Event) bool {
	set := map[string]json.RawMessage{}
	removed := map[string]bool{}
	var last uint64
	dead := false
	for _, ev := range batch {
		if ev.Missed > 0 {
			// The truthful recovery: re-seed from the source's current state.
			// A mirror that skips a patch is wrong forever and does not know
			// it, so the whole batch is abandoned for a fresh copy.
			if src := sub.Source(); src != nil {
				snap, at := src.Snapshot()
				_ = l.seed(snap, at)
			}
			return false
		}
		if ev.Version <= l.At() {
			continue // duplicate from the register-then-read window
		}
		for k, v := range ev.Applied.Set {
			if isLibrettoKey(k) {
				continue
			}
			set[k] = v
			delete(removed, k)
		}
		for _, k := range ev.Applied.Remove {
			if isLibrettoKey(k) {
				continue
			}
			removed[k] = true
			delete(set, k)
		}
		// A tombstone on the source is the death notice. The copy stays,
		// which is what makes a studied form deletable at all.
		if _, isDead := ev.Applied.Set[TombstoneKey]; isDead {
			dead = true
		}
		last = maxVersion(last, ev.Version)
	}
	if last == 0 {
		return false // nothing new
	}
	if dead {
		raw, _ := json.Marshal(false)
		set[KeyLibrettoAlive] = raw
	}
	raw, err := json.Marshal(last)
	if err != nil {
		return false
	}
	set[KeyLibrettoAt] = raw
	remove := make([]string, 0, len(removed))
	for k := range removed {
		remove = append(remove, k)
	}
	if _, _, err := l.form.ApplyEffectPrivileged(
		message.Patch{Set: set, Remove: remove}, 0); err != nil {
		// The death is not recorded, so do not stop listening on it: a
		// libretto that unsubscribed without writing alive=false would be
		// silently stale rather than truthfully dead.
		return false
	}
	return dead
}

func maxVersion(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}

// Close stops following and releases the form. The copy stays on disk.
func (l *Libretto) Close() {
	l.mu.Lock()
	sub, stop, done := l.sub, l.stop, l.done
	l.sub, l.stop, l.done = nil, nil, nil
	l.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	if sub != nil {
		sub.Close()
	}
	l.form.Close()
}

// isLibrettoKey names the keys a verbatim mirror must NOT copy.
//
// The bookkeeping namespace, because the copy would overwrite the refcount.
// And the TOMBSTONE, because a form seals itself when it carries one: mirror
// the source's death record and the libretto commits suicide the moment its
// source dies, which is the precise opposite of the copy outliving it. The
// death is recorded as system.libretto.alive instead, which is a fact about
// the source rather than an instruction about this form.
func isLibrettoKey(k string) bool {
	return strings.HasPrefix(k, "system.libretto.") || k == TombstoneKey
}

// HiddenLibrettoKey names the bookkeeping the MODEL must never see. The
// translator reads the libretto rather than the source, so the document's own
// machinery would otherwise fold into the studied block: system.libretto.at
// moves on every fold, and refs moves whenever some OTHER aria studies or
// drops the same form, which is cross-aria noise inside one aria's context.
//
// alive is deliberately not hidden. A dead source is reported in band, as a
// key transition like any other, which is what keeps liveness out of the
// projection entirely.
func HiddenLibrettoKey(k string) bool {
	return strings.HasPrefix(k, "system.libretto.") && k != KeyLibrettoAlive
}

func librettoPatch(kv map[string]any) message.Patch {
	set := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		raw, err := json.Marshal(v)
		if err != nil {
			continue
		}
		set[k] = raw
	}
	return message.Patch{Set: set}
}

func intOf(s form.Snapshot, key string) int {
	raw, ok := s.Get(key)
	if !ok {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0
	}
	return n
}
