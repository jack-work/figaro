# Translation Cache Lineages

Translation channels are parallel XWAL channels, not per-aria copies. A fork
shares the same immutable IR and translation prefix locations as its parent;
each child directory contains only its divergent suffix and fork metadata.

## Required XWAL behavior

Adding `translations/<provider>` after a lineage has forked must add the
channel to that lineage without copying or backfilling inherited records.
Existing and future descendants resolve the shared prefix through the same
fork tree as the main channel.

XWAL owns:

- immutable decoded channel snapshots and suffix views;
- the translated-through main-LT watermark;
- indexed main-LT lookup and bounded tail views;
- ordered duplicate-key lookup and reducible state at a main-LT watermark;
- atomic publication of a durable append as the next snapshot;
- one physical prefix shared by every descendant.

Figaro should not place another full-history cache or lock around these views.
Provider translators retain only their fingerprint, chalkboard state at the
watermark, and provider-native input projection.

## Catch-up

For a matching fingerprint:

1. Read the translation tail watermark.
2. Read the immutable IR suffix after that watermark.
3. Encode and append only that suffix.
4. Advance the in-memory projection and watermark.

Normal work is one or two messages regardless of aria length. Fingerprint
changes explicitly clear and rebuild the translation lineage. Request
serialization may remain proportional to prompt bytes; cache validation,
translation lookup, and catch-up may not scan the prefix.

## Rejected designs

- copying or backfilling a parent's translation records into a child;
- predeclaring a fixed list of provider channels;
- one `Lookup` per historical IR message;
- rebuilding chalkboard state from the beginning on each turn;
- treating Figaro's `cachedLog` as the durable hot-state owner.

These either duplicate prefix bytes, make provider registration static, or
move XWAL responsibilities into Figaro. None changes the existing on-disk
record format: recovery still derives hot snapshots and watermarks from the
canonical fork tree.
