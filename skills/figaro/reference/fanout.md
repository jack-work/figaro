# Fan-out limits: `system.fanout.max_children`, `system.fanout.mint` and the `mint` verb

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
  concurrent forks and reparents cannot overfill; nothing is created.
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
copies the predecessor's `max_children` onto it.

## Who may take a held role

Once a role has a holder, `cast` (and `new -C`) hands it only to the holder,
a descendant of the holder in the drawn tree, or the holder's direct drawn
parent when that parent was forked from the holder: the shape `succeed`
leaves after its demote. An ancestor that merely sits above the holder (a
lead, the King) is refused. The owner acting with no figaro label is exempt.

## `system.fanout.mint`: who may `figaro new`

A figaro may not create a top-level figaro unless it holds mint. Forks are
how a figaro fans out; `figaro new` and `figaro bind` (`figaro.create`,
`form.bind`) are for Gluck and for whoever he licenses. No policy file is
needed: the daemon applies this on every door.

A call that carries a figaro label (`FIGARO_ARIA`) on an unboxed door is
refused unless that figaro's effective `system.fanout.mint` is `true`:

```
mint: <id> may not create a top-level figaro; fork yourself instead (figaro fork <id>:1 ...)
```

Effective means its own form or any role it holds says `true`. A call with
no label (Gluck at a terminal) is allowed as before. Boxed callers keep the
policy path below: their box role needs `mint:*`. When a policy is in force
on an unboxed door, the policy and the key must both allow it.

The key is protected, with the writers of `max_children`:

- the owner, unlabeled, sets any value on any form, a role form included;
- an ancestor figaro sets it on a descendant only if it holds mint itself
  (own form or role); clearing it needs ancestry only;
- a figaro never sets its own, and no labeled figaro sets it on a role form
  or on a form it mints, since neither has it as an ancestor.

A claimed fork (`FIGARO_ARIA` on the call) is born without the key even when
its parent has it, unless the fork grants it in the same call
(`fork -S system.fanout.mint=true`, checked as a grant). An unclaimed fork
inherits it as it inherits everything else.

### Succession

A role carrying `system.fanout.mint: true` licenses whoever holds the role,
and only while they hold it, so the seat keeps it through every succession
with nothing copied. A holder's own key moves in the succeed swap the way
`max_children` does (the successor takes it, the predecessor loses it), and
daemon-side succession copies it onto the successor it mints.

### Licensing the King

Put the key on the King's role, from Gluck's own terminal. A figaro's tool
shell cannot do it, whatever its environment: the daemon knows it by its
cgroup and refuses it as that figaro (see [peer-check.md](peer-check.md)):

```sh
figaro set --id @<King role> system.fanout.mint true
```

`figaro set --id @<role>` writes the role form itself, not its holder's
board (checked on 0.74.0 against a scratch role). `figaro state show --id
@<King role> -j` confirms it. To revoke: `figaro form delete --id @<King
role> system.fanout.mint`, or set it `false`.

### What it does not stop

With `[authz] peer_check` on (the default on Linux), the daemon attributes a
local call by the cgroup of the process that made it, so a tool shell that
runs `env -u FIGARO_ARIA figaro new ...` is still its figaro and is refused.
It is the same uid, so a tool working against it can still leave its cgroup
on purpose; see [peer-check.md](peer-check.md).

## `mint`: the verb `figaro.create` carries alone

`figaro.create` (`figaro new`, root-minting a bound figaro) and `form.create`
(minting an unbound form, the mechanism behind `cast -O`/`new -C` role
minting) used to share the `create` verb. They no longer do:
`figaro.create` now needs `mint`; `form.create`, `form.bind` and import stay
on `create`. A figaro whose `system.scopes` grants `create:*` but not `mint`
can still cast itself into a role or import an aria, but `figaro new` is
refused.

The production box-door policy (`internal/authz/policy.go`) grants both, so
sandboxed workers keep minting figaros unless an outfit narrows them
further. The restriction this is for ("ordinary figaros lose `figaro new`")
is meant to land in `system.scopes`, on the outfit a figaro is born under:
see `docs/skills/fanout-limits-outfit-example.md` for the staged change, not
applied to any outfit or config in this repo. `new -C`/`cast` (role minting)
is unaffected either way, since those go through `create`, not `mint`.
