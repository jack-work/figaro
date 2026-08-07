# `figaro health` — a service-health verb

**Status: not built.** Agreed as the next piece of work after the hibernation
branch merges. This file is the brief, not a description of anything that exists.

## Why it is worth building

The hibernation work added four reclamation mechanisms and five configuration
knobs, and the only way to see any of them is `figaro doctor mem`, which was
written as an instrument for one investigation and shows what that investigation
needed. Someone tuning `[memory]` on their own machine currently has to read the
source to know what to look at.

There is also a question `doctor mem` cannot answer at all and which is the one
people actually ask: **what has this been costing me?** Token usage over days,
split by input, output, cache-write and cache-read, is the single most
frequently wanted number in the whole system and it is nowhere on the CLI.

## Shape

```
figaro health              short: one screen, the numbers that matter
figaro health -l           long: everything, per-aria tables
figaro health -j           machine-readable, the full long payload
figaro health --days 30    widen the usage window (default 7)
```

Non-interactive. Prints and exits — no alt-screen, no keys, no pager. It must be
safe to pipe, safe to run in CI, and safe at any terminal width, which for this
codebase means: **read the width, do not assume 80, and degrade by dropping
columns rather than wrapping them.** The `SUSANNA-table-wrap` proposal in
`proposals/` covers the wrapping rules already learned the hard way.

`-j` should emit the LONG payload regardless of `-l`, because a machine has no
use for an abridgement.

## What to report

Grouped as the sections should print.

### Daemon
- uptime, pid, build revision
- goroutines, GC count
- heap alloc / inuse / sys, total sys, and the armed `GOMEMLIMIT`
- pprof socket path when armed

All of this exists in `rpc.MemStatus` today.

### Arias
- total on disk, and the split: **live / dormant**

  Note the vocabulary trap. Today `figaro status` reports three states —
  `dormant | idle | active` — where `dormant` means "not in memory" and
  `idle`/`active` are both live. There is no separate "hibernated" state and
  there should not be one: an aria reclaimed by the sweep IS dormant, and
  inventing a fourth word would make the sweep's effect look like a different
  kind of thing than a cold start. If a user wants to know "was this reclaimed
  or never loaded", that is a *reason*, not a state, and belongs in the long
  view as `last_reclaimed_at`.
- how many have an open endpoint, and how many connected terminals across them.
  Already in `MemStatus` as `Endpoints` / `AttachedClients`. Worth showing
  prominently, because *endpoints with clients and no agent* is the state
  hibernation exists to produce and the one that most needs to be legible.
- resident aria handles, resident IR rows, resident IR bytes
- background sessions

### Per aria (long view only)
One row each. Sorted by cost descending, since that is the question being asked.

| column | source | notes |
|---|---|---|
| id, mantra | `AriaMeta` | mantra truncated to fit |
| state | registry + store | live / dormant |
| messages, turns | `AriaMeta` | |
| context tokens / limit | `AriaMeta` | with the % |
| **allocated bytes** | see below | the hard one |
| **disk bytes** | sum of the aria's channel segments | needs a store helper |
| last active | `AriaMeta.LastActiveMS` | |
| terminals | `ariaHub.Attached()` | per-hub, already tracked |

**Allocated bytes per aria is the hard column and must not be faked.** Go gives
no per-object-graph accounting, and the two honest options are:

1. **Estimate, and label it an estimate.** `cachedLog.ResidentBytes()` already
   exists and is calibrated: entries are sized from
   `Entry.EncodedBytes * irDecodeInflation`, with the constant (5) derived from
   measuring two real arias at 4.0x and 5.3x. Extend the same treatment to
   translations and the chalkboard and the number is defensible to within ~25%,
   which is what `TestRealAriaMemory` measured. Print it as `~12.4 MiB`.
2. **Say nothing.** Better than a number that looks exact and is not.

Do (1), with the tilde, and do not drop the tilde later.

The composed UI (`aria.Server`) is the other resident holder, at ~0.2x the
decoded IR on real arias. It only exists while an agent is bound, so a dormant
aria's row correctly shows less — that is not a bug and the long view should
make it obvious rather than surprising.

**Disk bytes needs a new store method.** `XwalStore` knows each aria's node
directory; summing segment file sizes per channel is a `filepath.WalkDir`. Break
it out by channel in the long view — the IR / translations split is genuinely
interesting, because a provider switch in an aria's lineage leaves a full second
wire projection on disk (measured: 1.2 KiB on one real aria, 6.0 MiB on
another).

### Token usage over time

The section people will actually run this for.

```
                 in        out    cache-w    cache-r       cost
  today      12.4k      3.1k      88.2k     1.42M      ...
  7 days    104.9k     28.7k     612.0k    18.30M      ...
```

**The data problem, stated plainly.** `AriaMeta` carries `TokensIn`,
`TokensOut`, `CacheReadTokens` and `CacheWriteTokens` as **lifetime totals per
aria**, with no time dimension. So today the only honest daily series must come
from the IR itself: every assistant message carries `message.Usage`, and every
message has a turn and a logical time. Folding usage per day means walking IR —
which is exactly the full-log walk the windowing work spent effort removing.

Three options, in order of preference:

1. **A usage sidecar, appended per turn.** One line per turn:
   `{ts, aria, in, out, cache_r, cache_w, model}`. Cheap to append at the point
   `publishMetadata` already runs, trivially aggregable by day, and it makes the
   series available without touching an aria's IR at all. It is also the only
   option that survives an aria being deleted, which matters: spend does not
   become untrue because a conversation was removed.
2. **Fold from IR on demand, bounded.** Correct with no new writes, but O(all
   messages in all arias) for a 30-day window. Acceptable for `--days 1`,
   painful for 30, and it re-residents everything the window just evicted.
3. **Per-aria lifetime totals only, no time series.** What is possible today.
   Ship this in the short view immediately, and do not pretend it is a series.

Recommend (1). There is a precedent for the shape — `metrics.jsonl` sits beside
`logs.jsonl` in the state dir — but **it will not do the job as it stands**:
inspected on a real store, the only metric it carries is
`figaro.request.duration`. So this is a new append, not an aggregation over
something that exists. It is still small: one line per turn at the point
`publishMetadata` already runs.

### Configuration echo

Print the `[memory]` values in effect, with defaults marked. A user tuning knobs
should not have to guess whether their file was read — during the hibernation
fuzz an entire run was wasted on a config directory that lacked `credo.md`, and
the failure surfaced as a loadout error rather than a config one.

Also print, when true, the two conditions that mean reclamation cannot work:
`dormant_after_minutes = 0` (disabled), and every resident aria having a live
agent (nothing for eviction to reach). `doctor mem` already prints the second;
it belongs here instead.

## What `doctor mem` becomes

Delete it, and fold what it shows into `health`. It was scaffolding for a
measurement; two overlapping views of the same counters is one more thing to
keep honest. Its `-j` consumers are zero — it shipped on this branch.

## Sequencing

1. `figaro health` short + `-j`, over `rpc.MemStatus` and `AriaMeta`. No new
   data, so it is mostly rendering.
2. Disk bytes per aria (new store method) and the per-aria long table.
3. Decide the usage-series question above, then the token section.
4. Remove `doctor mem`.

Steps 1 and 2 are worth having on their own, which is the test of whether this
is sequenced right.
