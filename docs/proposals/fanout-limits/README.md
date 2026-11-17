# Enforced fan-out limits

Status: proposal, revision 2, after Gluck's review on 2026-11-17. Asked by
the King after the Anthropic weekly plan burned at about 5x the sustainable
rate.

## Decisions taken

- One limit only: `system.fanout.max_children`. No `max_running`, no
  `max_depth`; concurrency stays a rule in prompts.
- Figaros fan out by **fork** only: an early fork (turn 0, 1 or 2) when the
  child needs no context, a tail fork when it does. By default a figaro may
  not mint a top-level figaro (`figaro new`). The King keeps that right, so
  he can repair a limit that proves too tight.
- Unboxed figaros are held by honour: the daemon trusts the figaro label the
  CLI stamps (`FIGARO_ARIA`) and refuses on it, and a figaro that strips it is
  breaking the rules, not the lock. Boxed figaros are held for real: their
  door names them.
- Once this ships, the policy is computed once and written into central
  command (the Home Manager setup), not set by hand.

## Children, not spawns

Since fan-out is by fork, a figaro's children are its children in the
topology. No spawner key is needed.

`max_children` counts a figaro's direct children that are live: neither
retired nor reaped. Dormant and idle children count, because they are
inventory and can wake. Absent means no limit.

## Where it is checked

The daemon refuses past the limit at every call that adds a child:

| call | refused when |
|---|---|
| `fork` | the parent already holds `max_children` live children |
| `demote`, `adopt`, any reparent | the new parent would then hold more than its `max_children` |
| `succeed` | never for its own fork: the successor and predecessor swap places at once, so the count does not change. Every other figaro it moves is checked as a reparent |

A refused reparent leaves the topology as it was. The figaro gets an error
it can act on, for example:
`fanout: f3a1 holds 3 live children of max_children 3; retire one or fork elsewhere`.
That figaro recovers by itself: it retires a finished child, or asks the King.

## Granting fan-out to a child

A child gets `max_children = 0` at birth unless its parent grants more, so
the parent decides whether a child may fan out. A parent may grant at most
its own value. `fork -S system.fanout.max_children=2` is the gesture.

`system.fanout` joins the protected keys (`api/form/protected.go`), with the
same writers as `system.scopes`:

- the owner (Gluck) writes any value;
- an ancestor writes a descendant's value, up to its own;
- a figaro never writes its own.

A role form's `system.fanout.max_children` is the default for whoever holds
the role. It applies to the holder through every succession, so a new
holder cannot reset it.

## Top-level figaros

`figaro new` is the `create` verb (`api/rpc/verbs.go`). Ordinary figaros
lose it through their scopes: the default outfit gives `system.scopes`
without `create`, and the King's outfit keeps it. Scopes only narrow down
the lineage, so no fork of an ordinary figaro gets `create` back. Gluck at a
terminal carries no figaro label and is unaffected. Minting a role form
(`new -C`, `cast` with a patch) is a separate method and keeps working.

To check before building: whether `form.create` shares the `create` verb.
If it does, root minting needs its own verb (for example `mint`).

## Feasible?

Yes, and small. The topology already knows each figaro's children, and
every reparent passes one daemon function. The work:

1. Count live children, check at fork and reparent, add the protected key
   and the birth default of 0. Tests: fork refused at the limit, a reparent
   past the limit refused with the topology unchanged, `succeed` with a full
   predecessor allowed, a retired child freeing a slot, a figaro unable to
   raise its own limit, a parent unable to grant more than it holds. About
   200 lines plus tests.
2. Drop `create` from the default outfit's scopes, and keep it in the King's.
3. Once both have run for a while: compute per-role limits from real fleet
   shape and write them into central command through managed apply.

Nothing on disk breaks. Figaros that already exist carry no key, so they
have no limit until step 3 writes one. Behaviour does change: from step 1,
every new fork is born at 0 unless its parent grants more, so a lane that
fans out to reviewers needs `-S system.fanout.max_children=N` when it is
forked.
