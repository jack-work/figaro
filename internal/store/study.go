package store

// study and drop, as the store performs them.
//
// A study leaves TWO nodes consistent: the libretto (refcount up, following)
// and the observer's board (`system.studies` gains the id). Two actors, two
// logs, no shared transaction, and durable-forms §12.2.1 says what to do
// about that: not two-phase commit, but ORDERING chosen so every crash fails
// in the safe direction.
//
//	study: libretto FIRST (retain), board SECOND
//	drop:  board FIRST (stop claiming it), libretto SECOND (release)
//
// Both leave, on a crash, a refcount that is too HIGH. Too high delays
// reclamation; too low reclaims a copy a live observer still needs. One is a
// leak and the other is data loss, and only the leak is recoverable — by
// `ReconcileLibrettos`, which recomputes from the boards.
//
// The verb above this (in internal/figaro) owns the user-facing rules: which
// forms are study-able, what a projection means, and what the agent's
// in-memory mirror does. This owns the two writes and their order.

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

// StudyForm makes observer study sourceForm: the libretto is minted if
// absent, seeded from the source, retained, and following; then the board
// declares it. Idempotent — studying twice is not two references, because the
// board is a SET and the refcount is derived from the boards.
func (b *XwalBackend) StudyForm(observerID, sourceFormID string) ([]string, bool, error) {
	lib, err := b.libretto(sourceFormID)
	if err != nil {
		return nil, false, err
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
			return nil, false, err
		}
		if slices.Contains(studies, sourceFormID) {
			return studies, false, nil // already declared, and not a second reference
		}
		if !retained {
			// LIBRETTO FIRST. A crash here leaves a count too high, which the
			// sweep repairs. The reverse leaves a board naming a libretto
			// nothing counted, which reclamation would collect out from
			// under a live observer.
			if _, err := lib.Retain(); err != nil {
				return nil, false, err
			}
			retained = true
		}
		next := append(append([]string(nil), studies...), sourceFormID)
		err = b.setStudies(observerID, next, version)
		if err == nil {
			retained = false // the declaration owns it now
			return next, true, nil
		}
		if !errors.Is(err, ErrFormMoved) {
			return nil, false, err
		}
		backoff(attempt)
	}
	return nil, false, fmt.Errorf("study: the board would not hold still after %d attempts",
		studyAttempts)
}

// studyAttempts sizes the optimistic retry on `system.studies`.
//
// It used to be five, which was enough while a figaro's casts were
// serialized by its actor loop. Taking the cast off that loop (the self-cast
// deadlock) replaced serialization with optimism, and optimism has to be
// sized for the contention it now meets: eight concurrent casts of one
// figaro exhausted five attempts and failed with "the board would not hold
// still", losing studies for roles that had already been pointed at the
// caster.
//
// Each attempt costs one fsync on conflict, so a generous count is cheap and
// only spent under contention that used to be impossible.
// A var, not a const, so a test can drive the exhaustion path: it is the
// branch where the two participants can disagree, and it is unreachable
// otherwise without 32 real conflicts.
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
func (b *XwalBackend) DropForm(observerID, sourceFormID string) ([]string, bool, error) {
	var studies []string
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
			return nil, false, err
		}
		idx := slices.Index(studies, sourceFormID)
		if idx < 0 {
			return studies, false, nil
		}
		// BOARD FIRST: stop claiming it before the count comes down, so a
		// crash between the two over-counts rather than under-counts.
		next := slices.Delete(append([]string(nil), studies...), idx, idx+1)
		if err := b.setStudies(observerID, next, version); err != nil {
			if errors.Is(err, ErrFormMoved) {
				backoff(attempt)
				continue
			}
			return nil, false, err
		}
		studies = next
		undeclared = true
		break
	}
	if !undeclared {
		return studies, false, fmt.Errorf(
			"drop: the board would not hold still after %d attempts (the study stands, and so does its reference)",
			studyAttempts)
	}
	lib, err := b.libretto(sourceFormID)
	if err != nil {
		return studies, true, err
	}
	if _, err := lib.Release(); err != nil {
		return studies, true, err
	}
	return studies, true, nil
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

// StudiedBy is the observer's declared set, from the durable board rather
// than from any in-memory mirror of it.
func (b *XwalBackend) StudiedBy(observerID string) ([]string, error) {
	return b.studiesOfBoard(observerID)
}

// Libretto returns the libretto for a studied form, minting and following it
// if it is not already open. Exported for the verb and for the translator.
//
// The sigil matters in exactly one direction: an unbound form is addressed as
// "@abc123" and its libretto's STUMP is named for the bare id, so the name is
// stripped and the LOOKUP is not. Getting that backwards produces "unknown
// trunk", which is how this was found.
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
//
// The rule is the base rule of the whole design and it was broken by the
// reconciliation sweep, which opened its own Libretto per libretto examined:
// a copy already open and following then had a second writer appending to its
// log, each computing versions from its own replayed state. Whoever needs a
// libretto asks here.
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
//
// It was, and that was the bug b2b0c543 caught under -race: the last drop
// closed the instance, so a verb holding it got ErrFormClosed and retried
// its refcount move on a fresh one -- and the first move is AMBIGUOUS. The
// conditional apply may have landed durably before the close reported the
// sentinel, so the retry decremented twice and the next honest drop found
// "release below zero".
//
// Transferring a refcount across an instance boundary cannot be made safe by
// retrying, because the caller cannot tell whether its write landed. So the
// boundary is removed instead: the instance lives until the backend closes,
// exactly like any other resident Form, and the verb path has no lifecycle
// race to retry through.
//
// What that costs is bounded and already measured: one fold goroutine and
// one resident source Form per form that has been studied since this daemon
// started, plus a second durable write per source patch while a copy is
// current. `doctor mem` reports both numbers.

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
//
// Privileged, because `system.studies` is system-managed -- which is what
// stops a hand-written `fig set` from claiming a study nothing counted.
func (b *XwalBackend) setStudies(observerID string, ids []string, ifVersion uint64) error {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, _, err = b.ApplyFormEffectPrivilegedIf(observerID, message.Patch{
		Set: map[string]json.RawMessage{StudiesKey: raw},
	}, ifVersion)
	return err
}

// studiesAndVersion reads the declared set together with the version it was
// read at, which is what makes the write above a compare-and-set.
func (b *XwalBackend) studiesAndVersion(observerID string) ([]string, uint64, error) {
	snap, err := b.FormState(observerID)
	if err != nil {
		return nil, 0, err
	}
	version, err := b.FormVersion(observerID)
	if err != nil {
		return nil, 0, err
	}
	return studiesOf(snap), version, nil
}
