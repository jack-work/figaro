# Fan-out limits: `system.fanout.max_children`, `system.fanout.quota` and the role verbs

## `system.fanout.max_children`

A protected form key, same writers as `system.scopes`: the owner sets any
value, an ancestor figaro sets a descendant's value up to its own, a figaro
never sets its own. It is an integer count of a figaro's **live** direct
children in the drawn topology: neither retired nor reaped. Dormant and idle
children count; they are inventory and can wake.

Absent means unlimited, for that figaro alone: it does not propagate as
"unlimited" to anything forked from it.

A role form may carry the key too. Whoever holds the role is bound by the
tighter of its own value and every role it holds, at fork, at reparent and
as a granter, so the limit survives a succession even when the new holder's
own form carries none.

The daemon refuses:

- `fork`, when the figaro being forked already holds `max_children` live
  children. Counted under the topology lock together with the birth, so
  concurrent forks and reparents cannot overfill; nothing is created. The
  refusal names the grant its parent can run:
  `figaro set --id <it> system.fanout.max_children <n>`.
- `demote`, `adopt` (the `figaro.accept` consent step), and any other move
  that redraws the topology, when it would leave some node holding more live
  children than its limit. The topology is left exactly as it was.

A figaro forked by **another figaro or by itself** (the call carries a
`FIGARO_ARIA` claim) is born at `max_children = 0` unless the forking patch
grants more in the same call (`fork -S system.fanout.max_children=2`). A
`-D` of the key on a claimed fork is the same as naming nothing: the child
is born at 0. A grant may not exceed the granter's own limit; a granter
with no limit may grant any value. A fork with no figaro claim, the common
case of a human forking interactively, carries no default and starts absent
(unlimited), exactly as before this key existed.

**The owner's forks are never refused.** A fork the owner makes (no figaro
claim, not through a box door) skips `max_children` and the quota, but it
still counts: as a live child of its parent and in every ancestor's usage.
It carries `runtime.owner_made = true`. A figaro over its cap because of
them may retire them (they sit in its own lineage); its refusal names them.

## The swap: the limit goes with the place

`succeed` forks the holder, then demotes the holder under its fork. When a
figaro is demoted under an aria forked from it, the two swap places and
their limits swap with them: the successor takes the predecessor's value
(or its absence), the predecessor takes the successor's (0, for a claimed
fork). The demote is checked against the predecessor's limit, tightened by
the roles either holds, so an uncapped holder's succession completes and a
capped one's fits by construction. A demote under a child that is not a
fork of the demoted figaro moves no limit and is checked against the
child's own.

Daemon-side succession (`succession-policy`) mints the successor fresh and
copies the predecessor's `max_children` onto it; the quota moves to it when
it takes the seat (below).

## Who may take a held role

Once a role has a holder, `cast` (and `new -C`) hands it only to the holder,
a descendant of the holder in the drawn tree, or the holder's direct drawn
parent when that parent was forked from the holder: the shape `succeed`
leaves after its demote. An ancestor that merely sits above the holder (a
lead, the King) is refused. The owner acting with no figaro label is exempt.

## `system.fanout.quota`: a budget for the whole subtree

**Off by default.** The quota is computed and counted everywhere, but the
daemon refuses nothing for it until the owner turns it on:

```toml
# config.toml
[authz]
enforce_quota = true
```

While it is off, every fork, reparent or quota write the quota would refuse
goes through, is logged as `authz: would refuse` with the switch, and is
counted in `figaro doctor admission`. `max_children` is enforced either way.

A protected key beside `max_children`, same writers, checked independently.
It is the most live figaros a figaro's subtree may hold, itself not counted.

- A live child with a quota charges its parent `1 + quota`, or `1 + used`
  while it uses more. A live child with
  none charges `1 +` what its own subtree uses, so a grant under an uncapped
  lead still counts against the cap above it. A retired or reaped child
  charges only what its subtree uses.
- `used` is the sum of the children's charges. The daemon keeps it in memory,
  beside the live-child counts, and changes it on every birth, retirement,
  reparent, limit write and role cast, walking up only to the nearest live
  capped ancestor. `figaro doctor admission` compares it with the tree.
- A role's quota is a ceiling for its holder: the effective quota is the
  lowest of the figaro's own and every role it holds.

**Budget only moves down.** A claimed fork is born at quota 0 unless the
fork grants more (`fork -S system.fanout.quota=3`), and the grant comes out
of the parent's free budget: a fork costs `1 + grant`. A `system.child_policy`
quota is a grant too. A fork never copies its parent's quota, whatever the
turn, the outfit or the patch: an owner's fork with no grant is uncapped.
Usage is read off today's live tree, so a fork at an old turn pays for the
tree as it is now.

**Re-granting.** An ancestor raises a child's quota only from its own free
budget. A quota may be lowered below
what the subtree already uses: nothing is reaped, the child cannot fork
until its use drops under, and meanwhile it charges its parent
`1 + max(quota, used)`. A figaro never writes its own. `figaro ls` marks a
figaro using more than its quota with `!`. Usage is never written to a form.

