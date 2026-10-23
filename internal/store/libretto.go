package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// librettoPrefix carries "::", which an outfit version ("@" + a hex hash) cannot.
const librettoPrefix = "@libretto::"

// Under system. because a whole-form mirror copies the source's keys verbatim;
// system.libretto.* is the one namespace a form cannot write (CheckWritable
// refuses it), which makes a collision impossible rather than unlikely.
const (
	KeyLibrettoRefs  = "system.libretto.refs"
	KeyLibrettoAlive = "system.libretto.alive"
	KeyLibrettoAt    = "system.libretto.at"
)

// LibrettoID is deterministic from the source id: nothing is looked up or
// stored to find it.
func LibrettoID(sourceFormID string) string {
	return librettoPrefix + strings.TrimPrefix(sourceFormID, "@")
}

func SourceOfLibretto(librettoID string) (string, bool) {
	rest, ok := strings.CutPrefix(librettoID, librettoPrefix)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

type Libretto struct {
	form *Form
	// source is the bare id (sigil stripped), which names the stump.
	source string

	mu   sync.Mutex
	sub  *Subscription
	stop chan struct{}
	done chan struct{}
}

// OpenLibretto does not begin following the source; see Follow.
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
	// Written once, so a reader can tell never-studied from studied-and-dropped
	// without inferring it from an absence.
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

func (l *Libretto) formState() form.Snapshot {
	snap, _ := l.form.Snapshot()
	return snap
}

func (l *Libretto) ID() string { return LibrettoID(l.source) }

// Following false means the source could not be opened (deleted, most often)
// and the copy stands still.
func (l *Libretto) Following() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sub != nil
}

func (l *Libretto) State() form.Snapshot { return l.formState() }

// Version is the cursor an IR record stamps.
func (l *Libretto) Version() uint64 { return l.form.Read().Version }

// PatchesBetween must come from this instance: a second Form over the same
// stump replays once and never hears the fold again, and the per-LT cache
// makes whichever rendering ran first permanent.
func (l *Libretto) PatchesBetween(after, upTo uint64) []VersionedPatch {
	return l.form.PatchesBetween(after, upTo)
}

// At is the source version last folded in.
func (l *Libretto) At() uint64 { return uint64(intOf(l.formState(), KeyLibrettoAt)) }

// Retain and Release move the backref set idempotently, so a repeated retain
// or a lost release cannot drift the way an increment could. Privileged
// patches on this form's own actor, so they serialize against the fold.
func (l *Libretto) Retain(observer string) (int, error)  { return l.moveRef(observer, true) }
func (l *Libretto) Release(observer string) (int, error) { return l.moveRef(observer, false) }

func (l *Libretto) Refs() int { return len(refsOf(l.formState())) }

func (l *Libretto) RefSet() []string { return refsOf(l.formState()) }

func (l *Libretto) moveRef(observer string, add bool) (int, error) {
	observer = strings.TrimPrefix(observer, "@")
	if observer == "" {
		return l.Refs(), fmt.Errorf("libretto %s: a ref move needs an observer", l.ID())
	}
	for {
		at := l.form.Read()
		from := refsOf(at.Snapshot)
		next, changed := withRef(from, observer, add)
		if !changed {
			return len(from), nil
		}
		_, _, err := l.form.ApplyEffectPrivileged(
			librettoPatch(map[string]any{KeyLibrettoRefs: next}), at.Version)
		if err == nil {
			return len(next), nil
		}
		if !errors.Is(err, ErrFormMoved) {
			return 0, err
		}
	}
}

// withRef keeps the set sorted so the durable value does not depend on arrival order.
func withRef(set []string, observer string, add bool) ([]string, bool) {
	i, found := slices.BinarySearch(set, observer)
	switch {
	case add && found, !add && !found:
		return set, false
	case add:
		return slices.Insert(slices.Clone(set), i, observer), true
	default:
		return slices.Delete(slices.Clone(set), i, i+1), true
	}
}

// SetRefs replaces the whole set: the reconciliation pass knows better than
// the value it replaces.
func (l *Libretto) SetRefs(observers []string) error {
	next := slices.Clone(observers)
	for i := range next {
		next[i] = strings.TrimPrefix(next[i], "@")
	}
	slices.Sort(next)
	next = slices.Compact(next)
	_, _, err := l.form.ApplyEffectPrivileged(
		librettoPatch(map[string]any{KeyLibrettoRefs: next}), 0)
	return err
}

