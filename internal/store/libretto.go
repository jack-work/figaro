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
	// source is the bare id, which names the stump; addr is the id as the
	// store addresses it, sigil and all. They differ for an unbound form.
	source string
	addr   string

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
	l := &Libretto{form: f, source: strings.TrimPrefix(sourceFormID, "@"), addr: sourceFormID}
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

// Source is the form id this libretto observes, as the store addresses it.
func (l *Libretto) Source() string {
	if l.addr != "" {
		return l.addr
	}
	return l.source
}

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
func (l *Libretto) Version() uint64 { return l.form.Version() }

// Refs is how many figaros are studying the source.
func (l *Libretto) Refs() int { return intOf(l.formState(), KeyLibrettoRefs) }

// Alive reports whether the source form still exists. A libretto outlives
// its source: the copy is still renderable, it simply stops moving.
func (l *Libretto) Alive() bool { return boolOf(l.formState(), KeyLibrettoAlive, true) }

// At is the source version last folded in.
func (l *Libretto) At() uint64 { return uint64(intOf(l.formState(), KeyLibrettoAt)) }

// Retain and Release move the refcount. They are ordinary privileged patches
// on this form's own actor, so they serialize against the fold rather than
// racing it -- which is what makes "refs reached zero" a fact rather than a
// sample.
func (l *Libretto) Retain() (int, error)  { return l.addRefs(1) }
func (l *Libretto) Release() (int, error) { return l.addRefs(-1) }

func (l *Libretto) addRefs(delta int) (int, error) {
	for {
		st := l.formState()
		version := l.form.Version()
		next := intOf(st, KeyLibrettoRefs) + delta
		if next < 0 {
			// A double drop is a bug in the caller, not a reason to invent a
			// negative refcount that reclamation would never collect.
			return intOf(st, KeyLibrettoRefs), fmt.Errorf(
				"libretto %s: release below zero", l.ID())
		}
		_, _, err := l.form.ApplyEffectPrivileged(
			librettoPatch(map[string]any{KeyLibrettoRefs: next}), version)
		if err == nil {
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
	if err := l.seed(sub.Snap, sub.At); err != nil {
		sub.Close()
		return err
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
			l.applyBatch(sub, batch)
		}
	}
}

// applyBatch folds a run of source events into ONE patch on the copy.
//
// Later events win, per key, in both directions: a key set and then removed
// leaves a removal, and one removed and then set leaves a set. Getting that
// backwards is how a mirror ends up holding a value the source does not.
func (l *Libretto) applyBatch(sub *Subscription, batch []Event) {
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
			return
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
		return // nothing new
	}
	if dead {
		raw, _ := json.Marshal(false)
		set[KeyLibrettoAlive] = raw
	}
	raw, err := json.Marshal(last)
	if err != nil {
		return
	}
	set[KeyLibrettoAt] = raw
	remove := make([]string, 0, len(removed))
	for k := range removed {
		remove = append(remove, k)
	}
	if _, _, err := l.form.ApplyEffectPrivileged(
		message.Patch{Set: set, Remove: remove}, 0); err != nil {
		return
	}
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

func boolOf(s form.Snapshot, key string, fallback bool) bool {
	raw, ok := s.Get(key)
	if !ok {
		return fallback
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return fallback
	}
	return b
}