**Admission holds what it admits.** A fork, a mint and a quota raise take
no hierarchy lock. Each checks the budget and holds its charge in the
daemon's counts in one step, and lets the hold go once the birth or the
write has landed, so forks and raises racing each other never overdraw.
A reparent (demote, adopt) is checked under the hierarchy lock instead,
against what has landed and what forks and raises hold, so one racing them
lands a figaro over by at most what was admitted while the reparent was
decided. An overshoot is
never undone: the figaro is over budget, refused every fork until it is back
under, charges its parent `1 + max(quota, used)`, and nothing is reaped. The
reconciler rebuilds any count that drifted from the tree, and `figaro doctor
admission` lists every figaro over `max_children` or its quota.

**A refusal names the budget** at every live capped ancestor on the way up,
the owner's forks under the one that refused, and the grant its parent can
run:

```
fanout: this needs 1 of budget, and 9a5715d8 has 0 free of quota 2; retire a figaro under it (the owner made 3f0c11aa), or have its parent grant more (figaro set --id 9a5715d8 system.fanout.quota 3)
```

There is no verb to ask for budget: ask the parent in words, and quote it
the command.

**Reserved is reserved.** Admission checks only the nearest live capped
ancestor, because a child's charge already covers everything under it. A
figaro over its quota (owner forks, a quota lowered below use, a race)
refuses its own forks, but a capped figaro under it with room left keeps
admitting: what was granted to it stays its own. So `doctor admission`
showing `A uses 14 of quota 10` while A's children still fork is the rule,
not a fault. A's own forks wait until it is back under.

**Minting spends budget.** A labeled `figaro new` is charged to the minter as
a fork would be: the new figaro is born at quota 0, drawn under its minter,
and costs it 1 until it retires.

**Reparents** carry the whole charge of the moved subtree; a move that would
overdraw a capped ancestor is refused and nothing moves.

**`figaro ls`** shows `used/quota` in the `FAN` column for every capped
figaro (`-` when uncapped); `ls -j` carries `fan_used` and `fan_quota`.

## Succession and the quota

- **`figaro succeed`** (the predecessor retires) is net zero. The successor
  fork costs nothing; the seat swaps places, the successor takes the
  predecessor's quota and the predecessor keeps none, so once it retires the
  seat uses exactly what it used before. It is refused only if the
  predecessor's budget is already overdrawn.
- **A seat whose successor is not the predecessor's fork** puts the
  predecessor under the successor along with its children. If that leaves
  the successor over the quota it took, its quota is raised to its use: a
  predecessor at its quota costs +1 on the seat and +1 at its outer
  absorber, and the raise stays after the predecessor retires. Every other
  seat is net zero.
- **`--abdication linger`** (familial) keeps the predecessor alive under its
  successor, so it costs 1 and is refused at the fork without it.
- **`--successor <it>`** (overlap) paid for the successor at the fork that made
  it; the seat moves nothing in the budget.
- **Daemon-side succession** places the fresh successor beside the
  predecessor for its ramp, which costs 1 at their parent, and moves the
  quota to it when it takes the seat.

## Role verbs: `cast` and `succeed`

**Off by default, with the mint grant below.** Until the owner sets

```toml
[authz]
enforce_key_grants = true
```

a cast, succession or mint these rules would refuse goes through, is logged
as `authz: would refuse`, and is counted in `figaro doctor admission`, and
`system.fanout.mint` licenses exactly as before.

Two verbs in `system.scopes` name a role form (`cast:@<role>` is short for
`cast:aria:@<role>`):

- `cast` is needed to cast anything into an existing role. A role that
  nobody holds is taken only with `cast` **written** in the figaro's scopes
  (or an ancestor's): the implicit `*:*` of an unscoped figaro does not
  count. The owner at a terminal is exempt.
- `succeed` is needed to fork a seat's successor (`fork --seat`, which
  `figaro succeed` sends) and to seat one (`figaro.seat`) in place of a
  holder of that role.

The default box role carries `cast:sandboxed:self` and
`succeed:sandboxed:self`.

## `mint`: who may `figaro new`

A figaro may not create a top-level figaro unless `mint:*` is written in its
`system.scopes` or an ancestor's; the implicit `*:*` of an unscoped figaro is
no license. `figaro new` and `figaro bind` (`figaro.create`, `form.bind`)
are otherwise for Gluck:

```
mint: <id> may not create a top-level figaro; fork yourself instead (figaro fork <id>:1 ...), or have mint:* written in its system.scopes
```

A figaro gives `mint:*` only if it holds it. Scopes on a role form grant
nothing, so a seat's license lives on its holder and succession carries it
with the rest of `system.scopes`.

With `enforce_key_grants` on, `system.fanout.mint` is retired: it is no
longer read, and a write is refused with a pointer to `mint:*`. Clearing it
is allowed. With it off, the key licenses as it always did: a figaro may
`figaro new` when its own form or a role it holds says `true`, and only
Gluck's own terminal can put it on the King's role. A figaro's tool shell
cannot, whatever its environment: the daemon knows it by its cgroup and
refuses it as that figaro, so `env -u FIGARO_ARIA figaro new ...` from a
tool shell is still refused. It is the same uid, so a tool working against
it can still leave its cgroup on purpose; see [peer-check.md](peer-check.md).

`figaro.create` needs the `mint` verb; `form.create`, `form.bind` and import
stay on `create`. The production box-door policy grants both.
