package store

// study and drop, as the store performs them.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// StudyDecl is one declaration's verdict: the set that LANDED, and the board
// VERSION it landed at.
type StudyDecl struct {
	Studies []string // the declared set as it stands
	Version uint64   // board version this set landed at; 0 if nothing was written
	Changed bool     // whether this call wrote
}

// StudyForm makes observer study sourceForm: the libretto is minted if
// absent, seeded from the source, retained, and following; then the board
// declares it. Idempotent — studying twice is not two references, because the
// board is a SET and the refcount is derived from the boards.
func (b *XwalBackend) StudyForm(observerID, sourceFormID string) (StudyDecl, error) {
	lib, err := b.libretto(sourceFormID)
	if err != nil {
		return StudyDecl{}, err
	}
	// RETAINED ONCE, not once per attempt. The reference is taken before the
	// first board write (§12.2.1's order) and held across the retries, so
	// contention costs extra board writes rather than extra durable writes
	// on the libretto -- eight concurrent casts were paying a retain and a
	// release per attempt each.
	retained := false
	defer func() {
		// Whatever the outcome, a reference this call took and did not use
		// goes back: a failed study must not leave a count only a sweep can
		// explain.
		if retained {
			if _, err := lib.Release(); err != nil {
				slog.Warn("study: could not release after a failed attempt",
					"aria", observerID, "form", sourceFormID, "err", err)
			}
		}
	}()

	for attempt := 0; attempt < studyAttempts; attempt++ {
		studies, version, err := b.studiesAndVersion(observerID)
		if err != nil {
			return StudyDecl{}, err
		}
		if slices.Contains(studies, sourceFormID) {
			// Already declared, and not a second reference. No version: this
			// call wrote nothing, so it has no claim on the mirror's order.
			return StudyDecl{Studies: studies}, nil
		}
		if !retained {
			// LIBRETTO FIRST. A crash here leaves a count too high, which the
			// sweep repairs. The reverse leaves a board naming a libretto
			// nothing counted, which reclamation would collect out from
			// under a live observer.
			if _, err := lib.Retain(); err != nil {
				return StudyDecl{}, err
			}
			retained = true
		}
		next := append(append([]string(nil), studies...), sourceFormID)
		landed, err := b.setStudies(observerID, next, version)
		if err == nil {
			retained = false // the declaration owns it now
			return StudyDecl{Studies: next, Version: landed, Changed: true}, nil
		}
		if !errors.Is(err, ErrFormMoved) {
			return StudyDecl{}, err
		}
		backoff(attempt)
	}
	return StudyDecl{}, fmt.Errorf("study: the board would not hold still after %d attempts",
		studyAttempts)
}

// studyAttempts sizes the optimistic retry on `system.studies`.
var studyAttempts = 32

// backoff spreads retries so N writers of one board converge instead of
// colliding in step. Deliberately tiny: the writes themselves are
// milliseconds and the point is only to break the lockstep.
func backoff(attempt int) {
	d := time.Duration(attempt+1) * 200 * time.Microsecond
	if d > 5*time.Millisecond {
		d = 5 * time.Millisecond
	}
	time.Sleep(d)
}

// DropForm is the inverse, in the inverse order. Dropping a form that has
// since been deleted is legal (durable-forms §12.2.2): the subscription goes,
// the board stops naming it, and the copy stays.
func (b *XwalBackend) DropForm(observerID, sourceFormID string) (StudyDecl, error) {
	var studies []string
	var landed uint64
	// The declaration must be GONE before the count comes down. Exhausting
	// the retries is not "gone": releasing anyway would take a reference off
	// a libretto a board still names, which is the under-count §12.2.1's
	// ordering exists to make impossible -- and unlike an over-count, the
	// sweep cannot tell it from a legitimate observer.
	undeclared := false
	for attempt := 0; attempt < studyAttempts; attempt++ {
		var version uint64
		var err error
		studies, version, err = b.studiesAndVersion(observerID)
		if err != nil {
			return StudyDecl{}, err
		}
		idx := slices.Index(studies, sourceFormID)
		if idx < 0 {
			return StudyDecl{Studies: studies}, nil
		}
		// BOARD FIRST: stop claiming it before the count comes down, so a
		// crash between the two over-counts rather than under-counts.
		next := slices.Delete(append([]string(nil), studies...), idx, idx+1)
		v, err := b.setStudies(observerID, next, version)
		if err != nil {
			if errors.Is(err, ErrFormMoved) {
				backoff(attempt)
				continue
			}
			return StudyDecl{}, err
		}
		studies, landed = next, v
		undeclared = true
		break
	}
	if !undeclared {
		return StudyDecl{Studies: studies}, fmt.Errorf(
			"drop: the board would not hold still after %d attempts (the study stands, and so does its reference)",
			studyAttempts)
	}
	decl := StudyDecl{Studies: studies, Version: landed, Changed: true}
	lib, err := b.libretto(sourceFormID)
	if err != nil {
		return decl, err
	}
	if _, err := lib.Release(); err != nil {
		return decl, err
	}
	return decl, nil
}

// KindWord names what a caller aimed at when a verb refuses it, so the
// refusal says WHICH rule was broken rather than only that one was. Here
// because both halves of study -- the agent's and the hub's -- must agree
// about what is study-able, and one wording is one rule.
func KindWord(kind string) string {
	switch kind {
	case string(kindOutfit):
		return "outfit, not a form"
	case "":
		return "node of no kind"
	default:
		return "figaro, not an unbound form"
	}
}

