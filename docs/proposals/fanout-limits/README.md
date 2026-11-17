# Enforced fan-out limits

Status: proposal, for Gluck. Asked by the King, 2026-11-17, after the
Anthropic weekly plan burned at about 5x the sustainable rate.

## Problem

Fan-out limits today live in prompts ("max 2 concurrent lanes"). A figaro
that forgets them, or a child that never heard them, spawns freely. The
daemon mints every `new`, `fork` and `succeed`, and starts every turn, so it
can hold the line itself.

## Keys

Three keys under `system.fanout`, each a non-negative integer. Absent means
no limit from that level.

| key | counts | past the limit |
|---|---|---|
| `max_children` | live figaros this figaro spawned (not retired, not reaped) | `new`, `fork`, `succeed` from it are refused |
| `max_running` | turns running at once among everything it spawned, at any depth | the next turn waits for a slot (FIFO); it is not refused |
| `max_depth` | generations of spawning below it | a spawn deeper than this is refused |

`max_children = 0` means no fan-out at all, so no separate `allowed` flag.

Dormant and idle children hold no running slot. A child woken by figla or a
send takes a slot, or waits for one. That is the count the King asked for:
`max_children` bounds inventory, `max_running` bounds spend.

## Who spawned whom

`figaro new` mints a root, so topology does not record the spawner. The
daemon stamps `system.spawner` at birth from the call's figaro claim, along
with `system.spawner_role` when the spawner holds a role. Both are
daemon-written; no caller sets them. A call with no figaro claim (Gluck at a
terminal) stamps nothing and is unlimited.

A role-holding spawner's children count against the role, not the aria. So
when the Engineer succeeds, the new holder inherits the old holder's live
children and their slots without any rewrite, and a succession cannot reset
the budget.

## Effective limit

For each key, the smallest of:

- the figaro's own `system.fanout.<key>`;
- the same key on every role form the figaro holds;
- each spawner's own effective value, minus one generation for `max_depth`;
- `[figaros] fanout` in `config.toml`, the node-wide ceiling.

A turn needs a running slot at every level of its spawner chain, so a lane's
grandchildren count against the King's budget as well as the lane's.
Children always get the same limits as their parent or tighter ones. They
never get looser ones.

## Who may write the keys

`system.fanout` joins the protected keys (`api/form/protected.go`), on figaros
and on role forms. As with `system.scopes`:

- the owner (Gluck's door) writes any value;
- an ancestor or spawner may write a child's value only at or below its own
  effective value (the `refuseScopesBeyondWriter` check, reused);
- a figaro may not write its own value, nor its role form's.

For the King to set limits on roles he does not spawn, add one policy verb,
`govern:<selector>`, granted to the King's role in the troupe policy. Without
it, only Gluck sets role defaults.

## The gap, stated plainly

An unboxed figaro's bash reaches the local door as the owner (#57). The
claim is the CLI's `FIGARO_ARIA` stamp, and `env -u FIGARO_ARIA figaro new`
drops it. Step 1 therefore stops runaway fan-out by a well-behaved model, not
a model working around the limit.

Step 2 closes this for the local door. The daemon spawns every tool process
and knows their pids. On a local-door call it reads the peer pid
(SO_PEERCRED, already read in `internal/authz/peercred_linux.go`) and walks
the parent chain. If the chain reaches a tool process of figaro X, the call
is X's whatever the environment says. This is tool identity for unboxed
figaros, and it narrows #57 for every protected key, not only fan-out.

## Refusals and visibility

- A spawn past the limit fails with an error the model can act on:
  `fanout: @ee927149 holds 2 live children of max_children 2; retire one first`.
- A turn waiting for a slot shows in `figaro ls` as `waiting: fan-out`, and
  `figaro scopes <id>` (or a new `figaro fanout <id>`) prints each level,
  its count and its limit.
- Every refusal or wait logs one line in the daemon log.

## Steps

1. Keys, `system.spawner`/`spawner_role`, the effective limit, refusal on
   spawn, the running-slot semaphore, protected-key wiring. Tests: limit at
   each level, role counts surviving `succeed`, a wider write refused, a
   dormant child holding no slot, a woken child waiting. Roughly 400 lines
   plus tests.
2. Peer-pid attribution on the local door (closes the env gap; separate
   review).
3. Role defaults, set by Gluck in Home Manager through managed apply
   (I-01, already shipped): for example King 4 running / 8 children,
   Engineer 2 / 6, lanes 1 / 2 with depth 1.

Nothing on disk breaks: absent keys mean today's behaviour.

## Questions for Gluck

1. Past `max_running`, should a turn wait for a slot (proposed) or be
   refused?
2. Should the King get `govern:*`, or is the owner alone enough?
3. Are the role defaults above about right?
