# Fan-out limits: `system.fanout.max_children` and the `mint` verb

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
