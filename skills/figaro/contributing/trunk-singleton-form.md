# The trunk state, as a singleton form

**Status: BUILT** (`feat/incantations`, phase 8). `internal/trunk` and
`trunks.json` are gone; the hierarchy is `store.TopologyTree`, a form on a
reserved stump. Read this for the WHY; read `internal/store/topology_form.go`
for what shipped, and the differences below before changing either.

## What shipped, and where it differs from this design

- **The home is a reserved STUMP** (`@topology`), not an unbound form node.
  A stump is the one node figwal names by a string the caller chooses, so
  the form needs no marker file and no lookup, and it cannot be forked or
  bound. An unbound form node would have needed client-specified trunk ids,
  which figwal does not have yet (and which Gluck wants for other reasons:
  see `plans/answers-forms.md` §2). `listStumps` filters it out, or the
  hierarchy grows a row describing itself.
- **Retention is NOT built.** This document asks for a single rewritten
  segment, which needs figwal's compacting channel (phase 7). Until then the
  topology form keeps every record, which is correct and unbounded. A
  promote is rare, so the growth is slow, but it IS growth: whoever builds
  phase 7 should point it here first.
- **The migration folds `trunks.json` in on first open** and renames it to
  `trunks.json.migrated`. Ordering, not a journal: the fold is idempotent and
  a form that already holds edges is never migrated into again.
- **Parity is checked, not claimed.** `internal/trunk`'s own test suite was
  ported verbatim onto the replacement: same diagram, same claims, same
  names, in `internal/store/topology_form_test.go`.

## The claim

The presentation hierarchy is durable, versioned, reducible state with one
writer. That is the definition of a **form** in this project
([forms-design.md](forms-design.md)). It is currently a hand-rolled JSON
file instead, with its own atomic-rename dance, its own version field, and
its own crash story. The file is the odd one out, and every property it
wants, the form primitive already has.

So: **one unbound form per store, holding the hierarchy.** One per store
means one per angelus, because the angelus is the single process that owns
the store. It is a singleton by the same argument that makes the daemon a
singleton, not by a lock.

## The document

Folded, this is the whole state:

```jsonc
{
  "version": 1,
  "parent": {                  // aria id -> the aria it is DRAWN under
    "86d12409": "01efd291",
    "e2a75c6b": "86d12409"
  }
}
```

An aria absent from `parent` is drawn where its history puts it. The map
holds OVERRIDES, never a full tree, so a lost document degrades to the
truthful default rather than to a wrong one. Keep that property: it is the
same reason figwal can rebuild its index from markers.

The patch stream, one record per edit, folded by the existing form reducer
(`formReduce`; a null value deletes a key):

```jsonc
{"parent": {"86d12409": "01efd291", "01efd291": "86d12409"}}   // one promote
{"parent": {"86d12409": null}}                                 // a forget
```

A promote is two keys in one record, which is also what makes it atomic:
today's two-edge write is one `save()` of a whole file, and the pair must
not be able to half-land.

## Where it lives

The store's genesis root already hosts named depth-one children. The
hierarchy form is one more of those, minted on first use, under a reserved
name (`@trunks`), never forked, never bound, never listed as an aria. It
must be excluded from `Conversations()` the way form nodes already are, and
from `ls -g`'s form rows, or the listing grows a row describing itself.

## What figwal must add

Nothing about correctness. Everything about retention.

Every other channel in the store keeps history because history IS the
value. Here only the fold is: nobody will ever ask what the hierarchy
looked like on Tuesday. Left alone, this channel grows one record per
promote forever, and every daemon start replays them all to answer a
question two map writes could have answered.

The smallest thing that fixes it:

- a per-channel option, `Compact` (or `SingleSegment`), meaning: after a
  write, if the segment holds more than one record, rewrite it as the
  folded document alone.
- the rewrite is the atomic-replace figwal already performs for segment
  rolls: write a temp segment, fsync, rename, fsync the directory. The
  reducible channel already knows how to produce a fold, so the payload is
  free.
- the crash story is unchanged from a roll: either the old segment or the
  new one is there, and both fold to a correct document.

Do NOT reach for this by truncating in place. A truncation that is
interrupted is a torn record, and the reason a segment log is safe is that
it never rewrites bytes anyone might be reading.

The knob figwal already has and figaro should keep inheriting:
`IdleUnload` (default five minutes, `xwal/store.go`) evicts a lineage's
in-RAM head. A one-segment document with a handful of keys is not the
thing to tune it for.

## What it buys

- One durable-state mechanism instead of two.
- Crash safety and atomic replace from the WAL, not from a bespoke
  `save()`.
- `figaro form @trunks` inspects it; the form family's verbs already read
  and fold it.
- A home for the next store-global fact, which today would become
  `something-else.json` beside the first one.

## What it must not break

- **Forking never consults the hierarchy.** `internal/topo` says it, and it
  survives this change unaltered: a presentation edge must never decide
  where data comes from.
- **The load path cannot deadlock.** `internal/trunk` keeps its lock around
  the map alone, because resolving an edge asks the store for a topology
  snapshot and the rebuild reads the edges back. Reading the hierarchy from
  a form makes that loop tighter, not looser: the form lives in the same
  store whose snapshot it feeds. Resolve the document ONCE at open, keep it
  in memory, and write through; never read it on the snapshot path.
- **An unsound edge stays refusable.** `topo.Present` drops an edge naming
  a missing aria or closing a cycle. A document is not more trustworthy
  than a file just because a WAL wrote it.

## Migration

Read `trunks.json` at open when the form has no records yet, write it as
one patch, and leave the file alone (do not delete it: an older figaro
sharing the store still reads it). Drop the file read a release later.
