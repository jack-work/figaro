# P5 migration: fleet grants and child policies

What has to be written before P5 runs on Gluck's fleet, so that no seat loses
a power it uses today and no lane goes quiet.

Read on the live fleet, 2026-11-18 06:2x. Every seat holder below is
unscoped: no `system.scopes` on it or on any ancestor, effective set
`*:*` (every figaro verb on every figaro).

| Seat | Role form | Holder at reading |
|---|---|---|
| King of Spain | `@96447061` | leisure-utensil `8e7bddb1` |
| Figaro Engineer | `@ee9271497f3823d7f50922bec4899818` | jaguar-tallness `7e37711e` |
| Figaro Knight | `@d121c4041785b13c6261d6055f113f8d` | goatskin-stingray |
| Figaro Strategist | `@9bf485c24507fe8484a001c18a1b84cc` | dozed-outhouse `44eb6388` |
| El Zapador | `@662ee4fedbd0fdbad7ad6adef20cdeee` | pamphlet-oceanic `b0e9ecbb` |
| Figaro Review Steward | `@9e5bdab374ebdf583434860ff1fa9955` | plywood-tweezers |
| El Pregonero | `@2c1645942f894dd40d9dfcfb97619c58` | rimmed-caviar |
| El Taller | `@2b443573385ce498dc40d0f5a672dd2a` | tissue-hubcap |

## What P5 takes from an unscoped seat

Only cross-lineage topology. Demote, promote, retire and kill now reach the
figaro itself and everything below it. Beyond that they need a written
grant. Everything else an unscoped figaro does today (send, read, set, fork,
cast, mint, kill inside its own subtree) is unchanged, because its effective
set is still `*:*`.

The seats sit in shared trees (the Engineer, Knight and Strategist holders
are all under `jarring-diamond`; the King and El Zapador under
`gimmick-olympics`). None of them holds another's subtree. Below itself,
each seat holds only its own fan-out forks.

## Writing any scope removes the implicit reach

Yes. A figaro with no `system.scopes` anywhere on its lineage holds `*:*`
implicitly. Once any `system.scopes` is written, on the figaro or on an
ancestor, its effective set is exactly the intersection of what is written,
top first. A top-level holder whose scopes are `["kill:*"]` holds kill and
nothing else: no send, no read, no set. So a seat never gets a partial list.
It gets the full list below, or `*:*` plus nothing.

Writing a scope is also what makes reach explicit. `TopologyReach` lets a
figaro act beyond its lineage only when its effective set covers the verb on
the target and a `system.scopes` is written somewhere on its lineage (or the
policy sets a `[figaros]` ceiling). Implicit `*:*` does not count.

## Scopes per seat

The recommended value for every seat that reaps across the fleet is the
explicit form of what it holds today:

```
system.scopes = ["*:*"]
```

It keeps every current power and adds the fleet topology grant. Seats that
do not reap others' figaros need nothing written: their own subtree is still
theirs.

| Seat | Needs fleet topology | Write |
|---|---|---|
| King of Spain | yes: retires and kills across every regiment | `["*:*"]` |
| Figaro Engineer | yes: reaps idle figaros to free slots, retires lanes it did not fork | `["*:*"]` |
| Figaro Knight | yes: retires reviewers and fan-out forks of other seats | `["*:*"]` |
| Figaro Strategist | no: retires only its own forks | nothing |
| Review Steward | yes, if it retires reviewers: a reviewer is a turn-1 fork of the Strategist, drawn under the Strategist, not under the Steward | `["*:*"]` |
| El Zapador, El Pregonero, El Taller | no | nothing |

A narrower list, if Gluck wants one, must still name everything the seat
does, because the written list is all it holds. For the King, the Engineer
and the Knight that is at least:

```
["read:*", "listen:*", "send:*", "fork:*", "set:*", "delete:*", "create:*",
 "mint:*", "kill:*", "adopt:*", "decide:*", "use:*"]
```

`*:*` covers `adopt:lineage:self` (consenting to a figaro moved under the
seat) and `use:sandbox:local` (naming a host cwd), which every seat needs: a
`*` verb is every figaro verb and a `*` object every resource. A seat given a
narrower list must name both, or `adopt:*` and `use:*` as in the list above.
`kill:*` covers demote, promote and kill everywhere. `set:*` (or
`write:*#system.retired`) covers retire everywhere. `send:*` covers messages
to Gluck's other seats. `admin`, `join`, `lease` and `say` are left out: no
seat uses them through its own claim today. This list differs from `*:*` only
by those four verbs, so `*:*` is the simpler choice.

## Where the scopes live, so succession carries them

On the holder's own bound form, written once by the owner:

```
env -u FIGARO_ARIA figaro set --id <holder> system.scopes '["*:*"]'
```

