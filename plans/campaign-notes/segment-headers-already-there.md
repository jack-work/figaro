# The header snapshots are already on disk, and nothing reads them

By aria 7e151902 (role @980dc16c), 2026-08-18. Measured, not read: the
probe and its counts are below, and they falsified a reading — mine and
the executor's — that would have sent stage 2 to write a mechanism that
exists.

## THE QUESTION

Part II of plans/delta-seam.md rests on: "every segment carries a HEADER
SNAPSHOT, always brought into memory alongside the segment itself", and
from that, THE ONE-SEGMENT BOUND — any snapshot request costs at most one
segment's worth of patch application, never a walk from the beginning.

Aria 6defe6f9 checked whether figwal supports that and reported, honestly
and with the caveat "this is a grep, not a proof", that it does not: the
segment codec's `headerSize` is 8 bytes, a per-RECORD length prefix; the
reduced state lives in per-baseIndex WATERMARK FILES
(`<chDir>/<baseIndex>.jsonl`) written by `ensureWatermark` /
`rewriteWatermark` at channel creation, at recovery and at fork backfill —
**and at no rotation**. If that were the whole story, a snapshot request
on a long unforked channel would fold from the nearest fork base, i.e.
the whole history, and the bound would be false as written.

That reading of the watermark files is CORRECT. It is not the whole story.

## THE SECOND MECHANISM, under the same roof

figwal v0.18.0 (the version go.mod pins) also has opaque BLOCK-0 SEGMENT
HEADERS, which are a different thing from the 8-byte record prefix:

    disk/log.go:44        Options.OnSegmentOpen puts a log in HEADER MODE
    xwal/xwal.go:356,1004 channelOpts wires EVERY ChannelReducible to
                          reducibleFold — so figaro's form channel is in
                          header mode today, on every aria on disk
    disk/log.go:634-668   openActiveLocked calls OnSegmentOpen(prevHeader,
                          sealedPayloads) and segment.WriteHeader on every
                          segment creation, which is every rotation
    segment/segment.go    hasHeader / OpenHeadered / WriteHeader / Header
    disk/log.go:510       StateAt(idx) folds that header with the entries
                          [segBase..idx] and nothing earlier

## THE MEASUREMENT, because reading is what put both of us wrong once

Probe: /var/tmp/fig-hdr-probe (own module, figwal v0.18.0 from the module
cache — the pinned version, NOT the /home/gluck/dev/figwal checkout, which
is ahead). jsonl codec, 4 KiB segments, 400 patches → 8 segments, a
COUNTING reducer: every patch application increments a counter. Counting,
not timing, because the question is "how many times".

    folds during the 400 appends            0   rotation folds are deferred
                                                to flush
    FIRST StateAt after those appends     400   the deferred rotations
                                                settling: each record is
                                                folded ONCE, ever
    every subsequent StateAt               44   = idx - segBase + 1, checked
                                                at idx 400, 300 and 200
    StateAt(1)                              1
    Close + REOPEN, FIRST call             44   the bound holds COLD, from
                                                disk

THE ONE-SEGMENT BOUND HOLDS TODAY, with no figwal change.

A NOTE ON THE 400, because it is this campaign's own rule applied to me: a
first-touch count of 400 BEAT ITS OWN BOUND, and a number that beats its
own floor is a suspect first. The first probe ordered the requests
{400, 200, 1} and I nearly reported "the bound fails at the tail". Running
each index TWICE, and reordering them, showed the 400 belongs to the first
request of any kind and not to any index — deferred write-side work, once
per record over the log's life, not read-side cost per request.

## THE CONSEQUENCE FOR STAGE 2

  1. THE WRITE SIDE IS ALREADY PAID. Headers are being written for every
     form segment in the owner's store right now, and NOTHING IN FIGARO
     EVER READS THEM: `grep -rn "StateAt|HeaderAt" internal/` has zero
     non-test hits. Stage 2 is a READ that was already provisioned for.
  2. THE DERIVED LOG IS A MEMO, NOT A PERSISTENCE LAYER. figwal re-folds
     from the header on EVERY StateAt; it keeps no memo of the snapshot it
     last built. The design's worked example (LT 15 then LT 17 costs 6
     then 2) needs the memo, and the memo is the whole of stage 2.
  3. DO NOT FOLD THROUGH formReduce. internal/store/xwal_store.go:166
     unmarshals the state, applies one patch and marshals it back, PER
     RECORD; its own comment measures 97us decode and 76us encode on a
     15KB board. figwal's StateAt pays that per patch. Take the header
     BYTES, unmarshal ONCE, fold the segment's patches DECODED through
     form.Fold. The count to assert is UNMARSHALS PER SNAPSHOT REQUEST
     == 1, not N.
  4. THE GAP, verified before anyone was told to route around it:
     xwal.Store exposes StateAt but NOT HeaderAt. HeaderAt and
     SegmentBaseIndexes live on disk.Log, and xwal reaches them only for a
     count in its stats. So (3) needs a SMALL ADDITIVE FIGWAL CHANGE —
     expose what exists at the store level — rather than the new
     per-rotation watermark the first reading called for.

## THE PROPERTY NOBODY HAD WEIGHED

Once the header half of a snapshot is folded by figwal (formReduce,
through JSON) and the tail half by figaro (form.Fold, decoded), EVERY
SNAPSHOT IS ASSEMBLED BY TWO IMPLEMENTATIONS. They agree only if
form.Snapshot's MarshalJSON/UnmarshalJSON round trip is EXACT — nil vs
empty, key sets, unknown fields, ordering.

fold-from-header == fold-from-zero can PASS on a poor fixture while the
round trip quietly drops a key no fixture carries. So the oracle the plan
names is necessary and not sufficient: a rich-corpus round-trip identity
belongs beside it. Failure here is silent and gets STORED, under a
fingerprint asserting the bytes are right.
