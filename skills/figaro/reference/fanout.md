# Fan-out limits: `system.fanout.max_children` and the `mint` verb

## `system.fanout.max_children`

A protected form key, same writers as `system.scopes`: the owner sets any
value, an ancestor figaro sets a descendant's value up to its own, a figaro
never sets its own. It is an integer count of a figaro's **live** direct
children in the drawn topology: neither retired nor reaped. Dormant and idle
children count; they are inventory and can wake.

Absent means unlimited, for that figaro alone: it does not propagate as
"unlimited" to anything forked from it.

The daemon refuses:

- `fork`, when the figaro being forked already holds `max_children` live
  children. Checked once, up front; nothing is created.
- `demote`, `adopt` (the `figaro.accept` consent step), and any other move
  that redraws the topology, when it would leave some node holding more live
  children than its own `max_children`. The topology is left exactly as it
  was.
- `succeed`'s own swap is not special-cased: a predecessor's old parent loses
  the predecessor and gains the successor in the same plan, so the net count
  there is zero and the move is never blocked by it. The successor itself is
  checked like any other receiving node.

A figaro forked by **another figaro** (the call carries a `FIGARO_ARIA`
claim) is born at `max_children = 0` unless the forking patch grants more in
the same call (`fork -S system.fanout.max_children=2`). A grant may not
exceed the granter's own value; a granter with no value of its own (itself
unlimited) may grant any value. A fork with no figaro claim, the common case
of a human forking interactively, carries no default: it inherits nothing
and starts absent (unlimited), exactly as before this key existed.

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
