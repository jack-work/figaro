# Permissions: scopes, key grants, child policies

What a figaro may do to other figaros and to forms, how a parent hands that
down, and the three switches that decide whether the daemon enforces it.

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
ancestor) writes yours, and it gives only what it holds itself. Gluck, running
without `FIGARO_ARIA`, writes anything.

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

## Reach

Demote, promote, retire and kill reach your own lineage. Beyond it they need
a written grant: a `system.scopes` somewhere on your lineage that covers the
verb on the target (`kill:*`, `set:*` for retire). Implicit `*:*` does not
count.

## The switches

Three owner-held switches in the daemon config (`config.toml`, `[authz]`)
decide whether the daemon enforces the rules above. All are off by default.
The daemon reads them at start and on `figaro state outfit --refresh`.

```toml
[authz]
enforce_key_grants = true      # key-by-key writes, study/drop/cast rules, a label never the owner
enforce_child_policy = true    # default lane scopes and system.child_policy at birth
enforce_lineage_reach = true   # demote/promote/retire/kill beyond the lineage need a grant
```

| switch | on | off |
|---|---|---|
| `enforce_key_grants` | a keyed grant covers only its keys; a labelled call on the local door is judged as that figaro | the write lands as it would have before key grants |
| `enforce_child_policy` | a figaro's fork is born with the policy or the lane set | the fork inherits its parent's scopes, as before |
| `enforce_lineage_reach` | reach beyond the lineage needs a written grant | the call goes through |

Off, the daemon still decides, logs `authz: would refuse` with the switch and
the reason, and counts it:

```sh
figaro doctor admission          # each switch, on or off, and its would-refuse count
```

Rules older than these switches (protected keys, runtime keys, key shapes, box
key lists, method grants) hold whatever the switches say.