- Succession copies them. The successor's `system.scopes` are the old
  holder's own (or its effective set when ancestors narrowed it), so a seat
  keeps its grant through every hard, overlap or familial succession with no
  further write. This is the path that matters, and it needs nothing new.
- Not the role form. Scopes on a role form narrow nothing and grant nothing
  to its holder: grants are read off the figaro's own lineage, never off the
  forms it holds.
- Not the shared outfit. Every seat's `succession-policy.outfit` is
  `default,succession-standard`, shared by all seats, so a scope there would
  reach seats that must not reap. Once the charter (E6) renders one outfit
  per seat, the seat outfit should carry the same `system.scopes`. That
  covers a holder minted by hand (`figaro new -C @role`), which succession
  does not reach. Until then, a hand-minted holder needs the `figaro set`
  above.
- Not a birth patch. It covers a single birth and is lost on the next
  succession.

## Sends a default child loses, and the child_policy that keeps them

A fork made by a figaro is born with
`["send:parent", "read:aria:self", "write:aria:self#@self", "delete:aria:self#@self"]`.
Its parent is the figaro it was drawn under at birth. A send to a role
counts as a send to the parent only while that role resolves to the parent.

Sends a lane makes today that a default child loses:

| Who | Sends to | Why it is lost |
|---|---|---|
| Strategist fan-out forks | `@9bf485c2` (Strategist role) | Fine while the parent holds the role. Once the Strategist is succeeded, the role resolves to the successor, who is not the fork's parent. This happened tonight: cognitive-swimwear was succeeded by dozed-outhouse while its forks were working. |
| Fan-out forks | `@d121c404` (Knight), directly | Not their parent. |
| Lanes the Engineer forks | `@ee927149` (Engineer role) | Same succession gap as the Strategist's. |
| Lanes the King forks (buscador, cantero, fermata, pregonero prompts) | `@96447061` (King) | Same succession gap. Some also report to Gluck through herald, which is not a figaro send. |
| Lanes messaging El Zapador | `@662ee4fe` | Not their parent. |

Each seat that forks lanes should carry a `system.child_policy` naming its
own role and the seats its lanes report to. The role selector takes the role
form's id, so the grant follows the role through succession:

```
# on the Strategist holder
system.child_policy = { "system.scopes" = [
  "send:parent",
  "send:role:@9bf485c24507fe8484a001c18a1b84cc",
  "send:role:@d121c4041785b13c6261d6055f113f8d",
  "read:aria:self", "write:aria:self#@self", "delete:aria:self#@self",
] }

# on the Engineer holder
system.child_policy = { "system.scopes" = [
  "send:parent",
  "send:role:@ee9271497f3823d7f50922bec4899818",
  "send:role:@96447061",
  "read:aria:self", "write:aria:self#@self", "delete:aria:self#@self",
] }

# on the King holder
system.child_policy = { "system.scopes" = [
  "send:parent",
  "send:role:@96447061",
  "send:role:@662ee4fedbd0fdbad7ad6adef20cdeee",
  "read:aria:self", "write:aria:self#@self", "delete:aria:self#@self",
] }
```

`system.child_policy` is lineage class: only Gluck, or an ancestor figaro,
writes it, and a seat cannot write its own. It lives on the holder, and
succession carries it to the successor with `system.scopes`,
`system.retirement`, `system.sandbox`, `system.tools` and the fanout keys
(added in this change). A holder minted by hand needs it written, or carried
by its seat outfit once E6 renders per-seat outfits.

## A label on the local door is never the owner

A call on the local door that names a figaro (FIGARO_ARIA set, as in every
figaro's own shell) is judged as that figaro's tools are under the peer
check (skills/figaro/reference/peer-check.md), never as the owner. The label
only narrows, so it needs no proof. It writes the keys its scopes reach, a
protected key (system.scopes, system.sandbox, the fanout keys,
system.child_policy, target-aria) only on a figaro below it, and an
owner-held key (system.model, system.provider, system.tools,
system.retirement, succession-policy, ...) nowhere. Of the lifetime it only
shortens one or retires, within its reach. With the peer check on, a tool
shell is already its figaro by its cgroup; the label decides for a shell
outside every tool cgroup that names a figaro, and for every shell when
peer_check is off. Before P5 such a call skipped every key rule unless its
params named one of a few words.

Gluck writes owner-held keys from his own terminal, unlabelled:
`env -u FIGARO_ARIA figaro set ...`. Nothing in the fleet's routine needs a
figaro to write one: a seat that switched its own model through a labelled
`figaro set system.model` now asks Gluck, or carries the model in its
outfit.

## Order

1. Before the P5 binary runs: write `["*:*"]` on the King, Engineer,
   Knight and Review Steward holders.
2. Write the child policies above on the seats that fork lanes.
3. Restart onto the P5 build (Gluck's take).
4. Check with `figaro scopes <holder>` (effective `*:*`, own `*:*`) and
   `figaro form show <holder>` (`system.child_policy`).
