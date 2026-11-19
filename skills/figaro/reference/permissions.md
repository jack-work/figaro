# Permissions: scopes, key grants, child policies

What a figaro may do to other figaros and to forms, how a parent hands that
down, and the four switches that decide whether the daemon enforces it.

## Scopes

`system.scopes` on a figaro is a JSON array of `verb:object` grants. Your
effective set is your own list intersected with every ancestor's list, top
first. With no `system.scopes` anywhere on your lineage you hold `*:*`.

```sh
figaro scopes <id>          # own and effective set
figaro set --id <child> system.scopes '["send:parent","read:*"]'
```

Verbs: `read`, `listen`, `send`, `fork`, `set`, `delete`, `create`, `mint`,
`kill`, `adopt`, `decide`, `use`, and a few more; `*` is every figaro verb.
`write` is another spelling of `set`.

Objects you will meet:

| object | reaches |
|---|---|
| `aria:<id>`, `aria:self` | one figaro, or yourself |
| `parent` | the figaro you were drawn under at birth |
| `children` | the figaros drawn under you |
| `lineage:<id>` | that figaro and everything below it |
| `role:@<form id>` | whoever holds that role now; it follows succession |
| `*` | everything |

`system.scopes` is protected: you never write your own. Your parent (or any
ancestor) writes yours, and it gives only what it holds itself. Gluck, at his own
terminal without `FIGARO_ARIA`, writes anything; a figaro's tool shell is
never Gluck ([peer-check.md](peer-check.md)).

## Key grants

A `set` or `delete` grant may name keys after `#`. Then the grant covers only
those keys, and a write is checked key by key: one key outside the grant
refuses the whole patch.

```
write:aria:self#mantra              your own mantra only
write:aria:self#notes.**            notes and everything under it
write:aria:self#@self               every self-class key on yourself
delete:aria:self#@self              and removing them
set:children#system.scopes          your children's scopes (protected)
```

`*` matches one path segment, `**` any depth. `@self` and `@lineage` name
the key classes in `figaro doctor form-keys` (see
[key-writers.md](key-writers.md)). Owner-held keys (`system.model`,
`system.ttl`, `system.tools`, `succession-policy`, ...) are never granted:
only Gluck writes them. `runtime.*` is the daemon's alone.

A keyed grant does not let you `study` or `drop` on another figaro (that needs
an unkeyed `set`), and a `cast` needs `set` on the role's `target-aria`.

The `datetime` every send carries is not a write: `send` alone covers it.

## Child policy

A figaro's fork is born with scopes chosen at birth:

- forked by a figaro (`FIGARO_ARIA` set): the parent's `system.child_policy`
  if it has one, otherwise the default lane set
  `["send:parent","read:aria:self","write:aria:self#@self","delete:aria:self#@self"]`;
- forked by Gluck: the parent's own scopes, unless the parent has a policy.

`system.child_policy` is an object of key paths to values, written into the
birth patch key by key. It is lineage class: an ancestor or Gluck writes it,
never the figaro itself, and owner-held keys in it only when Gluck writes them.

```sh
figaro set --id <seat holder> system.child_policy \
  '{"system.scopes": ["send:parent", "send:role:@9bf485c2", "read:aria:self",
    "write:aria:self#@self", "delete:aria:self#@self"]}'
```

In an outfit, the same thing:

```toml
[system.child_policy]
"system.scopes" = ["send:parent", "send:role:@9bf485c2", "read:aria:self"]
system.fanout.max_children = 2
```

Quoted or dotted, each key is a path. Only Gluck may name an outfit that
carries `system.scopes`, `system.fanout` or `system.child_policy`. The policy
is applied once, at birth;
changing it later changes only later forks. A figaro forking its own seat
(`figaro succeed`) gives the successor its own scopes and policy instead.

Sending to a role: `figaro send -f --id @role -- ...` needs only
`send:role:@role` (or `send:parent` while the role points at your parent).
The daemon resolves the role for you; no read on the role form is needed.

## Outfit grants