// RequireStudyTarget names the slot errors: only an UNBOUND form is
// study-able or castable. PRIMARY FORMS ONLY (Gluck, 2026-08-13): "studying
// is something for forms. Outfits are just the names I give the named files
// that seed primary forms." An outfit is a seed, not a subject; a bound
// board is private to its figaro; and a libretto is a derived form, which is
// not a node at all and so never reaches this check.
func RequireStudyTarget(b Backend, formID string) error {
	if b == nil {
		return fmt.Errorf("study: ephemeral aria has no store")
	}
	n, ok := b.Node(formID)
	if !ok {
		return fmt.Errorf("%s: no such form", formID)
	}
	if n.Kind != "form" {
		return fmt.Errorf("%s is a %s: study and cast take unbound forms (an outfit is a seed, a bound board is private to its figaro)", formID, KindWord(n.Kind))
	}
	return nil
}

// StudiedBy is the observer's declared set, from the durable board rather
// than from any in-memory mirror of it.
func (b *XwalBackend) StudiedBy(observerID string) ([]string, error) {
	return b.studiesOfBoard(observerID)
}

// Libretto returns the libretto for a studied form, minting and following it
// if it is not already open. Exported for the verb and for the translator.
func (b *XwalBackend) Libretto(sourceFormID string) (*Libretto, error) {
	return b.libretto(sourceFormID)
}

// libretto opens, seeds and starts following, once per source, and keeps the
// instance: the fold is a goroutine per LIBRETTO, not per observer, which is
// the whole point of sharing one per studied form.
func (b *XwalBackend) libretto(sourceFormID string) (*Libretto, error) {
	lib, err := b.librettoInstance(sourceFormID)
	if err != nil {
		return nil, err
	}
	if !lib.Following() {
		// Not following: either the first open, or a source that could not be
		// opened before and may be openable now.
		if err := b.attach(lib, sourceFormID); err != nil {
			slog.Info("libretto: not following its source",
				"libretto", lib.ID(), "source", sourceFormID, "err", err)
		}
	}
	return lib, nil
}

// librettoInstance is THE instance for this source: one Libretto, therefore
// one store.Form, therefore ONE WRITER on that stump's channel.
func (b *XwalBackend) librettoInstance(sourceFormID string) (*Libretto, error) {
	key := strings.TrimPrefix(sourceFormID, "@")
	b.mu.Lock()
	if l := b.librettos[key]; l != nil {
		b.mu.Unlock()
		return l, nil
	}
	b.mu.Unlock()

	// Outside the lock: this replays a form and reads files.
	lib, err := OpenLibretto(b.store, sourceFormID)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if existing := b.librettos[key]; existing != nil {
		b.mu.Unlock()
		lib.Close() // lost the race; the shared one wins
		return existing, nil
	}
	if b.librettos == nil {
		b.librettos = map[string]*Libretto{}
	}
	b.librettos[key] = lib
	b.mu.Unlock()
	return lib, nil
}

// LibrettoStats is what the daemon holds for phase 9: how many librettos are
// open and folding, and how many observers they carry between them. A fold is
// a goroutine and a subscription, so this is the one number that says whether
// studying is costing anything.
func (b *XwalBackend) LibrettoStats() (open, observers int) {
	b.mu.Lock()
	all := make([]*Libretto, 0, len(b.librettos))
	for _, l := range b.librettos {
		all = append(all, l)
	}
	b.mu.Unlock()
	for _, l := range all {
		open++
		observers += l.Refs()
	}
	return open, observers
}

// attach subscribes a libretto to its source, if the source can be opened.
func (b *XwalBackend) attach(lib *Libretto, sourceFormID string) error {
	src, err := b.form(sourceFormID)
	if err != nil {
		return err
	}
	return lib.Follow(src)
}

// A LIBRETTO IS NOT TORN DOWN WHEN ITS COUNT REACHES ZERO.

// closeLibrettos stops every fold. The ONLY teardown path, and it runs when
// the backend closes -- which is the one moment no verb is in flight.
func (b *XwalBackend) closeLibrettos() {
	b.mu.Lock()
	all := b.librettos
	b.librettos = nil
	b.mu.Unlock()
	for _, lib := range all {
		lib.Close()
	}
}

// closeLibretto drops one instance. Shutdown and test teardown only: see the
// note above for why the verbs must not do this.
func (b *XwalBackend) closeLibretto(source string) {
	source = strings.TrimPrefix(source, "@")
	b.mu.Lock()
	lib := b.librettos[source]
	delete(b.librettos, source)
	b.mu.Unlock()
	if lib != nil {
		lib.Close()
	}
}

func (b *XwalBackend) studiesOfBoard(observerID string) ([]string, error) {
	snap, err := b.FormState(observerID)
	if err != nil {
		return nil, err
	}
	return studiesOf(snap), nil
}

// setStudies writes the declared set, guarded by the board version so a
// concurrent writer cannot be lost: two arias studying different forms at
// once must not overwrite each other's declaration.
func (b *XwalBackend) setStudies(observerID string, ids []string, ifVersion uint64) (uint64, error) {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	version, _, err := b.ApplyFormEffectPrivilegedIf(observerID, message.Patch{
		Set: map[string]json.RawMessage{StudiesKey: raw},
	}, ifVersion)
	return version, err
}

// studiesAndVersion reads the declared set together with the version it was
// read at, which is what makes the write above a compare-and-set.
func (b *XwalBackend) studiesAndVersion(observerID string) ([]string, uint64, error) {
	// ONE atomic load. Reading the set and the version separately hands back
	// a pair that never existed, and the caller then writes a study set
	// computed from the old one while quoting the new version -- so the
	// guard passes and another aria's declaration is overwritten.
	at, err := b.FormAt(observerID)
	if err != nil {
		return nil, 0, err
	}
	return studiesOf(at.Snapshot), at.Version, nil
}
