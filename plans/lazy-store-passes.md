# Lazy store passes, and the eight-log node open

Opened 2026-08-21 by ede92072, from measurements taken on a 271 MB copy of the
author's store (742 arias, 1,163 nodes, 5,836 segment files). Instrument:
`scratch/sweepcost`. Approved by Gluck 2026-08-21 in the order given below.

The layered-cache campaign shipped in v0.28.0. This is the follow-up: the
whole-store passes the daemon runs at boot, and the reason each of them costs
eight times what it asks for.

## What a boot actually costs

| phase | wall | logs opened | segments recovered | blocks the socket |
|---|---|---|---|---|
| `NewXwalBackend` | 24–275 ms | 8 | 2 | yes |
| `wire.Install` | 19 ms | 16 | 3 | yes |
| `Nodes()` (first call) | 596 ms | 6,504 | 3,597 | no |
| board walk of `reconcileLibrettos` | 1,045 ms | 3,816 | 2,317 | no |
| `AuditLibrettos` (warm) | 29 ms | 120 | 19 | no |
| `metaBackfill` read half | 14 ms | 0 | 0 | no |

What the two sweeps FIND on this store: `boards 1,159 · librettos 14 ·
corrected 0 · orphaned 0 · missing 0`, and `736 sidecars · 0 needing a fold`.
Both are repair passes that read the whole store to discover nothing is wrong,
at every boot, forever.

Neither was visible: `librettos reconciled` logs only when a count CHANGED, so
a sweep that finds nothing leaves no trace. The only witness these passes ever
left was 16 MB of `log opened` records per boot, which is why they were found
by deleting that spam (3f5a81c1) rather than by reading a number.

## Two claims that did not survive measurement

- **"The topology rebuild is the floor on restart."** False. `Index.RebuildFrom`
  walks ONE directory (every node is depth-1), reads each node's marker, and
  builds an in-memory map: tens of ms at 1,163 nodes, 8 logs opened. The
  1,161 ms first measured was a freshly-copied store with cold directory
  metadata. It pulls in no arias.
- **"`Nodes()`'s memo dies on every topology change, so the next listing pays
  600 ms under `s.mu`."** False. `fig form new` followed by `ls -g` costs 38 ms,
  identical to warm. The 596 ms is a cold-cache first touch, once per boot.

## The mechanism under all of it

`openNode` → `s.trunks.Head(id)` returns the node's whole XWAL, which
materialises every channel it owns — `ir`, `turn-wal`, `form`, `translations`
(×2), `translations-v2/{anthropic,copilot-messages,copilot-responses}` —
about **8 logs and 5 segment recoveries per node**, whichever single channel
the caller wanted. Both boot payers want one:

- `b.Nodes()` → `labelAll` → `labelOf` folds a STUMPLESS aria's board to fill
  the listing's OUTFIT column: 813 × 8 = 6,504 opens. (`labelAll`'s comment,
  "200 arias on four outfits reads four birth records", holds only for arias
  WITH a stump.)
- the reconcile walk reads one key, `system.studies`, from every board; 56 of
  1,159 have it.

## What landed

**Item 1 (887cd162, 2026-08-21).** AriaMeta carries a version; the identity
fold rides Meta() and stamps. metaBackfill deleted. The stamp -- not the
emptiness of the fields -- is the completion marker, which is what the boot
pass never had. writeMetaLocked became the one writer for all three
producers. A reader that writes must revalidate: the identity healer abandons
when an ordinary SetMeta superseded the state it read, which
TestAColdReadCannotOverwriteAWriteThatBeatIt caught immediately.

**Item 2 (29816752, 2026-08-21).** system.libretto.refs is a sorted set of
observer ids, not a count. Idempotent, so a retry costs no durable write and
"release below zero" is not an error class. The array is its own version
stamp: RefsNeedMigration reads only the librettos, so the exhaustive pass runs
at most once per store and a migrated store never scans again. Boot cost of
the libretto machinery on the author's store: ZERO board reads.

It also exposed a live bug the count had hidden. inheritStudies ran BEFORE the
fork on the PARENT's id, because the child did not exist yet; +1 was right by
accident, and with ids it retained the parent again (idempotent) so the child
got no backref at all. The fork now happens first and the child retains its
own studies.

Deleted with it: HasLibrettos (guarded a sweep that no longer exists),
libretto_ledger.go (77 lines, a ring of refcount moves whose only consumer was
the below-zero error), and two tests that measured properties a set does not
have.

## The work, in the order Gluck approved it

### 1. Sidecar version + migrate-on-read; then delete the boot pass

`metaBackfill` has no completion marker: it re-reads every sidecar at every
boot forever. Its candidacy test is a heuristic — "mantra, cwd and outfit are
all empty" — which cannot distinguish "not yet migrated" from "genuinely
empty", so a legitimately blank aria would be re-folded at every boot for the
life of the store. Zero such arias exist today; that is luck, not design.

Put a version int on `AriaMeta`. The list path already fetches meta per aria
(`enrichList` → `fillFromMeta`): when the version is old, fold the form there
and write it back. An aria nobody looks at then costs nothing forever, an aria
listed once is migrated once, no boot pass exists, and the next migration
reuses the seam instead of adding another scan.

### 2. Lazy libretto reconcile + a doctor verb

Reconcile a libretto when it is OPENED — the only moment its refcount matters
— which is O(14) instead of O(1,159). The whole-store pass survives as
`figaro doctor librettos`, run on demand. The migration case (minting for
pre-existing studies) is already complete on this store: `missing 0`.

An index of studying boards was considered and rejected: it duplicates what
the boards already say, which is the accretion the standing rules warn against.

### 3. Channel-granular node open

Open the channel that was asked for. Worth roughly 7/8 of both walks above and
speeds every first-touch read, not just boot. Largest blast radius of the
three; wants its own gate.

IN PROGRESS. xwal.open builds channels from the manifest and opens no log;
ch.Log() opens once, on first touch. The field was renamed log -> lg so the
COMPILER finds all 38 call sites, which is the only reason this was safe to
attempt -- a nil field would have been found by panics in production instead.

hydrateLastTS was the reason laziness alone bought nothing: it read the last
record of every channel at every open, which IS the eight opens and the five
segment recoveries. It now runs on the first append or the first explicit
LastTS, and where a Trunks owns the node it uses the existing file probe
(probeLastTS reads segment files without opening a log) -- one mechanism where
there were two.

The sync paths skip channels that were never opened: nothing written through a
handle can be dirty in a channel it never touched.

GATE, BEYOND THE USUAL: one run of TestCrashKill reported append-lost and the
package failed; a rerun passed, and the seeds are random. Being unable to tell
a pre-existing flake from a durability regression by staring at it, the answer
is an A/B -- 20 crash runs on HEAD and 20 on the lazy tree, same conditions,
counting failed runs and VIOLATION lines. Nothing else may run on the box
during it: the kill delays are timing-sensitive and the arms run in sequence,
so load on one arm alone would bias the comparison.

## Standing

`scratch/sweepcost` is the instrument. Re-run it against a COPY of a real
store (never the live one — the daemon holds the flock, and a second opener
bypasses it) and the table above is reproducible in one command.
