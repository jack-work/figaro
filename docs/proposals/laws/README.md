# Figaro laws

Status: proposal, revision 1, 2026-11-18. Nothing is built. Asked by the King
for Gluck. Written by the laws lane (figaro `54fc120b`, a fork of the Figaro
Engineer) against figaro v0.74.1
([`adccceea1`](https://keel.kelliher.info/_keel/jack-figaro/diff/adccceea199a884cf3cbcbb5bb58a3da9fe87e81)).
It is written to fit the
[deployment modes proposal](https://keel.kelliher.info/_keel/jack-figaro/blob/1c0dff4cca0925f3705d4b95ea6730d1257e94ed/docs/proposals/deployment-modes/README.md?view=preview)
(branch `proposal/deployment-modes`, revision 1 at `1c0dff4cc`) and the
revision Gluck asked for: house mode on Merlin first, the same `figaro`
binary serving `figaro valet`, every figaro allowed by the valet by default,
and the valet's socket as Gluck's kill switch.

Three parts, each shippable alone:

1. [Laws](#part-1-laws): rules Gluck writes that figaros read and can never
   change.
2. [The fan-out quota](#part-2-the-fan-out-quota): a budget of live
   descendants handed down the tree, replacing `system.fanout.max_children`.
3. [The crypt](#part-3-the-crypt-reaped-figaros-kept-by-size): reaped
   figaros kept for audit, bounded by size instead of by age.

## Names used here

| name | what it is |
|---|---|
| **law** | A named piece of text that Gluck writes and every figaro it applies to reads, rendered by the daemon where no key, study or tool output can shadow it. A law binds the model. It is never a key the daemon enforces. |
| **law source** | Where a law is written: the troupe policy, the node policy, or a role form. Each law is named by its source, for example `law:node/no-em-dash`. |
| **sealed laws** | A house mode setting under which laws come only from the deployed config. Not even Gluck can change one at runtime; only a deploy can. |
| **the owner** | As in the deployment modes proposal: the principal holding every verb. Gluck at his own terminal. |
| **a tool caller** | Any process started by a figaro's tool. The **peer caller check** (below) recognises one by its cgroup and never treats it as the owner. |
| **fan-out quota** | `system.fanout.quota`, the most live descendants a figaro's subtree may hold, not counting itself. |
| **charge** | What a child costs its parent's quota: one for itself if live, plus its own quota (or, if it has none, the live figaros under it). |
| **free budget** | A figaro's quota minus the charges of its live children. What it may re-grant. |
| **reaped figaro** | A figaro the ttl sweep has soft-deleted (`figaro reaped`). The brief calls these tombstones; this proposal does not, because `system.tombstone` already names something else in figaro (a closed form, [`internal/store/form_tombstone.go`](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/internal/store/form_tombstone.go)). |
| **the crypt** | A new directory beside the live store that holds reaped figaros, compressed and without their wire cache, until a size cap evicts the oldest. |

Sources read, at v0.74.1 unless linked otherwise:

- protected keys:
  [api/form/protected.go](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/api/form/protected.go)
- owner-held keys:
  [api/form/owner_held.go](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/api/form/owner_held.go)
- fan-out limits as shipped in v0.74.0 and v0.74.1:
  [skills/figaro/reference/fanout.md](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/skills/figaro/reference/fanout.md?view=preview)
- authorization, owner-held keys, figaro scopes, the credo, troupes:
  [skills/figaro/reference/architecture.md](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/skills/figaro/reference/architecture.md?view=preview)
- roles, study, succession, `@reaped`:
  [skills/figaro/reference/forms.md](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/skills/figaro/reference/forms.md?view=preview)
- store layout:
  [skills/figaro/reference/store.md](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/skills/figaro/reference/store.md?view=preview)
- reminder rendering:
  [internal/provider/anthropic/anthropic.go](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/internal/provider/anthropic/anthropic.go)
  (`renderPatchBlocks`), and the same in the gemini, openaichat and
  responses providers
- daemon-side succession placement:
  [internal/angelus/succession.go](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/internal/angelus/succession.go)
  (`placeBeside`)
- the fan-out re-review that found the per-level growth:
  `/var/tmp/rereview-fanout.md`, finding R2
- captains-log, under `/home/gluck/.captains-log/`:
  `2026/11/17/fanout-limits-proposal.md`, `2026/11/17/fanout-limits-lane.md`,
  `2026/11/17/bundle-c-and-train2.md` (the mint-default entry, with the peer
  pid estimate), `2026/11/18/valet-prior-art.md`

## The peer caller check, which two parts lean on

Today a call on the CLI door (`angelus.sock`) is the owner unless it carries
a figaro label (`FIGARO_ARIA`), and a tool can drop the label. This is figaro
issue [jack-work/figaro#57, "an unboxed aria's tools reach the daemon as the
owner"](https://github.com/jack-work/figaro/issues/57).

The **peer caller check** closes it by asking the kernel instead of the
environment: the door reads the caller's pid (`SO_PEERPIDFD`, falling back to
`SO_PEERCRED`'s pid), then `/proc/<pid>/cgroup`, and maps the cgroup to what
started it.

- A caller inside the valet's cgroup (`figaro-valet@gluck.service`) is a tool,
  never the owner. The valet's box door names which figaro it serves, so the
  call is judged as that figaro.
- In home mode the daemon starts each tool run in a transient scope
  (`figaro-tool-<figaro>.scope`) and a caller inside one is judged as that
  figaro.

Cost, from the mint-default lane's estimate: 150 to 250 lines of production
code and about 300 of tests, one to two days, plus a day for the scope
tagging, since a ppid walk is broken by `setsid` and double forks.

What it holds: in house mode the valet's cgroup sits under `system.slice`,
whose `cgroup.procs` is root's, so a tool cannot move itself out. In home mode
the tool's scope sits in the user's own delegated subtree, so a tool can run
`systemd-run --user --scope` and leave it. Home mode stays honour; the check
only raises the bar there.

---

## Part 1: laws

### What a law is, and what it is not

A law is text. It says "do this" or "never do that" to a model, and the
daemon guarantees three things about it:

1. **It is always shown.** Every request for a figaro the law applies to
   carries it.
2. **It cannot be shadowed.** Nothing a figaro, a tool, a studied form or a
   later key writes can appear to the model as the same law, a newer version
   of it, or a law at all.
3. **It cannot be changed by a figaro.** Only Gluck writes laws: from his
   terminal at runtime, or by deploy. Under sealed laws, only by deploy.

A law does not make the daemon refuse anything. That is what keys are for:
`system.scopes`, `system.fanout.quota`, `system.tools`. The split is the
point: **keys bind the daemon, laws bind the model.** A rule the daemon can
check belongs in a key; a law may name the key so the model knows ("your
fan-out quota is on your role; ask the King for more, never `figaro new`").

### Why the reminders figaro has today are not enough

A rule placed on a role form today reaches the holder as a study reminder.
Three ways it fails:

- **Occluded.** The holder can `figaro drop @role`, and the reminder stops.
- **Shadowed by a later key.** A reminder is `<system-reminder name="<key>">`.
  The renderer escapes the name but not the body, so any form value, a
  `send` from another figaro or a tool result containing
  `</system-reminder><system-reminder name="x">...` produces a block that
  looks exactly like one the daemon wrote. A later block with the same name
  reads to the model as the newer value.
- **Buried.** A rule rendered once at turn 3 sits under 200 turns of
  transcript. Models weight recent text.

### How a law reaches the model

**The law block.** Laws in force render as a second system block, after the
credo, with its own cache breakpoint:

```
<laws version="9f3c2a1e">
<law name="node/no-em-dash" source="node policy" since="2026-11-18">
Never write an em dash.
</law>
<law name="role/@ee927149/report-to-role" source="role @ee927149 (Figaro Engineer)" since="2026-11-17">
Report to @ee927149, never to Gluck directly.
</law>
</laws>
```

The system block is the one place no transcript content can reach, and it is
read before anything else.

**Changes mid-life.** The credo is fixed at birth because rewriting the
system block drops the prompt cache. Laws change rarely, but they do change,
so:

- A law added, changed or repealed while a figaro lives is rendered at once
  as a `<law>` (or `<law-repealed>`) block at the head of the next turn's
  reminders.
- The system block refolds to the current laws only **when the cache is cold
  anyway**: on wake after the provider's cache lifetime has passed, or at the
  next succession. So a law change never costs a warm cache, and no figaro
  carries a stale law block for more than one cache lifetime of idleness.

Call this **refold at cold**.

**The `law` tag is reserved.** Every renderer escapes `<law`, `</law` and
`</system-reminder` in every body it did not write as a law: form values,
study reminders, sender blocks, prompts from other figaros and tool results.
Escaping one tag in tool output costs almost nothing and is the only way the
guarantee holds against a tool that prints a fake law. The law block's
`version` is a hash of its contents; the credo line that introduces laws says
a law exists only in the system block or in a `<law>` block, and that text
claiming to be a law anywhere else is not one.

**No law key renders as a study.** Keys under `law.` on a role form are
skipped by the study renderer. They reach the holder only through the law
block, so dropping the study drops nothing.

**One option for short, vital laws.** `recall = "every-turn"` re-renders a
law at the tail of every turn's reminders, where recency works for it. It
costs its length in tokens per turn, so it is off unless Gluck sets it; the
obvious use is a one-line spend rule.

### Where laws live

Three sources. Each law is named `<source>/<name>`, so two sources can never
collide or shadow each other.

| source | where it is written | who writes it | applies to |
|---|---|---|---|
| **troupe** | the troupe policy in the ledger, signed by the troupe root | Gluck, with the root key (`troupe.policy.set`) | every figaro on every node that adopts the policy, narrowed by an `on` selector |
| **node** | the node policy file, `[authz] policy`, as `[[law]]` tables | Gluck by editing the file (home mode) or by deploy (house mode, rendered read-only by Nix) | every figaro on the node, narrowed by `on` |
| **role** | the role form, as `law.<name>` keys | Gluck from his terminal, or the node policy naming the role (deploy) | the holder; with `reach = "lineage"`, the holder and every figaro below it in the drawn tree |

Policy syntax, the same in the node and troupe policies:

```toml
[[law]]
name   = "no-em-dash"
on     = "*"                       # any selector the policy knows
text   = "Never write an em dash, unless Gluck asks for one by name."

[[law]]
name   = "throttle"
on     = "lineage:9a5715d86dc9c264292aee19c80706b8"
text   = "Spend: Opus only under the throttle. Open no sub-lanes."
recall = "every-turn"

[[law]]
name   = "engineer-reports"
on     = "role:@ee9271497f3823d7f50922bec4899818"
reach  = "lineage"
text   = "Report to the role, never to a person."
```

`on` reuses the policy selectors that exist (`aria:<id>`, `role:@<id>`,
`lineage:<id>`, `outfit:<name>`, `sandbox:<name>`, `unboxed`, `*`), so there
is nothing new to learn and no new matcher to test.

On a role form, at runtime:

```sh
figaro law set @ee927149 report-to-role --reach lineage -- "Report to the role, never to a person."
figaro law ls <figaro>        # every law in force for it, with its source and since
figaro law rm @ee927149 report-to-role
```

`figaro law set` writes `law.report-to-role = {text, reach, since}` on the
role form. Plain `figaro set law.x ...` is refused with a pointer to `law
set`, so a law cannot be written by accident as a free-form key.

**Not on bound forms, not in outfits.** A fork copies its parent's bound
form, so a law on a figaro's own form would become an independent copy in
every fork and Gluck could no longer repeal it in one place; `reach =
"lineage"` on a role covers that need. An outfit is dress, and anyone who may
dress a figaro chooses its outfit, so an outfit can never carry a law. An
outfit or bound form patch that names `law.` is refused.

### How laws compose down the lineage and through succession

A figaro's laws in force are the **union** of:

1. troupe laws whose `on` matches it;
2. node laws whose `on` matches it;
3. laws on every role it holds;
4. laws with `reach = "lineage"` on every role held by any figaro above it in
   the drawn tree.

Laws only add. A descendant can never drop an ancestor's law, the way scopes
only narrow. When two laws contradict, the daemon does not choose: it renders
both, outermost source first (troupe, node, then roles from the top of the
tree down), and the credo line says the earlier law wins. A contradiction is
also reported by `figaro law ls` as a warning, since it is almost always a
mistake.

**Reparent.** The set is computed from the drawn tree when each request is
built. A figaro moved under another lineage gains that lineage's laws and
loses its old ones, each rendered as a change at the next turn.

**Succession.** Role laws belong to the seat: the successor is cast into the
role and has them from its first turn, with nothing copied; the predecessor
loses them when it drops the role. Since `succeed` demotes the predecessor
under the successor, the predecessor's children stay under the seat and keep
its lineage laws. A daemon-side succession mints the successor clean, and the
law block is in its very first request, before the handoff is read.

### How laws relate to what exists

| thing | who writes it | what it governs | relation to laws |
|---|---|---|---|
| **protected keys** (`system.scopes`, `system.sandbox`, `system.fanout.*`, [api/form/protected.go](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/api/form/protected.go)) | the owner, or an ancestor figaro up to its own value; never the figaro itself | what the daemon lets a figaro do | Laws are stricter: no figaro ever writes one, ancestor or not. `law.` becomes a third key class beside protected and owner-held, in a new `api/form/law.go`. |
| **owner-held keys** (`system.provider`, `system.credo`, `system.tools`, `system.ttl`, [api/form/owner_held.go](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/api/form/owner_held.go)) | the owner: the local door, an in-process call, or the owner's principal | the figaro's configuration | Owner-held today admits any call on the local door, so a tool that drops its label passes. Law keys need the owner **and** no figaro label **and** a caller that passes the peer caller check. The same tightening should apply to owner-held keys in house mode; it is the same check. |
| **credo** (`system.credo`) | the outfit or the birth patch; fixed at birth | who the figaro is | The credo is the first system block, laws the second. The credo is per figaro and frozen; laws are per seat, node or troupe, and may change (refold at cold). The durable rules now repeated in every credo ("no em dash", "never read credential files") are better as node laws; moving them is optional and is not part of the staged plan. |
| **outfits** | Gluck's config | a figaro's dress at birth | Outfits carry no laws (above). |
| **study reminders** | anyone who may write the studied form | what a figaro watches | Law keys never render as a study. |

### The write rule

A law write (`figaro law set|rm`, the `law.` prefix, any `[[law]]` change by
`troupe.policy.set` or a policy reload) is allowed only when all hold:

- the principal is the owner (for a troupe law, the troupe root's signature);
- the call carries no figaro label;
- the caller passes the peer caller check: it is not a tool;
- it does not come through a box door, a host door or the node door;
- laws are not sealed.

**Sealed laws** (`[laws] sealed = true`, house mode only): the daemon reads
laws from the rendered config alone. `law.` keys on role forms are not
rendered, `figaro law ls` lists them as inert, and every runtime law write is
refused, Gluck's included. A role law under seal is written in the node
policy with `on = "role:@<id>"`. Changing a sealed law is a deploy.

### Which laws are enforced in which mode

| guarantee | home mode | house mode |
|---|---|---|
| the law block is in every request | enforced: the daemon builds the request | enforced |
| reserved `law` tag, escaping, no study rendering | enforced | enforced |
| a labeled figaro cannot write a law | enforced on the label; a tool can drop it | enforced: the peer caller check makes any valet caller a tool |
| a tool cannot write a law by other means | **honour**: a tool runs as Gluck, can edit `~/.config/figaro` policy files, run `outfit reload`, or write the store directly | enforced: config is rendered read-only by Nix, the store is `/var/lib/figaro` owned by the `figaro` user, tools run in the valet |
| sealed laws | not offered: Gluck's uid owns the config | enforced |
| troupe laws are Gluck's | **honour** while the troupe root sits online in the desktop's hush | enforced after the key ceremony (deployment modes, stage 6) |

---

## Part 2: the fan-out quota

### Why `max_children` is the wrong shape

`system.fanout.max_children` caps a figaro's **direct** live children. It
does not bound a subtree: a figaro at 3 may grant each child 3, each of
those grants 3, and the subtree grows by a factor of three per level. The
re-review of v0.74.0 found exactly this (finding R2, "a capped subtree
grows"), and Gluck's earlier worry was the same family: a reparent moves a
whole subtree, and a per-level cap says nothing about what the subtree
already holds, so depth and concurrency invariants checked at fork can be
broken by a move.

A quota is a conserved budget instead. A parent hands part of its budget to a
child; the child can never hold more than it was handed; the budget comes
back when the child retires. Size, and therefore depth, is bounded by the
quota of the nearest capped ancestor, and every check is local.

### The rule

`system.fanout.quota` on a figaro is the most live descendants its subtree
may hold, itself not counted.

- The **charge** of a child `c` to its parent:
  - if `c` is live: 1, plus `c`'s quota; if `c` has no quota, 1 plus the live
    figaros under it;
  - if `c` is retired or reaped: the live figaros under it, and nothing for
    itself.
- **used(X)** is the sum of the charges of X's children.
- **free(X)** is quota(X) minus used(X).
- **The invariant**: used(X) never exceeds quota(X) for any capped X.

By induction, every capped figaro's subtree holds at most its quota.

**At fork.** A fork of X is refused when free(X) < 1 plus the quota the
fork grants. If X has no quota, the check runs at X's nearest capped
ancestor instead, whose subtree X's new child joins. A fork by a figaro (a
claimed fork) is born at quota 0 unless it grants more in the same call
(`fork -S system.fanout.quota=4`), exactly as v0.74.1 does for
`max_children`. An unclaimed fork (Gluck at a terminal) has no default.

**Re-granting.** Only a figaro's drawn parent, or the owner, writes its
quota. Raising a child's quota from q to q' needs free(parent) of at least
q' minus q. Lowering it is allowed down to used(child), and the difference
returns to the parent's free budget at once. A figaro never writes its own.

**Freeing budget.** Retiring a child returns its own slot and the unused part
of its quota. Its live children keep their charges until they retire too.
That is the incentive Gluck asked for: a lead out of budget retires its stale
lanes, and can fork again.

**Top-level figaros.** A top-level figaro's quota is the owner's grant, set
by Gluck (on its role, or from config under sealed laws). A labeled
`figaro new` by a figaro holding `system.fanout.mint` is charged to the
minter as if it were a fork: the new figaro is born at quota 0 and costs the
minter 1, shown under the minter in the budget view until it retires.
Without this, `mint` is a way around every budget.

**Roles.** A role's `system.fanout.quota` is a ceiling for whoever holds it:
the holder's effective quota is the lowest of its own and every held role's.
It is not a grant: budget only ever flows down the tree, so a role cannot
mint budget its holder's parent did not hand down. A top-level seat (the
King) is the exception, since its parent is the owner.

### Reparent accounting

A move of the subtree rooted at `s` from parent A to parent B:

- B's nearest capped ancestor-or-self must have free budget of at least
  charge(s), counting only capped figaros that do not already contain `s`.
- Every capped figaro on A's side that no longer contains `s` gets charge(s)
  back.

This is the topology guard's existing "net arrivals minus departures per
plan" (`TopologyGuard.fanout`), with charge(s) in place of 1. A refused move
leaves the topology as it was, as today.

Why this answers the worry: there is no depth invariant left to break,
because depth is bounded by size, and size is what the check counts. A
moved subtree carries its whole charge with it, so the receiver pays for
everything it takes.

### Succession

- **`succeed` (the swap).** The successor Y is a claimed fork of the
  predecessor X, charge 1. The demote puts X under Y and Y in X's place, and
  swaps their quotas: Y takes X's, X takes 0. X is then retired, so its
  charge is the live figaros under it. used(Y) is X's old used minus one.
  Net zero for X's parent, as `max_children` already behaves.
- **Daemon-side succession** (`system.retirement` with `action = "succeed"`)
  today mints the successor clean and places it **beside** the predecessor
  (`placeBeside` in `internal/angelus/succession.go`), copying
  `max_children`. Under a quota that double counts: the parent pays for the
  successor's quota while the retired predecessor's subtree still charges it.
  **Change:** the daemon places the successor beside, then demotes the
  predecessor under it and swaps quotas, so both successions draw the same
  tree and follow one accounting rule. This changes the drawn tree after
  every daemon-side succession; see the staged plan.

### Concurrency, maybe

`system.fanout.running`, optional: the most turns that may run at once in a
figaro's subtree, itself included. Checked when a turn starts against every
capped ancestor; a turn over the cap waits in its queue, first in first out,
and is never refused. A reparent is never refused for concurrency: a moved
subtree's running turns count against its new ancestors at once, and new
turns wait until the count drops.

Gluck decided in the fan-out review that concurrency stays a rule in
prompts. That decision fits laws exactly: ship concurrency first as a
lineage law with `recall = "every-turn"` on the King's role, and build the
key only if the law proves too weak.

### What `figaro ls` shows

A new column, `fan`, on every figaro with a quota or a capped ancestor:

```
ID        MANTRA                                FAN
9a5715d8  King XVI: Project Italy               14/40
ee927149  Figaro Engineer                        6/12
54fc120b  Proposal: figaro laws, fork quota      0/0
```

`used/quota`, blank when uncapped with no capped ancestor, and marked when
used exceeds quota (possible only after the owner overrode it). `figaro
fanout <id>` prints the budget tree: each child, its charge, its own quota,
and the parent's free budget, so a lead can see which lane to retire.

### Migration from `max_children`

The store snapshot of 2026-11-18 holds no form with any `system.fanout`
key: v0.74.1 shipped the keys, and nothing has set one yet. So there is no
stored value to translate.

- `system.fanout.max_children` is removed. A write is refused with "renamed:
  system.fanout.quota counts the whole subtree". A stored value is ignored
  with one log line per figaro per run, and `figaro doctor normalize` drops
  it.
- The claimed-fork-born-at-0 rule, the granter bound, the placement check and
  the succeed swap keep their shape and move to the new key.
- `skills/figaro/reference/fanout.md` is rewritten around the quota.
- `system.fanout.mint` is unchanged, except that a labeled mint is now
  charged to the minter.

### Enforcement by mode

| | home mode | house mode |
|---|---|---|
| boxed figaros | enforced: the box door names them | enforced |
| unboxed figaros | **honour**: the daemon trusts the label, as v0.74.1 does; the peer caller check's tool scopes raise the bar | enforced: tools run in the valet, whose door names the figaro, and the peer caller check refuses a valet caller as the owner |
| top-level quotas | owner, from the terminal | owner; from config under sealed laws |

---

## Part 3: the crypt (reaped figaros, kept by size)

### Measured

On a full snapshot of the store taken 2026-11-18 (`figaro doctor snapshot
/var/tmp/laws-snap/store --arias 0 --max-mb 0`, 2,078 figaros, 2,539 nodes,
10 seconds), never on the live store:

| | bytes | share |
|---|---|---|
| whole store | 2.79 GB | |
| `translations-v2/` | 1.63 GB | 58% |
| `ir/` | 1.08 GB | 39% |
| `form/` | 74 MB | 3% |
| `_meta/` | 1.2 MB | |

Per figaro node: median 0.19 MB, 90th percentile 3.3 MB, 99th 14 MB, largest
41.5 MB, mean 1.2 MB.

Reaped figaros, from `figaro reaped -j` mapped onto the snapshot:

| | value |
|---|---|
| reaped groups | 657, holding 666 nodes |
| their bytes | 990 MB, 36% of the store |
| of which `translations-v2/` | 673 MB (68%) |
| of which `ir/` plus `form/` | 317 MB, 0.48 MB per node |
| grace | 649 groups at 720 hours (`[memory] reaped_grace_hours = 720`), 8 at 72 hours (killed, `kill_grace_hours`) |
| reaping rate, 2026-10-30 to 2026-11-18 | 35 groups a day; 52 MB a day with the wire cache, 17 MB a day without |
| `ir/` under `zstd -3` | 2.7 times smaller (200 MB of segments to 74 MB) |

### Should the wire cache go at reap? Yes, a day after

`translations-v2/` is called a derived cache, and it mostly is, with one
exception the architecture documents: it holds Anthropic thinking
**signatures**, which the IR has no place for. Dropping it costs a restored
figaro two things: its first turn rebuilds the whole wire prefix (one cold
cache write), and its earlier thinking blocks are left out of replay, since
the cache-miss path drops unsigned thinking rather than send it (a 400
otherwise). Nothing is lost for audit: the IR keeps the thinking text, every
message and every tool call.

Restores are rare and mostly happen soon after an accidental reap. So the
crypt drops the wire cache when it takes a group, and it takes a group one
day after the reap (`[retention] settle`, default `24h`). A restore inside
that day is lossless.

Only nodes in the group's delete set lose their cache. A history prefix that
a live fork still reads stays in the live store, cache and all; that is the
same rule the purge follows today (`keeper.DeleteSet`).

At 17 MB a day, or about 6 MB a day compressed, an 8 GB crypt holds well
over three years of reaping at today's rate. With the cache kept it would
hold about five months.

### The design

**The crypt** is `~/.local/state/figaro/crypt/` (house mode:
`/var/lib/figaro/crypt/`): one directory per reaped group, keyed by its root,
holding its nodes' `ir/` and `form/` segments compressed with zstd, the
`_meta` sidecar, and a `group.json` manifest (root, nodes, mantra, reaped
at, entered at, bytes, and the live node its history forks from). The crypt
keeps a running total in `crypt.json`, so no job ever walks it to measure.

**The retention job** runs in the existing sweep:

1. A group reaped more than `settle` ago moves into the crypt: copy, verify,
   then delete from the live store.
2. When the crypt exceeds `crypt_max`, or the store plus the crypt exceed
   `store_max`, delete crypt groups **oldest reap first** until both fit.
3. If `crypt_max_age` is set, delete crypt groups older than it. Off by
   default and off on gluck.
4. If the live store alone exceeds `store_max`, delete nothing live: log it,
   say so in `figaro doctor ttl`, and show it in the pager footer.

**Reading.** `figaro show <id>`, `figaro reaped` and `@reaped` read the
crypt. A crypt group whose history parent was later evicted is
**headless**: it keeps its own turns, and `show` says from which turn the
history is gone and when it went.

**Restoring.** `figaro reaped restore <id>` moves a group back into the live
store, decompressed, with no wire cache (the cost above).

**Kills and boxes.** `kill_grace_hours` and a box's `purge_grace` remain
time limits, because they are explicit asks to forget: a killed group or a
reaped boxed group skips the crypt and is purged when its grace ends, as
today. `kill --now` still purges at once.

### Config

`[memory] reaped_grace_hours` is removed and a new table replaces it:

```toml
[retention]
store_max     = "20GB"   # live store plus crypt; required to enable the crypt
crypt_max     = "8GB"    # the crypt alone
settle        = "24h"    # reap to crypt; restores inside it are lossless
crypt_max_age = ""       # empty: no age limit (gluck)
```

With no `[retention]` table the crypt is off and reaped groups are purged
after `settle` (default 24 hours), which is shorter than today's 7-day
default: a node that wants audit sets the caps. In house mode these are
`services.figaro.retention.{storeMax, cryptMax, settle, cryptMaxAge}`.

**For audit.** In home mode a tool can delete or rewrite crypt files, so the
crypt is a record, not evidence. In house mode the crypt is owned by the
`figaro` user and tools cannot reach it.

---

## How this fits the deployment modes proposal

- **The owner enforces.** The angelus owns the request it builds, so it
  enforces laws; it owns the topology, so it enforces quotas; it owns the
  store, so it runs the crypt. Nothing here asks the valet, hush or spain to
  enforce anything.
- **House mode on Merlin first, `figaro valet`, every figaro allowed by
  default.** With the valet allowing every figaro by default, file grants are
  no longer the restraint on a figaro; laws, quotas and the peer caller check
  carry it. That raises the priority of the peer caller check: it is what
  turns both the law write rule and the quota for unboxed figaros from
  honour into enforcement.
- **The valet's socket as the kill switch.** Stopping the valet stops every
  tool. Laws and quotas still bind every turn that does not need a tool.
- **NixOS module surface**, added to `services.figaro`:

| option | default | what it does |
|---|---|---|
| `laws` | `[ ]` | node laws, rendered into the policy file as `[[law]]` |
| `laws.sealed` | `false` | sealed laws |
| `fanout.roots` | `{ }` | role id to quota, the owner's top-level grants under seal |
| `retention.storeMax`, `cryptMax`, `settle`, `cryptMaxAge` | unset | the `[retention]` table |

## Staged plan

Each stage ships alone, is reversible, and names what breaks.

| stage | what | breaks on disk | breaks in the protocol or behaviour | reverse |
|---|---|---|---|---|
| 1. the law block | node laws from `[[law]]`, the system block, refold at cold, the reserved tag and escaping, `figaro law ls` | nothing | the system block of every figaro on a node with laws gains a block, so each figaro's cache drops once, at its next cold wake. Bodies containing `<law` or `</system-reminder` render escaped | remove `[[law]]` |
| 2. role laws | `law.` key class (`api/form/law.go`), `figaro law set/rm`, lineage reach, no study rendering of `law.` | new keys on role forms | `figaro set law.*` refused; an outfit or bound form patch naming `law.` refused | `law rm`; old binaries see `law.` as plain keys and render them as studies |
| 3. the peer caller check | cgroup attribution on the CLI door, tool scopes in home mode, the valet's cgroup as a tool in house mode | nothing | a tool that drops `FIGARO_ARIA` is no longer the owner: scripts that ran `env -u FIGARO_ARIA figaro ...` from a tool shell to act as Gluck stop working. Law and owner-held writes need it | a config switch, `[authz] peer_check = false` |
| 4. the quota | `system.fanout.quota` replaces `max_children`, charges, reparent accounting, labeled mint charged to the minter, the `fan` column and `figaro fanout` | `max_children` ignored and dropped by `doctor normalize` (no form carries one today) | `-S system.fanout.max_children=` refused; daemon-side succession now demotes the predecessor under the successor, so the drawn tree after an automatic succession changes shape | ship the old key back; quotas are plain values |
| 5. troupe laws | `[[law]]` in the troupe policy, carried by the ledger | new ledger field | nodes on older releases ignore troupe laws: say so at `troupe join` | `troupe.policy.set` without them |
| 6. the crypt | `[retention]`, the move, eviction, headless reads, restore from the crypt | reaped groups move out of `arias/` into `crypt/`; their wire cache is deleted; `reaped_grace_hours` removed, refused at config load with the replacement named | a restore after `settle` costs one cold turn and drops replayed thinking | turn `[retention]` off; the crypt can be restored group by group |
| 7. sealed laws | `[laws] sealed`, `fanout.roots`, the Nix options | nothing | under seal, Gluck's runtime `law set` is refused | `sealed = false` and a deploy |
| 8. concurrency, only if the law is not enough | `system.fanout.running`, queued turns | nothing | turns over the cap wait instead of starting | unset the key |

Stages 1, 4 and 6 need nothing from each other. Stage 2 needs 1. Stage 7
needs 2 and house mode (deployment modes, stage 4). Stage 3 is independent,
but laws and quotas are honour for unboxed figaros until it lands.

## Open for Gluck

1. **Laws in the system block.** Proposed: yes, refolded only when the
   cache is cold, with mid-life changes as `<law>` blocks. The alternative,
   laws only as reminders, never touches the cache but is weaker against
   recency. Which?
2. **Should the "no em dash" family move out of every credo into node laws?**
   It would shrink every credo and make the rules repealable in one place,
   but changes the credo hash of every outfit that drops them.
3. **Can a lead write laws for its lanes?** Proposed: no, laws are Gluck's
   only. A lead's standing orders stay in its briefs and forms. A "lead law"
   that a role holder may set on its own lineage would be a different, weaker
   thing and needs its own name.
4. **Labeled `figaro new` charged to the minter.** Proposed: yes, or `mint`
   escapes every budget. It means the King's quota must cover what it mints.
5. **Concurrency.** Ship it first as an every-turn lineage law on the King's
   role, and build `system.fanout.running` only if that proves too weak?
6. **Crypt defaults.** With no `[retention]` table, reaped groups go after 24
   hours instead of today's 7 days. Is that the right default for a node that
   has not opted in, or should the crypt be on with modest caps (say 4 GB and
   2 GB) everywhere?
7. **Compression.** zstd on crypt segments gains 2.7 times at the cost of a
   decompress on `show` and restore. Worth it, or keep plain JSONL for
   `grep`?
