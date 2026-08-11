# Streaming tool arguments: what was built, and what is still open

Commission of 2026-08-04/05. The work is in the branch; this file is only the
part that outlives it: the measurements that justified the design, and the
items deliberately not built.

The rendering rules themselves live where they belong -
[reference/ui-stream.md](../skills/figaro/reference/ui-stream.md) for the block
and [reference/architecture.md](../skills/figaro/reference/architecture.md) for
`system.eager_tool_streaming`.

## The measurements that decided things

**Tool arguments were never streamed to the screen.** The deltas arrived and
were accumulated (`Agent.argPartials`) but the UI IR had no field to carry a
partial input, so `compose` could only leak one tool's one argument into the
*output* channel. On the wire: the tool node was created bare, and 25.4 s later
`args` and `summary` arrived in a single `set`, whole. Prose in the same
capture arrived as `patch` splices.

**The provider does stream them, finely.** 836 `input_json_delta` events,
median 7 bytes, for one 5.6 KB argument: but Anthropic **buffers each
parameter value** until it is complete, which is documented behaviour and the
reason a large write arrives in one lump. `eager_input_streaming` (GA) turns
that off; the Copilot Anthropic-dialect endpoint rejects the field outright, so
the copilot provider drops it whatever the chalkboard says.

Measured on three routes, sampling the wirelog tee as it was written:

| route | events | arrival |
|---|---|---|
| api.anthropic.com | 681 | 8 at t=0, 673 at t=25.4 s |
| copilot enterprise | 770 | 9 at t=0, 761 at t=27.7 s |
| anthropic, thinking off | 836 | 7 at t=0.5 s, 829 at t=28.2 s |

Control that exonerates the instrument: `text_delta` on the same route, same
tee, same sampler: 68 distinct 100 ms buckets, median gap 0.50 s.

**One of those routes was our bug, not theirs.** On the Copilot Responses route
the upstream streams perfectly (1147 events, median gap 0.1 s) and figaro
forwarded **zero** of them: the proxy re-encrypts `item_id` on every event, so
the lookup missed every delta while `output_item.done` still delivered the
arguments whole: nothing looked broken. Fixed by falling back to
`output_index`.

**Durations.** 312 of 477 tool headers in a real aria lost their duration at
width 80 (the summary was 80 columns and the duration came after it), and 0 of
477 historical tool nodes carried a timing at all.

## Still open

**1. Historical durations are not persisted.** `OpenedAt`/`StartedAt`/
`FinishedAt` live in the Projector's in-memory map, so `figaro show` and the
pager over old turns have no timings to draw. The owner has agreed they should
go into the fig IR and the durations be derived from them. Not started; it is a
storage change, not a rendering one.

**2. Focal-point retention on Ctrl-O.** `Enter` anchors the viewport below the
change (`anchorBelow`), so the content the reader was looking at holds its
screen row. Ctrl-O does not, and it changes every block's height at once.

**3. `figaro replay` re-wraps a tape recorded at a different width.** A tape
carries its recording geometry; replaying into a wider pane re-wraps the rows
and can drop a character at a seam. The renderer is lossless at every width
(proved in-process, decorated rows included); this is replay's own geometry
handling. Pre-existing.

**4. `Enter` on a node with nothing to reveal is silently inert.** Tools always
toggle now. Prose and thinking have no collapsed form, so the key does nothing
and says nothing. A one-line footer notice would close it; it needs a decision
about whether that is noise.

## Instruments worth reusing

- `figaro send -v` with shell timestamps: the aria wire, per frame.
- `FIGARO_WIRE_DIR` plus a file-size sampler: provider bytes with arrival
  times (HTTP routes only; the copilot Responses route is a websocket and
  wirelog cannot see it).
- `figaro listen --record` / `figaro replay`: the UI without a provider.
- `.#snapshot` with `FIGARO_STATE_DIR` overridden onto `/var/tmp`, plus
  `touch $FIGARO_STATE_DIR/.snapshot-taken` to skip the 138 MB reseed: real
  config and real credentials, an empty isolated store.
