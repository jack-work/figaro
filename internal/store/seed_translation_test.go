package store

import (
	"encoding/json"
	"strings"
	"testing"
	"unsafe"

	"github.com/jack-work/figaro/internal/message"
)

// SEEDING THE DECODED TRANSLATION CACHE.
//
// The provider round-trips are ALREADY shared: the translation log goes
// through newXwalLog, so it inherits xwal's fork base and a child reads its
// ancestor's durable records below it. Nobody is re-translating. What is
// duplicated is the DECODE -- two forks holding two in-memory copies of the
// same records -- exactly as it was for the fig IR before 9343979f.
//
// TWO HAZARDS THAT DO NOT APPLY TO THE FIG IR, pinned before the code:
//
//  1. FINGERPRINT. A translation cache is keyed by encoder fingerprint and
//     cleared WHOLESALE on mismatch. Donating rows rendered for another
//     dialect is a lie, not a miss, and it would survive in memory after the
//     durable log was cleared.
//  2. NAMESPACE. There are three (anthropic, copilot-messages,
//     copilot-responses). A cross-namespace donation is the same lie in a
//     different costume.

// sameRaw reports whether two raw blocks point at the same bytes.
func sameRaw(a, b json.RawMessage) bool {
	return len(a) == len(b) && len(a) > 0 && unsafe.SliceData(a) == unsafe.SliceData(b)
}

func transRow(lt uint64, fp, text string) Entry[[]json.RawMessage] {
	return Entry[[]json.RawMessage]{
		LT: lt, FigaroLT: lt, Fingerprint: fp,
		Payload: []json.RawMessage{json.RawMessage(`{"text":"` + text + `"}`)},
	}
}

// A seeded log must refuse a donation whose fingerprint does not match the
// records the log actually holds. The seam check is the place that can see it:
// it reads the last donated record back out of the log and compares.
func TestATranslationSeedWithAForeignFingerprintIsRefused(t *testing.T) {
	inner := NewMemLog[[]json.RawMessage]()
	for i := 1; i <= 6; i++ {
		if _, err := inner.Append(transRow(uint64(i), "fp-A", "row")); err != nil {
			t.Fatal(err)
		}
	}
	rows := inner.Read()

	// The same rows, relabelled as another encoder's output: what an ancestor
	// that re-translated under a new fingerprint would be holding.
	foreign := make([]Entry[[]json.RawMessage], 0, 3)
	for _, e := range rows[:3] {
		e.Fingerprint = "fp-B"
		foreign = append(foreign, e)
	}

	seeded := newSeededLog[[]json.RawMessage](inner, 0, 0, 1, 1, transEntrySize, foreign)
	got := seeded.Read()
	for _, e := range got {
		if e.Fingerprint != "fp-A" {
			t.Fatalf("a row with fingerprint %q is being served from a log whose records are fp-A; "+
				"the child would render bytes encoded for another dialect", e.Fingerprint)
		}
	}
	if len(got) != len(rows) {
		t.Fatalf("the refusal lost rows: %d served, %d in the log", len(got), len(rows))
	}
}

// And the positive direction, so the refusal is not simply "refuse everything":
// a donation whose fingerprint matches is used.
func TestATranslationSeedWithAMatchingFingerprintIsUsed(t *testing.T) {
	inner := NewMemLog[[]json.RawMessage]()
	for i := 1; i <= 6; i++ {
		if _, err := inner.Append(transRow(uint64(i), "fp-A", "row")); err != nil {
			t.Fatal(err)
		}
	}
	rows := inner.Read()
	seeded := newSeededLog[[]json.RawMessage](inner, 0, 0, 1, 1, transEntrySize, rows[:3])
	got := seeded.Read()
	if len(got) != len(rows) {
		t.Fatalf("seeded log serves %d rows, the log holds %d", len(got), len(rows))
	}
	// IDENTITY ON THE SLICE, NOT ON A CONVERTED STRING. json.RawMessage is
	// []byte, and string(raw) COPIES -- comparing the string headers reports
	// "0 of 3 shared" no matter what, which is what this fixture said first.
	shared := 0
	for i := 0; i < 3; i++ {
		if sameRaw(rows[i].Payload[0], got[i].Payload[0]) {
			shared++
		}
	}
	if shared != 3 {
		t.Fatalf("%d of 3 donated rows share the donor's bytes; the donation was not used", shared)
	}
}

