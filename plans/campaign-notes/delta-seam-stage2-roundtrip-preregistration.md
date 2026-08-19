# Stage 2, the round-trip property: pre-registered before the test was run

Aria 041454f1, 2026-08-18, worktree `.../seam`, branch `feat/delta-seam` at
61711fe2. Written BEFORE the assertion existed, so the score below can be
read against something that could have gone the other way.

Ruled by 7e151902 before I wrote a line: **the subject is what the provider
sends**, not the snapshot's bytes. Snapshot byte identity is STRONGER THAN
THE TRUTH and would fail for reasons that do not matter. So the order is
semantic equality (necessary), then rendered wire bytes (sufficient, and the
thing the per-LT translation cache stores and prompt caching keys on), then
the rich-corpus round trip kept separately as the MECHANISM.

## WHAT THE DESIGN RESTS ON

Stage 2 assembles one snapshot out of two implementations. The header half
is folded by figwal through `formReduce`, i.e. THROUGH JSON. The tail half
is folded by figaro through `form.Fold`, i.e. DECODED. Nothing forces them
to agree; the agreement is a property, and this is the pre-registration of
its test.

## THE MECHANISMS, EACH WITH A FALSIFIER THAT COSTS ME

**M1 — the key set survives.** `Unmarshal(Marshal(s))` holds exactly the
keys `s` held, for every board in the corpus, including keys that are empty,
dotted, cased alike, quoted, newline-bearing, non-ASCII, and 1 KB long.
FALSIFIER: one key gained or lost. COST: the header/tail split is unsound as
written and stage 2 stops until `Snapshot`'s codec changes. This is the one
7e151902 named — fold-from-header can pass on a fixture that happens not to
carry the key the round trip drops.

**M2 — `MarshalJSON` CANONICALISES; it is not an identity on raw bytes.**
`value.go` shouts that a `Value` keeps the caller's EXACT bytes and never
rewrites them. But `Snapshot.MarshalJSON` delegates to `encoding/json` over
`map[string]json.RawMessage`, and `encoding/json` COMPACTS a `RawMessage`
and rewrites `<`, `>`, `&` as `\u003c`, `\u003e`, `\u0026`. So a board built
by `FromMap`/`Apply` from NON-CANONICAL bytes comes back from a round trip
holding DIFFERENT bytes for the same key — semantically equal, byte
different. **I PREDICT PER-KEY BYTE IDENTITY FAILS** for a value with
interior whitespace or a raw `<`. FALSIFIER: if those bytes survive
unchanged, my model of `MarshalJSON` is wrong and every inference below it
has to be redone.

**M3 — one round trip reaches a fixed point.** `Marshal(Unmarshal(Marshal(s)))`
== `Marshal(s)`, byte for byte, for EVERY board including the pathological
ones. This is what makes the header a canonical form and it is what the
design actually needs from the codec. FALSIFIER: any board whose second
marshal differs from its first.

**M4 — M2 never bites in the real system, because the WRITER already
canonicalises.** Form patches reach the form channel as
`json.Marshal(message.Patch)` (verified at the append sites, not assumed:
`xwal_store.go` `writeBirth` and the stump path both marshal before
`Append`), and `json.Marshal` compacts and HTML-escapes the `RawMessage`
values inside. Both operations are IDEMPOTENT. Therefore a value decoded
from a WAL record is ALREADY a fixed point of `Marshal`, and the two halves
of a stage-2 snapshot hold the SAME BYTES rather than merely equal ones.
FALSIFIER: a form-channel append site that writes a patch payload without
passing through `encoding/json`. I enumerate the sites; I do not assume
them.

**M5 — order-dependence of raw bytes does not split the halves.**
`ptree.Set` is a NO-OP THAT KEEPS THE ORIGINAL BYTES when the incoming value
is semantically `Equal` (`tree.go`, deliberate, so a reordered re-serialise
fires no reminder). A board's bytes therefore depend on the ORDER of writes,
not only on the final values. Both halves apply that same rule to the same
record sequence, so both retain the same earliest bytes. FALSIFIER: a
fixture where value A, then a semantically-equal-but-byte-different value B,
yields different bytes fold-from-header versus fold-from-zero.

**M6 — nil, empty, `{}` and `null` are one state.** `Snapshot{}`,
`FromMap(nil)`, `Unmarshal("{}")` and `Unmarshal("null")` are
indistinguishable through the public surface and fold identically
thereafter. FALSIFIER: any of the four behaving differently under a later
`Apply` or `Marshal`.

## THE CONSEQUENCE TEST, AND WHICH WORLD ITS PASS COMES FROM

7e151902 requires that a pass on the wire-byte oracle say WHY it passes:
because the raw bytes agree, or because the renderer canonicalises them.
Those are different worlds and only one survives the next change to `Value`.