// Reclaimable is necessary for reclamation, not sufficient.
func (l *Libretto) Reclaimable() bool { return l.Refs() == 0 }

// Follow subscribes to the source and folds its patches in until Close.
func (l *Libretto) Follow(src *Form) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sub != nil {
		return nil // already following
	}
	sub := src.SubscribeFrom(256)
	// Seed only when the copy is behind: re-attaching a current libretto after
	// a restart must not write the whole state again, or every boot appends a
	// seed record per libretto.
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

func (l *Libretto) seed(snap form.Snapshot, at uint64) error {
	entries := snap.AsPatch().Entries()
	set := make(map[string]json.RawMessage, len(entries)+1)
	for _, ent := range entries {
		k, v := ent.Key, ent.New
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
	_, _, err = l.form.ApplyEffectPrivileged(form.Build(form.Snapshot{}, set, nil), 0)
	return err
}

// fold never blocks the source's writer: the subscription drops events for a
// reader that falls behind, and the answer to a drop is to re-read the snapshot.
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
			// Coalesce: every fold is an fsync, so whatever is queued is applied
			// as one patch. The mirror's contract is the state, not the record
			// count; the cursor is the last version folded either way.
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
				// The source died: stop listening, or the subscription pins that
				// Form resident forever (the idle sweep refuses to evict anything
				// subscribed).
				l.detach(sub)
				return
			}
		}
	}
}

// detach: Following() reports false afterwards, so a later verb can re-attach
// if a form with this id comes back.
func (l *Libretto) detach(sub *Subscription) {
	l.mu.Lock()
	if l.sub == sub {
		l.sub, l.stop, l.done = nil, nil, nil
	}
	l.mu.Unlock()
	sub.Close()
}

// applyBatch folds the batch into one patch; reports whether the source died.
func (l *Libretto) applyBatch(sub *Subscription, batch []Event) bool {
	set := map[string]json.RawMessage{}
	removed := map[string]bool{}
	var last uint64
	dead := false
	for _, ev := range batch {
		if ev.Missed > 0 {
			// Re-seed from the source's current state: a mirror that skips a
			// patch is wrong forever and does not know it.
			if src := sub.Source(); src != nil {
				snap, at := src.Snapshot()
				_ = l.seed(snap, at)
			}
			return false
		}
		if ev.Version <= l.At() {
			continue // duplicate from the register-then-read window
		}
		for _, ent := range ev.Applied.Entries() {
			k, v := ent.Key, ent.New
			if ent.IsRemoval() {
				continue
			}
			if isLibrettoKey(k) {
				continue
			}
			set[k] = v
			delete(removed, k)
		}
		for _, ent := range ev.Applied.Entries() {
			if !ent.IsRemoval() {
				continue
			}
			k := ent.Key
			if isLibrettoKey(k) {
				continue
			}
			removed[k] = true
			delete(set, k)
		}
		// A tombstone on the source is the death notice. The copy stays,
		// which is what makes a studied form deletable at all.
		if _, isDead := ev.Applied.Entry(TombstoneKey); isDead {
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
	// Built against the copy's own form so the patch carries what it replaces.
	base, _ := l.form.Snapshot()
	if _, _, err := l.form.ApplyEffectPrivileged(
		form.Build(base, set, remove), 0); err != nil {
		// The death is not recorded, so keep listening: unsubscribing without
		// alive=false would be silently stale rather than truthfully dead.
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

// Close stops following; the copy stays on disk.
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
func isLibrettoKey(k string) bool {
	return strings.HasPrefix(k, "system.libretto.") || k == TombstoneKey
}

// HiddenLibrettoKey is bookkeeping the model must never see: at moves on every
// fold, and refs moves whenever another aria studies or drops the same form,
// cross-aria noise inside one aria's context.
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
	return form.Build(form.Snapshot{}, set, nil)
}

// refsOf: a libretto written before the set existed carries a number here; it
// reads as empty and the migration pass rebuilds it from the bound forms.
func refsOf(s form.Snapshot) []string {
	raw, ok := s.Get(KeyLibrettoRefs)
	if !ok {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// refsMigrated reports whether this libretto's refs are a SET, not a count.
func refsMigrated(s form.Snapshot) bool {
	raw, ok := s.Get(KeyLibrettoRefs)
	if !ok {
		return false
	}
	var ids []string
	return json.Unmarshal(raw, &ids) == nil
}

func (l *Libretto) RefsMigrated() bool { return refsMigrated(l.formState()) }

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