// translatedTrunk builds a parent with IR records AND their translations, in
// the shape production writes them -- each translation entry carrying
// FigaroLT = the IR LT it translates (provider/projection.go) -- and then
// forks it twice. Translations must exist BEFORE the fork: a child inherits
// what was durable at its base and nothing after, so a fixture that appends
// afterwards measures an empty child. It measured exactly that first.
func translatedTrunk(t *testing.T, namespaces ...string) (b *XwalBackend, parent, childA, childB string) {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	parent, _ = b.CreateConversation(l)

	ir, _ := b.OpenFigIR(parent)
	trans := map[string]Log[[]json.RawMessage]{}
	for _, ns := range namespaces {
		tl, terr := b.OpenTranslator(parent, ns)
		if terr != nil {
			t.Fatal(terr)
		}
		trans[ns] = tl
	}
	for i := 0; i < 60; i++ {
		e, aerr := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{message.TextContent("an IR record worth translating")}}})
		if aerr != nil {
			t.Fatal(aerr)
		}
		for _, ns := range namespaces {
			if _, terr := trans[ns].Append(Entry[[]json.RawMessage]{
				FigaroLT:    e.LT, // the coordinate production stamps
				Fingerprint: "fp-" + ns,
				Payload: []json.RawMessage{json.RawMessage(
					`{"ns":"` + ns + `","content":"a translated record long enough to be worth sharing between branches"}`)},
			}); terr != nil {
				t.Fatal(terr)
			}
		}
	}
	if _, childA, err = b.ForkAt(parent, 40); err != nil {
		t.Fatal(err)
	}
	if _, childB, err = b.ForkAt(parent, 40); err != nil {
		t.Fatal(err)
	}
	return b, parent, childA, childB
}

// NAMESPACE: a fork's translation cache is seeded only from the SAME provider
// namespace. The backend keys handles by namespace, so this asserts the wiring
// rather than a comment about it.
func TestATranslationSeedNeverCrossesNamespaces(t *testing.T) {
	b, parent, childA, _ := translatedTrunk(t, "anthropic", "copilot-messages")
	defer b.Close()

	for _, ns := range []string{"anthropic", "copilot-messages"} {
		if _, err := b.OpenTranslator(parent, ns); err != nil {
			t.Fatal(err)
		}
	}
	for _, ns := range []string{"anthropic", "copilot-messages"} {
		l, err := b.OpenTranslator(childA, ns)
		if err != nil {
			t.Fatal(err)
		}
		rows := l.Read()
		if len(rows) == 0 {
			t.Fatalf("namespace %s: the child inherited no rows; this measurement would be vacuous", ns)
		}
		for _, e := range rows {
			for _, raw := range e.Payload {
				if want := `"ns":"` + ns + `"`; !bytesContains(raw, want) {
					t.Fatalf("namespace %s served payload %s: a donation crossed namespaces", ns, raw)
				}
			}
		}
	}
}

func bytesContains(raw json.RawMessage, want string) bool {
	return len(raw) >= len(want) && stringsContains(string(raw), want)
}

