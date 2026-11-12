# Translation channels: a cache per figaro

Status: brainstorm. A proposal for Gluck, written before any code for it.
Lane `lane/fork-turn0` (feigned-molecule), base impresario 86233533
(v0.65.4). Covers issue 90, the fork at `<id>:1.0` bug and three findings
from the startup lane's catch-up profile.

The ruling it answers (Gluck, 11-11, agreeing with the Engineer's summary):
the IR log by LT is the only truth. A provider's translation channel is a
derived encoding of it. A channel records exactly which history it encodes,
never silently inherits a parent's prefix through a fork, and is appended
only after its own tail. A channel that does not match the history is
marked stale, never patched in place, and rebuilt from the IR: lazily on
the next turn, and by `doctor normalize` as a sweep. Gluck added:

> the presentation parent can probably be a trunk id rather than an lt,
> since the lt may begin prior to the presentation parents history. but we
> use the filesystem hierarchy parent's LT for operations like this so they
> can behave like before the regression

## Proposal in brief

1. A translation channel belongs to one figaro. It holds one row for every
   record that figaro reads, from LT 2 on, and reads no other directory. A
   fork starts with no rows; its first send encodes its history (33 ms of
   CPU at the median, measured below).
2. One writer (catch-up), one reader, one check. The check is O(1). A
   channel that fails it is retired whole and built again from the IR.
   Nothing is cut or inserted.
3. A channel names its figaro by trunk id and its encoder by fingerprint,
   in a header. A seat names a figaro by id and carries no LT. LT
   arithmetic lives only on the data lineage, which for every node is the
   lineage it had before 0.63.0 (where forks began to be moved under the
   figaro they were asked of).
4. One precondition: what a provider signs has to be on the IR record.
   Today a signed thinking block exists only in the row (16% of current
   anthropic assistant rows on the small store), so a rebuild loses it.
5. Translate-on-append and the provider's separate assistant row go away.
   The fingerprint is checked per channel, not per tail row.
6. The crash repair stops visiting translation channels. `doctor normalize`
   sweeps them. The old namespaces are deleted.
7. The read fix already on the lane (43e335b0) ships first. It cures the
   fork bug, issue 90 as reproduced and the masked reads today.

Four questions for Gluck are at the end.

## Today

A fork creates a directory for the child in every channel, with a marker
`.fork base=N`: indexes below N are read from the parent's directory. For
the IR that is sound, because the IR is append-only. A translation channel
is shared the same way, by position in the parent's channel, and it is not
append-only:

- `Clear` removes the directory and its marker and regrows the channel from
  index 1 (fingerprint change, hash mismatch at the tail, a cut below the
  fork base).
- The hole check cuts at the first record with no row (`TruncateFrom`) and
  re-derives from there.
- Translate-on-append writes a row for every input record into every
  provider channel the figaro has, and none for assistant records.

Each of these renumbers or replaces rows that descendants point into.

The store also has two readers of a lineage. The disk chain (`figwal/disk`
`Log`) reads below a base from its parent, capped by every base below it:
right for any bases. The tree cache (`fwtree` `split`, the store's `cuts`)
splits root first and assumes bases never decrease from root to leaf. Two
things now produce a decreasing base: a seat (since 0.63.0 a fork cut in an
old holder's turns is moved under the figaro it was asked of), and a
cleared channel (base 1 under an ancestor whose base is higher).

| sighting | seen | cause |
|---|---|---|
| fork at `<lead>:1.0`, 0.65.4 | whole stale turn shown; first two sends lost; Anthropic 400 "assistant message prefill" | IR base 6 under a parent with base 21; the tree cache gives the first holder LTs up to 16 and hides the fork's own records |
| issue 90 | "got 163, last 189", first on v0.63.0, the release that began moving forks; "got 164, last 233" after a restart on v0.65.1 | the same split in the channel's key space: the hole check sees the ancestor's rows instead of the figaro's own, cuts nothing, appends below the tail. Reproduced on 86233533 with a seated fork ("got 5, last 8") |
| El Buscador | "got 3, last 5" on its first turn | same family; its store is gone, the error text is not reproduced |
| masked forks (startup-4) | count equals record count; 47 records have no row, 47 have two | the figaro's channel was cleared: no marker, 1,076 own rows keyed 2..1077. Its parent's base there is 92, so the split serves rows 1..91 from the ancestor. Checked on a copy of the small store: 47/47 on 86233533, 0/0 with 43e335b0 |
| combs (startup-4) | 37 of 58 live figaros owe rows, p50 467, max 1,690 | on-append to idle providers |
| mixed fingerprints (startup-4) | one request built from rows written under v7, v8 and v10 | the fingerprint is compared on the tail row only, and on-append has just written that row |
| signed thinking (measured here) | 980 of 6,083 current-fingerprint assistant rows differ from a fresh encode of their record; every difference is a signed thinking block the row has and the IR lacks | the channel holds data the IR does not |

The last row is why "rebuild from the IR" is not free today. It is taken up
under invariant 9.

## Invariants

1. The IR is the only truth. A figaro's history is the records it reads, by
   LT. Anything else on disk can be deleted and rebuilt from the IR, the
   form history and the encoder.
2. A channel belongs to one figaro and one provider. It holds one row per
   record of that figaro's history, in LT order, from LT 2: row i encodes
   record i+1. Genesis is LT 1 and has no row.
3. A channel names what it encodes. Its header carries the figaro's trunk
   id and the encoder fingerprint it was built under. A directory without a
   matching header is not a channel.
4. A channel reads no other directory. Fork, seat, promote, detach, delete
   and move change no row of any channel.
5. One writer. Rows are appended by catch-up, for the channel's own figaro,
   directly after its tail, with no record skipped. Nothing else appends.
6. A row is a function of its record, the form history at that record (the
   bound form and the forms the figaro studied) and the fingerprint. The
   encoder reads no clock and no file. Two builds of the same history give
   the same bytes.
7. One check, before a channel is read or extended (below). A channel that
   fails is retired whole and built again from the IR. No row is cut,
   replaced or inserted.
8. One row set. The count, the check, catch-up and the request read the
   same rows through the same reader.
9. What a provider signs and requires back verbatim (a thinking signature,
   redacted or encrypted reasoning) is stored on the record. This is what
   makes 6 true for assistant records.
10. Reads and fork-point arithmetic use the data lineage: the nodes that
    hold a figaro's records, root first, bases never decreasing. The seat
    takes no part.

## Three relations, three names

- Owner of a channel: a trunk id, in the channel's header. Never an LT and
  never a position in another channel.
- Seat (the presentation parent): the figaro a figaro sits under. It is
  what `ls` draws and what `kill -r` and the reaper take. Since 0.63.0 it
  is `from` in the node marker when the move copies nothing, and a
  `@topology` override otherwise. Scopes and `DescendsFrom` walk `from`, so
  they follow the seat in the first case. It names a figaro and carries no
  LT. A seated figaro's own records can begin below its seat's: gluck has
  one such node today (the startup lead, base 6 under a seat whose own
  records begin at 21).
- Data lineage (the filesystem parent and its LT): for a node, the chain of
  nodes its records come from. It is derived from `from` and `base` by
  dropping every ancestor that a lower base beneath it eclipses, which is
  what the disk chain already does when it reads. A move is allowed only
  when every node between the old and the new parent has a base at or above
  the moved node's (`sameInheritance`), so all of them are eclipsed and the
  data lineage is the one the node had before the move.

My reading of Gluck's sentence: the seat is an identity and may be a figaro
whose history starts after the child's, so no LT belongs on it; every
operation that needs an LT uses the data lineage and behaves as it did
before 0.63.0. That is what 43e335b0 implements for reads. Forking was
already right: `ForkAt` finds the owner of the cut by walking up from the
leaf. If the sentence means seats should stop being storage moves at all,
see question 1.

## The channel

An xwal channel with a new property, `cache`: a fork creates no directory
for it, it is opened with no parent, and the crash repair, the move check
and detach skip it. New names, `translations-v3/<provider>`, so no v2
directory is ever read under the new rules.

Header, written once when the directory is made: trunk id, fingerprint.

The check, on every send, before anything is read or appended:

| fails when | reason logged |
|---|---|
| no header, or another trunk id | not this figaro's |
| header fingerprint differs from the encoder's | fingerprint |
| tail's index + 1 differs from the tail's LT | shape (a hole or a double) |
| tail's LT is past the IR's tail | ahead of the IR (crash) |
| hash of the record at the tail's LT differs from the tail's | another history (an LT reissued after a crash) |

A channel that fails is retired: the directory is renamed aside, the parent
directory synced, the old rows deleted in the background (normalize removes
what a crash leaves of them), a line logged with the reason and a counter
bumped. "Stale" is therefore not a state that lingers on disk: there is a
directory with a valid header, or none.

Catch-up, the only writer: read the tail, take the IR records after the
tail's LT, encode each in order, append unsynced, sync once, then build the
request. An absent channel is the same loop from LT 2. If the sync fails
the unsynced rows are discarded and the send fails. A crash mid-build
leaves a shorter channel that passes the check, and the next send extends
it. This is the single final sync the catch-up-sync lane (c1) is adding.

`doctor normalize` runs the full check: every row's position, hash and
fingerprint against its record.

Why the O(1) check is enough between sweeps: with one writer that only
appends after the tail, the rows a crash leaves behind are always a suffix,
and the first catch-up after it meets them at the tail. Today on-append can
put a fresh row after stale ones, which hides them from a tail check; that
is how the mixed fingerprints survived.

## Cases

Fork.
- Head fork: no channel. First send encodes the whole history. With
  invariant 9 the bytes equal the parent's rows, so the provider's prompt
  cache prefix is shared as it is today.
- Fork at `:N`, `:N.k` or `.LT`: the same, for the records up to the cut.
  A row from beyond the cut cannot appear, because nothing is read from the
  parent's channel.
- Turn-0 fork (`<lead>:1`, the name this lane gives it) seated under its
  lead: two or three birth records to encode.
- Seat, promote, demote, detach, delete of any relative: no effect.

Provider switch.
- To a provider this figaro never used: no channel; build.
- To one it used before: the channel is behind. Check, then extend from its
  tail. Being behind is not stale.
- To one its parent or seat uses (issue 90): the parent's channel is not
  consulted. Build.
- While a figaro runs on X nothing is written to its Y channel.

Restart and crash: nothing runs at boot. The check runs at the first send.

Fingerprint bump: every channel of that provider fails the check at its
next send and is rebuilt. No request can mix fingerprints.

## Invariant 9: signed thinking

Measured on a copy of the small store (69 figaros with anthropic rows,
31,382 assistant records): of the 6,083 assistant rows under the current
fingerprint, 5,103 are byte-identical to a fresh encode of their IR record
and 980 are not. Every one of the 980 has signed thinking blocks that the
fresh encode lacks, and the same blocks otherwise by type. The IR keeps the
thinking text and drops the signature (`decodeNativeMessage`), so the
encoder cannot replay it. The channels hold 2,351 signed blocks; a
signature averages 1,942 bytes (4.6 MB in all).

So the row is today the only home of the signature, and every rebuild
(a hole cut, a clear, and under this design every fork) silently drops
earlier thinking. On models that keep earlier thinking in context this
changes what the model reads and breaks the provider's cache prefix at the
first such block: a fork would pay a cache write for nearly its whole
context. Inside a tool loop it can be refused: with thinking on, the
assistant message being continued must open with its thinking block.

Rule: an assistant record keeps, per thinking block, what its provider
signed, tagged with the provider namespace that may replay it. The encoder
emits it when the namespace matches and omits the block otherwise, as now.
Then the provider's separate assistant row (`commitAssistantCache`) is
redundant: catch-up produces the same bytes from the record.

Records written before this have no signature in the IR. Their thinking is
not replayed once their figaro's channel is built under the new rules. That
loss happens once, at the upgrade, and is the same loss a clear causes
today. I did not check the copilot providers for the same pattern
(encrypted reasoning items); the rule covers them, the measurement does
not.

## Normalize

`figaro doctor normalize [-n]` gains, per figaro:

- the full check of the channel for its current provider; build it if it is
  absent or fails (reaped figaros are skipped);
- the full check of its other channels; retire the ones that fail, keep the
  ones that pass;
- delete the directories of `translations/*` and `translations-v2/*` and
  drop those channels from the manifest (timing: question 3);
- compare the tree cache's read of the figaro's IR with the disk chain's.
  Every rebuild trusts that read.

A figaro with a turn in flight is skipped and reported.

`-n` prints, per figaro and provider, the state, the rows owed and the
bytes that would be freed. The lazy path is the send; the sweep is for
disk and for finding damage before a turn does.

## The crash repair

`repairCoherentCuts` runs when the store was not closed cleanly. For every
node without children it trims, in every other channel, rows keyed past the
node's IR tail. It skips nodes with children. In the flat layout a head
with children is still appended to, so on those heads (203 live on gluck,
70 with translation rows) a row for a record that never landed survives.

Under this design the repair does not visit cache channels:

- gluck's manifest has 11 channels beside the IR, 9 of them translation
  channels. The repair's walk (17,688 directory visits in the startup
  lane's measurement: 1,474 nodes by 12 channels) drops to a quarter: the
  IR, the form and `turn-wal`.
- The gap closes for translation rows without touching the repair's rule:
  a row past the IR's tail, or at an LT a different record now holds, fails
  the check at the first send, on a head with children as on any other.
- What is left of the gap is the form channel and `turn-wal` on heads with
  children. That is the narrow-repair lane's (b2), not this design's.

## Migration

Nothing is converted. v2 directories are not read and not written by the
new code. Each figaro builds its v3 channel at its first send after the
upgrade. v2 stays on disk until normalize deletes it.

Seats need no migration under my answer to question 1. The one node on
gluck whose base is below its parent's (n2759) reads correctly with
43e335b0.

## What breaks

- Disk format: a new channel namespace and a `cache` property in the xwal
  manifest. An older daemon ignores the property and writes no rows under
  the v3 names; a directory it makes there by forking has no header and is
  not a channel. After a downgrade it finds its v2 rows behind and catches
  up; if normalize already deleted v2 it rebuilds them at its old cost.
- The first send of every figaro after the upgrade encodes its history.
- Signed thinking from before the upgrade is not replayed (above), and the
  provider's prompt cache misses once per figaro that was mid-conversation
  at the upgrade.
- Forks no longer share rows on disk. gluck today: 513.9 MB of anthropic
  rows over 518 nodes, 218 of them inheriting by position. If every one of
  those 518 woke and built its own channel: about 855 MB. Only figaros
  that take a turn build one. Deleting the old namespaces frees 1.25 GB
  (the IR is 0.95 GB).
- A move between nodes, a slice and a snapshot need not carry channels.
- `skills/figaro/contributing/translation-lineage.md` is rewritten: its
  required behaviour ("one physical prefix shared by every descendant") and
  its first rejected design are reversed by the ruling.
- Removed code paths: translate-on-append, the hole cut (`firstGap`,
  `TruncateFrom` on a channel), `ClearStaleTranslationCache`, the
  per-channel rebase for translation logs, `commitAssistantCache`.

## Cost

| | measured | source |
|---|---|---|
| encode + image fit + hash for a whole history, CPU, no disk | p50 33 ms (1,182 records), p90 50 ms (2,156), max 120 ms (724 records, 20 images, 11 MB of rows); 65,160 records of 77 figaros in 2.4 s | small-store copy, this lane |
| one row appended with its own fsync | 14.4 ms, 98.6% of catch-up | startup-4, n=1 |
| 334 rows, one sync at the end | 34 ms against 4,864 ms | startup-4, n=1 |
| IR growth from signatures | about 2 KB per thinking block | small-store copy |

A rebuild is affordable only with the single sync. c1 lands first.

The turn path gets cheaper: today an input record costs an fsync per
provider channel (on-append) and an assistant record one more; after, a
request costs one sync for the rows it adds. Each step ships with a
before and after of catch-up on the send path.

Not measured: the big store; histories with more than 126 images.

## Tests

Each defends an invariant and is shown failing first.

- 2, 4, 8: a model test. Random sequences of turn, provider switch, head
  fork, interior fork, seat, detach, delete, fingerprint bump, restart, and
  a crash that loses the IR's unsynced tail. After each step, for every
  figaro and provider with a channel: the rows equal a fresh encode of that
  figaro's history, and `Len`, a scan and the request's rows are the same
  set.
- 5, finding (a): turns on X leave Y's channel untouched; a switch to Y
  yields one row per record.
- 3, 7, finding (b): after a fingerprint bump the next request carries rows
  of one fingerprint only.
- 8, finding (c): the masked shape (a cleared channel under an ancestor
  with a higher base) cannot be built; on v2 data the read fix is pinned by
  the store test in 43e335b0.
- Issue 90: the switch to the history parent's provider across a restart
  (written; fails on 86233533 with "got 5, last 8"), and a fork's first
  turns on its parent's provider.
- 6, 9: a provider reply with signed thinking, redacted thinking and tool
  calls encodes from its IR record to the bytes the provider would have
  cached; a fork's first request equals its parent's request up to the cut.
- 7: a crash between the rows and the sync leaves a channel that passes the
  check; a row past the IR's tail on a head with children is retired at the
  first send.
- 10: the four fork cases of this lane's brief (written, in 43e335b0 and
  9e639742), and a property test that the tree cache's split names the same
  owner for every LT as the disk chain, on random lineages.

## Order of work

0. On `lane/fork-turn0` now, gate queued: 43e335b0 (the tree cache reduces
   a lineage to the nodes a leaf can read before splitting; both splitters)
   and 9e639742 (a fork cut at a coordinate takes its parent's provider and
   model). `<id>:1` is named the turn-0 fork; no new coordinate. The issue
   90 test joins them. Builders are blocked on this, so it could go out as
   a patch ahead of the rest.
1. c1, the single sync (startup-catchup-sync lane).
2. Invariant 9: the signature on the record, encoder emits it, fingerprint
   bump.
3. Cache channels: the xwal property, v3 names, header, check, the one
   writer and reader; on-append, the hole cut and the assistant row removed.
4. The repair skips cache channels; normalize sweeps.
5. One release later: normalize deletes the old namespaces.

## Considered and not proposed

- Keep sharing rows along the data lineage, as before 0.63.0. The masked
  forks are unmoved forks: sharing by position breaks whenever a channel on
  the chain is cleared or regrown, and rebuilding is now the rule.
- Share immutable generations: a rebuilt channel becomes a new generation
  and children keep pointing at the old one. Sound, but it needs reference
  counts and collection of old generations to save at most 340 MB of
  anthropic rows on gluck and 33 ms per fork.
- Copy the parent's rows at fork. A second writer, and after invariant 9 a
  build gives the same bytes.
- Keep on-append for the current provider only. Still a second writer, and
  it is what lets stale rows sit behind a fresh tail.
- A durable stale marker with a later repair. Retiring by rename is the
  mark, and it cannot be half done.

## Questions for Gluck

Attributed to feigned-molecule; separable from the proposal above.

1. Seats. I propose leaving seats as 0.63.0 made them (`from` is the seat
   when the move copies nothing) and deriving the data lineage, so
   authority, `kill -r` and the reaper are untouched, and f396ac18's
   reasons still hold (a turn-0 fork follows its role on succession and is
   not reaped with the first holder). The other reading of your sentence is
   that a seat goes back to being only a drawn edge and `from` always names
   the node that holds the cut. That makes `DescendsFrom` and scopes follow
   the first holder again unless they are moved onto the drawn edge, which
   is an authority change for El Inspector. Which?
2. Invariant 9. Put the signature on the IR record (proposed), or accept
   that every fork and rebuild drops earlier thinking?
3. Delete `translations/` and `translations-v2/` in the same release, or
   one release later so a downgrade keeps its rows (proposed)?
4. The sweep. Build channels for every figaro that is not reaped
   (proposed), or only retire what fails and leave building to the next
   send?

## Not verified

- El Buscador's exact error is explained, not reproduced.
- Issue 90's first sighting (v0.63.0, a gateway provider) has the same
  signature and falls in the release that began moving forks. I reproduced
  the error with a seated fork, not that figaro's state.
- The copilot providers' sealed data.
- startup-4's numbers are single runs; its big-store census is still
  queued.