I pre-register the answer as BOTH, SPLIT BY TYPE, and the test is built to
tell them apart:

  `genericBody` (render.go) renders a STRING value through `NewString()`,
  which json-decodes it — so `"a \u003c b"` and `"a < b"` reach the wire as
  the same bytes: THE RENDERER CANONICALISES, and byte agreement there
  proves nothing about the snapshot.

  A NON-STRING value (object, array, number) falls through to
  `string(e.New)` — RAW BYTES REACH THE WIRE UNTOUCHED. Agreement there is
  agreement of the snapshots themselves, and it is the only part of the
  oracle that is robust to a change in `Value`.

So the corpus MUST carry object and array values, or the oracle passes for
the wrong reason. A wire-byte test built only from string values would be
an instrument not reaching the code — the tenth instance, in the species
"the instrument reads the wrong number".

A third channel is registered because it is easy to miss: `Render` reads
`ReadDeltaLimits(prev)` off the SAME point-in-time board, so a divergence in
`system.delta_key_bytes` between the two halves changes TRUNCATION and
therefore the wire. The corpus carries a limits key that actually truncates.

## WHAT I DO IF THE WIRE ORACLE FAILS

I bring 7e151902 the failing pair with both byte strings and I do not work
around it. A failure means the seam makes prompt-cache keys depend on WHEN a
segment rotated, and the remedy — canonicalising at the seam — is a design
change, its to rule and possibly Gluck's to endorse.

## SCORE

Written empty. I drafted a filled-in score here first, out of momentum, and
deleted it before running anything: a pre-registration that carries its own
results is not one. What follows was added only after the runs below.

- **M1 PASS.** Key set survives every corpus board — the empty key, a 1 KB
  key, keys differing only in case, keys carrying quotes, newlines, tabs and
  non-ASCII. CANARIED: `UnmarshalJSON` made to drop the empty key BUILT OK
  and turned M1 and M3 red at `pathological-keys`. Reverted.
- **M2 CONFIRMED, AND THE PREDICTION HELD IN FULL.** Per-key bytes are
  rewritten by compaction and by `<`, `>`, `&` escaping, and BY NOTHING
  ELSE. In particular `"caff\u00e8"` came back EXACTLY AS WRITTEN, which is
  what I predicted and the prediction that could most easily have gone the
  other way: `encoding/json`'s compact rewrites only those three characters
  plus U+2028/U+2029 and does not re-encode escapes. CANARIED by flipping
  one expectation to "no escaping": built, failed, reverted.
- **M3 PASS.** Fixed point on every board including the ones M2 rewrites,
  and still a fixed point on a third pass.
- **M4 PASS, and the enumeration was the point.** Every form-channel append
  marshals through `encoding/json` first: `internal/store/form.go:547`
  (`Form.Apply`, the central path), `xwal_store.go` `writeBirth`, and the
  stump path. This is the invariant the whole seam rests on.
- **M5 PASS.** The order-dependent board round-trips, and both folds retain
  the same earliest spelling.
- **M6 PASS.** All four spellings of empty are one state and fold alike.
- **WIRE ORACLE PASS, ACROSS 136 (segBase, lt) PAIRS, AND IT PASSES IN BOTH
  WORLDS — which is the answer 7e151902 asked for.** String values agree
  because the renderer decodes them; object and array values agree because
  the raw bytes themselves agree, and they agree only because of M4. The
  split is asserted as its own test so it cannot drift into the weaker
  world unnoticed.

## THE FINDING I DID NOT PRE-REGISTER, so it is scored as luck, not skill

Canarying the off-by-one header in BOTH directions turned up an asymmetry
nobody had stated: a boundary that SKIPS a record is caught everywhere; a
boundary ONE RECORD AHEAD is caught in 13 of 136 pairs, all of them at
lt == segBase, and **0 of the 120 pairs where lt > segBase** — because form
patches are IDEMPOTENT, so re-applying the record the header already holds
is a no-op. The two misses at lt == segBase are exactly the two fixture
records that change nothing.

AN EQUIVALENCE ORACLE STRUCTURALLY CANNOT SEE A HEADER THAT READS ONE
RECORD TOO MANY. Only a fold COUNT can. That makes 9ed3f561's counters the
half this oracle cannot cover, and neither instrument covers the boundary
alone.

A CORRECTION, IN ITS OWN PARAGRAPH: my first classification of those 136
reported "12 failures where lt > segBase". The classifier was awk splitting
the subtest name and leaving `(0.00s)` glued to the field it compared, so it
was comparing string garbage. Caught before it was reported, re-run with a
real parse, true figure 0 of 120. Instance-ten species: the instrument
reading the wrong number, which survives every check a missing number fails.
