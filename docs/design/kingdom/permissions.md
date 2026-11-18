# Kingdom, Track P: permissions

The permissions half of the kingdom charter: every role, outfit and fan-out budget declared in Nix, with the daemon actually enforcing who may change what on which figaro, and who may talk to whom.

- Strategist: Figaro Strategist (Permissions), role `@9bf485c24507fe8484a001c18a1b84cc`. Plans, orders, answers. Does not build.
- Vanguard: Figaro Vanguard (Permissions), role `@d121c4041785b13c6261d6055f113f8d`. Builds one change at a time on a lane branch, one reviewer per change.
- Integrator: Figaro Engineer, role `@ee9271497f3823d7f50922bec4899818`. Only the Engineer merges or releases, and only with Gluck's nod.
- Plan root: `~/.local/state/impresario/plans/kingdom-charter.md`. King's feedback, binding: `~/.captains-log/2026/11/18/king-feedback-kingdom-charter.md`.
- Base: main `ee767e87c` (v0.74.2).

## Where things stand (read on main, 2026-11-18)

- **Who may write a key is decided in five places that disagree.**
  - `api/form/protected.go`: 5 keys (scopes, grants, sandbox, fanout.max_children, fanout.mint).
  - `api/form/owner_held.go`: 17 keys.
  - The `Mode` field in `api/form/known_keys.go`, which labels most protected and owner-held keys "user settable".
  - `internal/formapply/managed.go` `validatePath`, which refuses all of `system.*`.
  - Prose in key descriptions.

  The readers are `internal/angelus/box_keys.go`, `internal/angelus/form_apply.go`, `internal/config/authz.go` and `internal/config/config.go`.
- **Grants.**
  - `system.scopes` holds `verb:kind:name` strings, parsed in `internal/authz/figaro_grants.go`. The kinds are `aria`, `lineage`, `role`, `owner`, `path` and `sandbox`, plus `*`.
  - A child's grants are narrowed by its parent's (`effectiveDown`). A figaro may only grant what it holds.
  - There are 15 verbs (`api/rpc/verbs.go`).
  - `set` covers a whole form: there are no key paths. There is no `parent` or `children` selector.
- **Hierarchy.** Layer 3 is further along than its ledger says.
  - Step 1 landed in v0.65.0: `internal/hierarchy` is a leaf, under boundary rules 6, 8 and 9.
  - Step 2 landed in v0.66.0: proposals, parking, receiving-parent consent and adopt. As of `b27162c2c`, every topology edit is a plan applied under an injected guard.
  - `system.fanout.max_children` has been enforced at fork and reparent since v0.74.0.
  - The fork check (`admitFork`, `internal/angelus/topology_handlers.go`) scans every node. It costs 181 µs when a cap applies.