func stringsContains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// Equivalence: a seeded translation log answers exactly as an unseeded one.
func TestSeededTranslationEqualsAnUnseededOne(t *testing.T) {
	b, parent, childA, _ := forkedPair(t)
	dir := b.root

	pl, err := b.OpenTranslator(parent, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := pl.Append(Entry[[]json.RawMessage]{
			Fingerprint: "fp-A",
			Payload:     []json.RawMessage{json.RawMessage(`{"i":` + string(rune('0'+i%10)) + `}`)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = pl.Read()
	cl, err := b.OpenTranslator(childA, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	seeded := cl.Read()
	seededLen := cl.Len()
	b.Close()

	b2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	cl2, err := b2.OpenTranslator(childA, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	unseeded := cl2.Read()

	if len(seeded) != len(unseeded) || seededLen != cl2.Len() {
		t.Fatalf("seeded read %d rows (Len %d), unseeded read %d (Len %d)",
			len(seeded), seededLen, len(unseeded), cl2.Len())
	}
	for i := range unseeded {
		if seeded[i].LT != unseeded[i].LT || seeded[i].Fingerprint != unseeded[i].Fingerprint {
			t.Fatalf("row %d: seeded (LT %d, fp %q) vs unseeded (LT %d, fp %q)",
				i, seeded[i].LT, seeded[i].Fingerprint, unseeded[i].LT, unseeded[i].Fingerprint)
		}
		if len(seeded[i].Payload) != len(unseeded[i].Payload) {
			t.Fatalf("row %d: payload block count differs", i)
		}
	}
}

var _ = message.Message{}

// THE MEASUREMENT, BY IDENTITY, per namespace: with the ancestor's
// translation cache resident, the child's shared prefix must point at the
// ancestor's bytes rather than decoding its own copy.
func TestTranslationSeedSharesTheAncestorsBytes(t *testing.T) {
	for _, ns := range []string{"anthropic", "copilot-messages", "copilot-responses"} {
		t.Run(ns, func(t *testing.T) {
			b, parent, childA, childB := translatedTrunk(t, ns)
			defer b.Close()

			pl, err := b.OpenTranslator(parent, ns)
			if err != nil {
				t.Fatal(err)
			}
			pl.ReadFrom(1, 0) // residency is demand-driven: Read peeks
			p := pl.Read()
			byLT := map[uint64]json.RawMessage{}
			for _, e := range p {
				if len(e.Payload) > 0 {
					byLT[e.FigaroLT] = e.Payload[0]
				}
			}

			compared, shared := 0, 0
			for _, child := range []string{childA, childB} {
				cl, cerr := b.OpenTranslator(child, ns)
				if cerr != nil {
					t.Fatal(cerr)
				}
				rows := cl.Read()
				if len(rows) == 0 {
					t.Fatal("the child inherited no translation rows; this measurement would be vacuous")
				}
				for _, e := range rows {
					raw, ok := byLT[e.FigaroLT]
					if !ok || len(e.Payload) == 0 {
						continue
					}
					compared++
					if sameRaw(raw, e.Payload[0]) {
						shared++
					}
				}
			}
			if compared == 0 {
				t.Fatal("compared nothing; the fixture cannot show sharing")
			}
			t.Logf("%s: %d blocks compared across two branches, %d SHARED, %d MINTED",
				ns, compared, shared, compared-shared)
			if shared != compared {
				t.Errorf("%d of %d translation blocks were MINTED by a fork whose ancestor is resident",
					compared-shared, compared)
			}
		})
	}
}

// DOES THE FINGERPRINT CHECK LEAVE A MECHANISM THAT NEVER FIRES?
//
// A guard that blocks the common case would make this a knob nobody sends,
// which this project has refused to ship before. So the two cases are
// measured rather than argued:
//
//	normal ancestor            -> the seed FIRES  (76 of 76 blocks shared)
//	ancestor on a new dialect  -> the seed REFUSES and the child decodes
//
// The second is an encoder-config drift event, not a per-open condition: a
// live ancestor clears its cache wholesale on mismatch, so its resident rows
// are uniform in the steady state. The check costs one pass over the donated
// rows and blocks only after a drift.
func TestTheFingerprintCheckBlocksOnlyAfterADialectChange(t *testing.T) {
	b, parent, childA, _ := translatedTrunk(t, "anthropic")
	defer b.Close()

	pl, err := b.OpenTranslator(parent, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	rows := pl.Read()
	if len(rows) == 0 {
		t.Fatal("the parent holds no translations; this measurement would be vacuous")
	}

	// (1) The normal case fires: measured by the identity test above; here we
	// assert only that a donation is available and legal.
	base := uint64(41) // ForkAt(parent, 40): the child owns from 41
	donated := pl.(*treeLog[[]json.RawMessage]).residentBelow(base)
	if len(donated) == 0 {
		t.Fatal("no rows are resident below the fork base; the seed could never fire")
	}
	fp := donated[0].Fingerprint
	for _, e := range donated {
		if e.Fingerprint != fp {
			t.Fatalf("a live ancestor holds MIXED fingerprints (%q and %q) in its resident window; "+
				"the steady-state assumption behind this check is wrong", fp, e.Fingerprint)
		}
	}

	// (2) The drift case refuses. Relabel the donation as another dialect --
	// what an ancestor that re-encoded would hold -- and confirm the seeded
	// log falls back to the durable rows rather than serving them.
	foreign := make([]Entry[[]json.RawMessage], len(donated))
	copy(foreign, donated)
	for i := range foreign {
		foreign[i].Fingerprint = "fp-OTHER-DIALECT"
	}
	inner := newXwalLog[[]json.RawMessage](b.store, childA, transChannel("anthropic"), false)
	seeded := newSeededLog[[]json.RawMessage](inner, 0, 0, 1, 1, transEntrySize, foreign)
	for _, e := range seeded.Read() {
		if e.Fingerprint == "fp-OTHER-DIALECT" {
			t.Fatal("a foreign-dialect donation was served to a child; the durable log says otherwise")
		}
	}
	t.Logf("fires on a uniform ancestor (%d rows below the base, fingerprint %q); refuses after a dialect change",
		len(donated), fp)
}

// AND THE CHILD'S OWN ROWS ARE ITS OWN. The ancestor may serve the inherited
// prefix and NOT ONE COORDINATE MORE: past the channel's own fork base the
// two lineages hold DIFFERENT records at the same number, so an ancestor
// asked for them answers with another conversation's rows.
//
// This is what distinguishes the channel's own fork base from the ancestor's
// tail, which is why it is a test and not a comment.
func TestAForkServesItsOwnRowsAboveTheChannelsForkBase(t *testing.T) {
	b, parent, childA, _ := translatedTrunk(t, "anthropic")
	defer b.Close()

	pl, err := b.OpenTranslator(parent, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	cl, err := b.OpenTranslator(childA, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	inherited := len(cl.Read())
	if inherited == 0 {
		t.Fatal("the child inherited nothing; this measurement would be vacuous")
	}

	// The parent keeps writing, and so does the child: from here their
	// coordinates collide and their contents must not.
	for i := 0; i < 3; i++ {
		if _, err := pl.Append(Entry[[]json.RawMessage]{
			FigaroLT: uint64(900 + i),
			Payload:  []json.RawMessage{json.RawMessage(`{"role":"user","content":"PARENT ONLY"}`)},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := cl.Append(Entry[[]json.RawMessage]{
			FigaroLT: uint64(900 + i),
			Payload:  []json.RawMessage{json.RawMessage(`{"role":"user","content":"CHILD ONLY"}`)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows := cl.Read()
	if len(rows) != inherited+3 {
		t.Fatalf("the child reads %d rows, want %d", len(rows), inherited+3)
	}
	for _, e := range rows[inherited:] {
		if got := string(e.Payload[0]); !strings.Contains(got, "CHILD ONLY") {
			t.Fatalf("the child was served %s -- that row belongs to the parent", got)
		}
	}
}