An outfit carries the owner's authority: its owner-held keys
(`system.model`, `system.provider`, `succession-policy`, ...) land where the
caller could not write them with `-S`. So a figaro names only `default` and
the outfits granted to it, wherever it names one: `fork`, `new`, `bind`,
`form create`, `set`, `send` and `cast`, each comma layer of `-O` on its
own. Gluck, at his own terminal, names any. A figaro in a box needs both
its box's `outfits` list and the grant. The rule holds whatever the
switches below say.

`system.grants.outfits` is an owner-held list of outfit names or globs (`*`
matches any run of characters in a name, so `"*"` grants every outfit):

```sh
figaro set --id <figaro> system.grants.outfits '["opus5", "model-*"]'
```

A figaro holds its own list and the list on every role form it holds, so
the charter grants a seat's outfits on the role and a successor holds them
once it is cast. A fork is born with a copy of its parent's own list, never
a role's. The outfit a role's `succession-policy` names is dressed by the
daemon at a seat, not named by the caller, and needs no grant.

A refusal names the outfit and the command that grants it:

```
outfit hk is not granted to <id>: a figaro dresses in default and in what system.grants.outfits lists on its form or on a role it holds; the owner grants it with: figaro set --id <id> system.grants.outfits '["opus5","hk"]'
```

Managed apply sets the key on a role form (it is one of the operator paths
in [key-writers.md](key-writers.md)); cheroot-config's charter writes it
from each seat's `outfits`.

## Retire and succeed

`system.ttl` is owner-held and never granted. The verbs that end a figaro
write it under the lifetime rule of [peer-check.md](peer-check.md): you may
shorten a lifetime (a finite ttl no later than the current one) and retire,
on yourself, a figaro below you or one you fork. `figaro retire <id> --ttl
10m` on your fork and the abdication of a `figaro succeed` you run on your
own seat need no grant. You never lengthen a lifetime.

## Reach

Demote, promote, retire and kill reach your own lineage. Beyond it they need
a written grant: a `system.scopes` somewhere on your lineage that covers the
verb on the target (`kill:*`, `set:*` for retire). Implicit `*:*` does not
count.

A seat needs kill on both figaros; see **Seats** in
[architecture.md](architecture.md).

## The switches

Four owner-held switches in the daemon config (`config.toml`, `[authz]`)
decide whether the daemon enforces the rules above. All are off by default.
The daemon reads them at start and on `figaro state outfit --refresh`.

```toml
[authz]
enforce_key_grants = true      # key-by-key writes, study/drop/cast rules, a label never the owner
enforce_child_policy = true    # default lane scopes and system.child_policy at birth
enforce_lineage_reach = true   # demote/promote/retire/kill beyond the lineage need a grant
enforce_quota = true           # system.fanout.quota refuses forks, reparents and grants past it
```

| switch | on | off |
|---|---|---|
| `enforce_key_grants` | a keyed grant covers only its keys; a labelled call on the local door is judged as that figaro's tools are; `cast`, `succeed` and `mint:*` are checked, and `system.fanout.mint` is retired | the write lands as it would have before key grants, and `system.fanout.mint` licenses `figaro new` |
| `enforce_child_policy` | a figaro's fork is born with the policy or the lane set | the fork inherits its parent's scopes, as before |
| `enforce_lineage_reach` | reach beyond the lineage needs a written grant | the call goes through |
| `enforce_quota` | a fork, reparent, mint or quota grant past a subtree's budget is refused | it lands and is counted against the budget; `max_children` still refuses |

With `enforce_quota` on, only the nearest live capped ancestor of a fork is
checked. A figaro over its quota refuses its own forks, while a capped
figaro under it with budget left keeps forking: budget granted down is
reserved. See [fanout.md](fanout.md), "Reserved is reserved".

Off, the daemon still decides, logs `authz: would refuse` with the switch and
the reason, and counts it:

```sh
figaro doctor admission          # each switch, on or off, and its would-refuse count
```

Rules older than these switches (protected keys, runtime keys, key shapes, box
key lists, method grants, the peer check) hold whatever the switches say.