- **The enforcement gap.** The daemon identifies a CLI caller by uid only (`internal/authz/peercred_linux.go`). A figaro with a bash tool can run `env -u FIGARO_ARIA figaro set ...` and be taken for the owner (#57). Gluck watched this set a protected key. Every rule below is honour until P4 lands.
- **Daemon-written keys are scattered across `system.*`.**
  - `system.forked_from`
  - `system.studies`
  - `system.tombstone`
  - `system.checkpoint.marks`
  - `system.succession_warned`
  - `system.retired`: on the owner-held list, but the daemon writes it.
  - `system.failure.*`
  - the `turn.*` and `inflight` keys

## Order

The King's order is binding: **P1, P4, P3, P2, P5, P6, P7.** The vanguard builds them one after another, in one worktree, sharing builds with its reviewer.

```
P1 registry ── P4 peer caller check ── P3 admission ledger ── P2 runtime.* migration
                                                     └── P5 key-path grants, child_policy ── P6 quota, role verbs ── P7 laws, reminder kinds
```

- **P4 comes second** because without it every later grant and law is honour.
- **P2 waits until after P3**, and lands only once every reader of the old key names is listed and has a commit ready.
- **P5 needs P1 and P4. P6 needs P3 and P5. P7 needs P1 and P5.**
- **Out of order, first:** a small fix that makes `system.study_incantation` accept a string. It ships ahead of P4 (see "Interleaved fixes").

Every change below states what it does, what proves it done, what it breaks, and which numbers it may move. Numbers are measured on a store snapshot (`figaro doctor snapshot`, about 2,500 figaros) in the `.#snapshot` devshell, inside a tmux server on its own socket. The live store and the live daemon are never touched.

## P1. Key registry (no change in behaviour)

**What.** One table in `api/form` (say `registry.go`). It lists every known key path or prefix with:
- its writer class, one of:
  - runtime: the daemon only
  - owner: Gluck
  - lineage: the owner or an ancestor figaro, and a figaro only grants what it holds
  - self: anyone who may write the form
  - law: reserved for P7
- a shape check for its value
- one line of documentation

Everything else becomes a view of that table:
- `Protected` and `OwnerHeld`
- the `Mode` field in known_keys
- managed apply's refusal list
- `figaro doctor keys`

Lookup is by longest matching prefix. Every key keeps exactly the class it effectively has today.

Two known misfits are recorded in the table but not fixed here:
- `system.retired`: daemon-written but owner-held.
- `decide.*`: `decide.base_url` is owner-class, while `decide.provider`, `decide.model` and `decide.api` are self-class.

The shape checks give P5 something to refuse bad values with at write time, instead of warning on every render. That warn-per-render is what produced the study_incantation log flood.

**Relation to `lane/managed-fanout` (5f83ebbac).** That lane is item 25 in Gluck's digest and the King's first Italy win. It lands as it is. P1 then replaces its `OperatorPrefixes` list with a lookup of class lineage, for an owner caller, and behaviour stays the same. If managed-fanout has not landed by the time P1 is ready, P1 rebases onto it and carries it.

**Done when:**
- A test fails if any key in known_keys is missing from the registry, or the other way round.
- A golden table built from the v0.74.2 lists shows `Protected`, `OwnerHeld` and the managed-apply verdict unchanged, for every known key and for a set of fuzzed paths.
- `box_keys`, `form_apply` and `formapply` tests pass unmodified.
- Sabotage: delete one registry row and the golden test goes red.

**Breaks.** Nothing on disk and nothing in the protocol. The help text from `figaro form help keys` may be reworded.

**Numbers.** Neutral. A classification is a map lookup, where today it walks a list. The vanguard also records the **daemon cold-start baseline** here: time from exec to the first answered `figaro ls` on the snapshot. Every later change compares against it.

## P4. Peer caller check (tool shells are never the owner)

**What.**
- Each CLI connection is attributed to the caller's cgroup: the peer pid from `SO_PEERCRED`, then `/proc/<pid>/cgroup`.
- The tools a figaro runs are started inside a systemd scope labelled with that figaro's id. Proposed: one scope per figaro session, reused, rather than one per call.
- A caller inside a tool scope is that figaro, whatever environment it strips. It is never the owner.
- `setsid` and double forks stay inside the scope, which a parent-pid walk would not survive.
- In house mode, the valet's cgroup counts as a tool too. This is the hook Track E's valet (E4) and house mode (E5) consume.
- Config: `[authz] peer_check = true|false`, defaulting to true once proven.

**Done when:**
- A bash tool running `env -u FIGARO_ARIA figaro set --id <other> system.model x` is refused, and the refusal names the figaro that tried.
- The same holds after `setsid` and after a double fork.
- Gluck at a terminal is unaffected.
- `peer_check = false` restores today's behaviour.
- These are proven with real systemd user scopes in a devshell, in a tmux server on its own socket. A test that fakes the cgroup does not count.
- Sabotage: skip the attribution and the test goes red.

**Breaks.** Scripts run from a figaro's tool shell to act as Gluck stop working. The vanguard lists every one found. The take scripts run from Gluck's or the King's terminal; confirm that.

**Numbers.**
- Starting a tool in a scope: target under 20 ms on the first call of a session, and close to zero after that.
- Attributing a CLI connection: one `/proc` read, target under 50 µs.
- No change to fork latency.

**Reviewer.** Opus: this change is authz.

## P3. Admission ledger in the fork path

**What.** Wire `internal/admission` from `lane/fork-admission` 257fa9940 into the daemon.
- It keeps two counters per figaro in memory: live children, and budget used. Budget is a stub until P6.
- They are rebuilt at start from the topology the daemon already holds, in about 0.5 ms.
- Fork, retire, reap, restore, reparent and succeed all keep them current.
- A fork reserves a slot on every ancestor before the figaro is born, then commits or undoes the reservation. Nothing locks the hierarchy.
- `admitFork`'s full scan is removed, and the topology guard's fan-out check reads the ledger.
- `figaro doctor admission` compares the ledger against a fresh rebuild.
- In the same change: daemon-side succession demotes the predecessor under its successor, as `succeed` does, so neither path counts a slot twice.

**Done when:**
- A race test passes: 16 goroutines for 20 rounds against a reference model.
- Concurrent forks under one cap: exactly as many as the cap succeed.
- The existing `max_children` tests pass unchanged.
- A soak run on the snapshot leaves `doctor admission` clean.
- Daemon-side succession draws the same tree as `succeed`.
- Sabotage: skip the ancestor reservation and the race test goes red.

**Breaks.**
- The refusal message is worded differently.
- When two forks race for the last slot, the second is refused at once while the first is still being born. Today it waits about 55 ms and is then refused.
- The drawn tree after an automatic succession changes shape.

**Numbers.**
- Fork RPC p50 stays at or under 64 ms.
- The cap check drops from 181 µs to under 5 µs.
- Cold start grows by under 1 ms.
- Eight concurrent births take no longer than 527 ms.

The real serialisation point is the store's lock held across a birth (`XwalStore.mu`): 439 ms without the hierarchy lock, 527 ms with it. Narrowing that lock is a separate change, offered only if it proves cheap.

**Reviewer.** Opus: this change is hierarchy and store.

## P2. The `runtime.*` namespace and its store migration

**What.** Every daemon-written key moves under one top-level property that only the daemon writes, owner included. The proposed name is `runtime`, pending Gluck.
- `runtime.forked_from`
- `runtime.studies`
- `runtime.tombstone`
- `runtime.checkpoint.marks`
- `runtime.succession_warned`
- `runtime.retired`
- `runtime.failure.*`
- `runtime.turn.*` and `runtime.inflight`, if they live on the bound form. If they sit on a separate status form, they stay there and that form becomes runtime class.

The migration:
- It is versioned and runs once at daemon start. It appends one rename patch per form, so the event history keeps the old names.
- It is idempotent, and a run killed midway completes on the next start.
- `figaro doctor normalize` performs the same migration offline.
- For one release, readers fall back to the old names.
- A reverse migration is shipped with it, for rollback.

**Readers that must change in the same window.** From the King's list; the vanguard checks for more before landing:
- figscope: rou2's Engineer
- the paperd notify units: El Taller
- `~/notes/spain/bin/fig-take-update` and spanyard-watch: the King
- herald and telefono: El Pregonero
- anything that reads `figaro ls -j` or `figaro state -j` for `forked_from` or `retired`

Each owner gets a ready commit, or a one-line diff, before P2 is merged.

**Done when:**
- After migrating the snapshot, no form holds any of the old names, and every value is identical at its new path.
- A second run changes nothing.
- A run killed midway completes on restart.
- The reverse migration round-trips.
- Old-name fallback is tested.
- A non-owner write to `runtime.*` is refused, and so is an owner write.

**Breaks.**
- On disk: one rename patch per form. A binary from before P2 cannot read the new names, so rolling back requires the reverse migration.
- In the protocol: every external reader listed above.
- In the cache: where a moved key renders into a prompt, each affected figaro pays one cold cache write.

**Numbers.**
- The one-time migration at start: target under 2 s for 2,500 figaros, measured on the snapshot.
- Cold start afterwards: unchanged.

**Reviewer.** Opus: this change is store and migration.

## P5. Grants on key paths, `parent` and `children` selectors, `child_policy`

**What.**
- **Key paths in grants.** The grammar gains a key glob: `set:<kind>:<name>#<key-glob>`, for example `set:lineage:@kingdom#system.thinking_*` or `set:*#system.**`. `read` and `listen` gain the same form, for key-level studies, depending on Gluck's open question 1.
- **Two new selectors.** `parent`, and `children` (a figaro's whole lineage minus itself).
- **Every write is checked key by key.** For each key in a patch, the writer's effective grants must cover the verb, the target and the key.
- **Classes now have teeth.**
  - Runtime keys are never grantable.
  - Owner-class keys are held by nobody but the owner unless the owner grants them.
  - "Grant only what you hold" applies to edits of `system.scopes` as well: whoever holds `set` on another figaro's scopes can still grant only what it holds itself.
- **`system.child_policy`.** A template on the parent, applied in the birth patch at fork. It can carry scopes, `fanout.max_children`, `fanout.quota` (from P6), incantations, and any self- or lineage-class key.
  - It is applied at birth and not kept linked. A live link would mean writing to every descendant, and cold caches whenever provider or credo change.
  - Children are widened afterwards by whoever holds a grant on them.
  - A `reapply` verb is deferred.
- **The default child policy** for forks made by a figaro is `scopes = ["send:parent"]`. Forks made by Gluck keep today's inheritance, as proposed. A child with no `send:parent` cannot talk to its parent.
- **Role addresses.** Sending to `@role` where the role resolves to the parent counts as `send:parent`, so lanes reporting to the Engineer's role keep working.

**Done when:**
- A matrix test passes across writer × target × key class × selector.
- Escalation tests pass: a figaro holding `set` on another figaro's scopes cannot grant past its own, and runtime keys cannot be written by anyone.
- A default child can send to its parent and is refused everywhere else.
- A child whose policy omits `send:parent` cannot reach its parent.
- Old scope strings still parse.
- Sabotage: skip the per-key check and the matrix goes red.

**Breaks.**
- New forks made by figaros can only talk to their parent by default. A lane that messages the King, El Zapador or Gluck needs that grant in its parent's `child_policy`. The regiment outfits must carry it.
- The scopes grammar is extended, so binaries older than P5 refuse the new strings.

**Numbers.**
- Checking a typical patch: target under 20 µs.
- Checking a send: no worse than today's order of cost.
- Fork latency: unchanged.

**Reviewer.** Opus: this change is authz.

## P6. Quota, role verbs, succession charging

**What.**
- **`system.fanout.quota`** sits beside `max_children`. They are independent, and the P3 ledger checks both.
- **Charges** follow laws Part 2, with the fork-admission correction: a child without its own quota costs 1 plus what its subtree uses.
- **Budget moves down only.** A parent re-grants from its own free budget. A labeled `figaro new` by a minter is charged to the minter.
- **Role verbs become grants:** `mint:role`, `cast:@role` and `succeed:@role`, checked when a figaro casts or succeeds. They also close the v0.74.1 hole where a role nobody holds, carrying `mint`, can be taken by any figaro with `cast`. `system.fanout.mint` folds into the `mint:` scope (open question 4).
- **Succession charges by how long the predecessor stays alive.**
  - Hard `succeed` is net zero: the two figaros swap quotas.
  - Overlap and familial succession cost 1, and are refused if the budget is not there.
- **No `fanout ask` verb.** A refusal names the free budget at every capped ancestor, so the figaro asks its parent in plain words.
- **Managed apply** can set quota from the charter.

**Done when:**
- Random sequences of fork, retire, reparent and succeed keep every invariant against a reference model.
- Each succession variant is charged as described above.
- Role-verb refusals are tested.
- `ls` shows the budget.

**Breaks.**
- A new lineage-class key.
- Minting now spends budget.
- Daemon-side succession accounting changes. Its drawn tree already changed in P3.

**Numbers.** The same as P3: admission stays under 5 µs.

**Reviewer.** Opus: this change is authz and hierarchy.

## P7. Laws and reminder kinds

**What.**
- **Laws stage 1** (keel `proposal/laws` cd64246e8): node laws, a law block in the system prompt, and the block refreshed only when the cache is cold anyway. The reserved tag is already escaped since v0.74.2 (#143).
- **Laws stage 2:** `law.*` on role forms, as the registry's law class. A law never renders as a study, so `figaro drop` cannot remove it.
- **Reminder kinds** (keel `proposal/reminder-kinds` d796074ea):
  - every reminder carries a `kind=` attribute
  - `system.say.<kind>` hooks replace `study_incantation` and `fork_incantation`, with old keys read as the new ones
  - all providers build their reminders from one shared list
  - on Responses, laws go in as developer messages
- **Tool results** get escaped too.

**Done when:**
- A law cannot be forged from tool output, a form value or another figaro's prose.
- A law cannot be dropped.
- The law block is refreshed only on a cold cache.
- Per-provider wire tests pass.
- The incantation migration round-trips.

**Breaks.**
- Each figaro pays one cold cache write when the law block first appears, at its next cold wake.
- The incantation keys are renamed, with migration.
- Tool output is escaped.

**Numbers.**
- Cache-read fraction: unchanged in steady state.
- Tokens per turn: rise only by the length of every-turn laws, and those are off by default.

## Interleaved fixes

**IF1. `system.study_incantation` accepts a string** (ahead of P4).
- **The bug.** `~/.config/figaro/outfits/base/core.toml:45` sets the key to a plain string. `ReadStudyIncantation` (`api/form/incantation.go`) accepts only an object. Every figaro born from core carries the string. Every render logs `incantation ignored: not an object`, about 325 times so far, and the sentence is never said.
- **The fix.** Mirror `ReadForkIncantation`: a bare string means `onstudy`, the event the sentence was written for. Objects work as before.
- **Done when:**
  - Tests cover a string, an object, a mixed bad object, and an empty string.
  - The warning goes away for strings.
  - Sabotage: drop the string branch and the test goes red.
  - The docs that describe the key say "a string, or {onstudy, onupdate, ondrop}": `skills/figaro/cli.md`, `forms.md`, `reference/forms.md` and `contributing/forms-design.md`.
- **Breaks.** Figaros carrying the string start saying "You now study this form; heed its changes." when they begin studying a form. That is what the outfit meant. Past turns are not re-rendered, so warm caches are kept.
- **Reviewer.** Sonnet, quick.
- **Outfit.** Optional and independent: change core.toml to `[system.study_incantation] onstudy = "..."`. It fixes new births on v0.74.2 straight away, and works on both binaries. Figaros already carrying the string are fixed only by IF1.
- **Later.** IF1 folds into `system.say.study` in P7.

## Contracts with Track E (Estate)

- **P4 → E4 and E5.** In house mode the valet's cgroup counts as a tool. Track E names the valet's unit and scope, and P4 reads them.
- **P1, P3, P5 and P6 → E6.** The kingdom module in `cheroot-config/charter` renders:
  - `max_children` now, through managed-fanout
  - `quota` after P6
  - scopes and `child_policy` after P5
  - laws after P7

  It applies them through managed apply, which uses the registry's classes after P1.
- **No change to the key model** from splitting hush, the daemon and the valet into separate services.

## Numbers and how to read them

| Number | Baseline | Where it comes from |
|---|---|---|
| Fork RPC p50 | 63.9 ms | the `scratch/fork-admission` harness, 67da356c1 |
| Capped admission | 181 µs | same |
| Daemon cold start (exec to first answered `figaro ls`) | recorded in P1 | the snapshot |
| Time to first output, cache-read fraction, key_to_paint, turn wall | figscope, gluck-local | figscope |

A change that moves any of these explains why, with the number, in its review.

## Open for Gluck (the Engineer relays)

1. Your sentence that was cut off: "control exactly what values of which figaros we can ___". Is it study, read, or set? P5 builds key-level `read`/`listen` only after this.
2. The name of the daemon namespace: `runtime.*` is proposed.
3. The default child policy `send:parent` applies to forks made by figaros, not to forks you make. Proposed: yes.
4. Fold `system.fanout.mint` into a `mint:` scope. Proposed: yes, in P6.
5. Make all of `decide.*` owner class. Proposed: yes; P1 records the misfit and P5 enforces the change.
6. Peer check granularity: one systemd scope per figaro session. Proposed: yes.

## Status

| Change | Branch | State |
|---|---|---|
| IF1 study_incantation string | `lane/study-incantation-string` | assigned to the vanguard; ships ahead of P4 |
| P1 registry | `lane/key-registry` | in progress (vanguard) |
| P4 peer caller check | | next after P1 |
| P3 admission ledger | | after P4 (prototype: `lane/fork-admission` 257fa9940) |
| P2 runtime.* | | after P3, once its readers are listed and ready |
| P5 key-path grants | | after P2 |
| P6 quota and role verbs | | after P5 |
| P7 laws and reminder kinds | | after P6 |
