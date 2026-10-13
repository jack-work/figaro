package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
)

type StudyDecl struct {
	Studies []string // the declared set as it stands
	Version uint64   // board version this set landed at; 0 if nothing was written
	Changed bool     // whether this call wrote
}

// StudyForm mints, seeds, retains and follows the libretto, then the board
// declares it. Idempotent: the board is a set and the refcount is derived
// from the boards.
func (b *XwalBackend) StudyForm(observerID, sourceFormID string) (StudyDecl, error) {
	lib, err := b.libretto(sourceFormID)
	if err != nil {
		return StudyDecl{}, err
	}
	// A ref move is idempotent: a retry costs no durable write and a release
	// that never retained is a no-op.
	declared := false
	defer func() {
		if !declared {
			if _, err := lib.Release(observerID); err != nil {
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
			// The board already declares it: make the backref agree.
			if _, err := lib.Retain(observerID); err != nil {
				return StudyDecl{}, err
			}
			declared = true
			return StudyDecl{Studies: studies}, nil
		}
		// Libretto first: the reverse leaves a board naming a libretto nothing counted.
		if _, err := lib.Retain(observerID); err != nil {
			return StudyDecl{}, err
		}
		next := append(append([]string(nil), studies...), sourceFormID)
		landed, err := b.setStudies(observerID, next, version)
		if err == nil {
			declared = true
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

var studyAttempts = 32

// backoff breaks the lockstep of N writers of one board; tiny because the
// writes themselves are milliseconds.
func backoff(attempt int) {
	d := time.Duration(attempt+1) * 200 * time.Microsecond
	if d > 5*time.Millisecond {
		d = 5 * time.Millisecond
	}
	time.Sleep(d)
}

// DropForm is the inverse, in the inverse order. Dropping a form since deleted
// is legal: the subscription goes, the board stops naming it, the copy stays.
func (b *XwalBackend) DropForm(observerID, sourceFormID string) (StudyDecl, error) {
	var studies []string
	var landed uint64
	// The declaration must be gone before the count comes down: releasing
	// after exhausted retries would under-count a libretto a board still
	// names, which the sweep cannot tell from a legitimate observer.
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
		// Board first, so a crash between the two over-counts rather than under-counts.
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
	if _, err := lib.Release(observerID); err != nil {
		return decl, err
	}
	return decl, nil
}

// KindWord: both halves of study, the agent's and the hub's, must agree about
// what is study-able, and one wording is one rule.
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

// RequireStudyTarget: only an unbound form is study-able or castable. An
// outfit is a seed, a bound board is private to its figaro, and a libretto is
// derived and never reaches this check.
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

// StudiedBy reads the durable board, not an in-memory mirror of it.
func (b *XwalBackend) StudiedBy(observerID string) ([]string, error) {
	return b.studiesOfBoard(observerID)
}

func (b *XwalBackend) Libretto(sourceFormID string) (*Libretto, error) {
	return b.libretto(sourceFormID)
}

// libretto keeps one instance per source: the fold is a goroutine per
// libretto, not per observer.
func (b *XwalBackend) libretto(sourceFormID string) (*Libretto, error) {
	lib, err := b.librettoInstance(sourceFormID)
	if err != nil {
		return nil, err
	}
	if !lib.Following() {
		// Not following: the first open, or a source that could not be opened before.
		if err := b.attach(lib, sourceFormID); err != nil {
			slog.Info("libretto: not following its source",
				"libretto", lib.ID(), "source", sourceFormID, "err", err)
		}
	}
	return lib, nil
}

// librettoInstance: one Libretto per source, therefore one store.Form,
// therefore one writer on that stump's channel.
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

// LibrettoStats: a fold is a goroutine and a subscription, so this is the one
// number that says whether studying is costing anything.
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

func (b *XwalBackend) attach(lib *Libretto, sourceFormID string) error {
	src, err := b.form(sourceFormID)
	if err != nil {
		return err
	}
	return lib.Follow(src)
}

// A libretto is not torn down when its count reaches zero. closeLibrettos is
// the only teardown path and runs when the backend closes, the one moment no
// verb is in flight.
func (b *XwalBackend) closeLibrettos() {
	b.mu.Lock()
	all := b.librettos
	b.librettos = nil
	b.mu.Unlock()
	for _, lib := range all {
		lib.Close()
	}
}

// closeLibretto: shutdown and test teardown only; the verbs must not do this.
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

// setStudies is guarded by the board version so two arias studying at once
// cannot overwrite each other's declaration.
func (b *XwalBackend) setStudies(observerID string, ids []string, ifVersion uint64) (uint64, error) {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	version, _, err := b.ApplyFormEffectPrivilegedIf(observerID,
		form.Build(form.Snapshot{}, map[string]json.RawMessage{StudiesKey: raw}, nil), ifVersion)
	return version, err
}

func (b *XwalBackend) studiesAndVersion(observerID string) ([]string, uint64, error) {
	// One atomic load: a set and a version read separately can be a pair that
	// never existed, and the guard would then pass over another aria's declaration.
	at, err := b.FormAt(observerID)
	if err != nil {
		return nil, 0, err
	}
	return studiesOf(at.Snapshot), at.Version, nil
}
