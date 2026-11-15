# Italy: first source slice and existing identities

Prepared 2026-11-15 for King XV. Source preparation only: no runtime role creation, cast, outfit update, topology write, host switch or deployment performed. The agreed charters and missing-position identities still come from the King. The HTML review stays with b6926f81; this note does not replace it.

## What changed after review

Read the 13 threads returned by `kc ls jack/figaro 4674ecd5f93d9736356e46a47129eb23fe01b047 docs/proposals/project-italy/formation-review.html --json`, selecting only that path.

The host flake composes outfit updates and applies the owned outfit values to runtime role forms and reviewed `@topology` edges. Live roles and topology are the order site's source. A build-time Nix store file may carry arguments to an apply operation; it is not a second runtime registry, editable manifest, controller database or web data source.

The other review corrections are part of the source boundaries:

- The new Italy Engineer is distinct from the existing Figaro Engineer. Reuse Zanni for the merged UI/metrics staff; do not create a second UI owner. Sys1 staff researches, builds and maintains automation and methods, rather than manually running every prediction. Its officer title is still to be named; Voltigeur names the automation.
- Inspector-General commissions coherence/security fixes and appropriate rollback work. Do not turn ordinary local software changes into a new approval staircase or merge this role with the narrow Spain Inspector by accident.
- Staff may research with Brave and commission bounded R&D. Italy's uniform authorization and sandboxing are dependencies to implement, not capabilities obtained by drawing a parent edge.
- Metrics are added incrementally; absent measurements remain absent. Rou2 staff remains beneath the King and coordinates with UI staff across that boundary.
- The first replica is an isolated pinned local fixture, then Merlin after acceptance. A Spain VM is a later alternative, not a prerequisite or deployment authorization.
- Read-only order target is one to two days. Neither the timeline nor broad plan approval authorizes interference with Merlin or unnamed Spain deployments.

## Slice 1: adopt and check existing roles

The smallest useful change is a generic existing-ID outfit apply/check path, not missing-role creation. Existing RPCs already provide the storage and outfit operations; one bounded daemon entry is needed to enforce owned-path policy at the write boundary rather than trusting a browser or shell planner:

| Existing source | Use |
|---|---|
| `sdk/angelus.go:OutfitForm` | Ask the daemon to fold named outfits using the same daemon outfitter as other writes. No duplicate TOML parser. |
| `sdk/angelus.go:Form`, `sdk/aria.go:Form` | Read the existing role and durable version without a model turn. |
| `sdk/aria.go:Set`, `SetDressed`, `api/rpc/turn.go:SetRequest.IfVersion` | Write a pure owned-key patch against the read version. A stale read is an explicit conflict, not a successful overwrite. |
| `internal/store/form.go` | Existing single writer reduces no-op patches and preserves durable version when nothing changed. Do not add another diff-after-read write path. |
| `internal/angelus/topology_form.go:arrange` | Existing guarded topology operation, authorized per moved node and parent. A topology request may park for adoption; report it, never claim the display already changed. |
| `nix/module.nix:services.figaro.outfits` | Existing NixOS outfit rendering. Share this rendering where useful; do not duplicate a second role definition format. |
| `nix/hm-module.nix` | Add the same outfit declaration/application support to Home Manager without tying it to `restartOnSwitch`. No daemon restart is required to patch an existing role. |

Proposed source footprint:

1. A small owned-value planner behind one daemon entry, independent of node names and our kingdom. Inputs are the exact target ID, named outfits, explicit owned leaf paths, expected role version and check/apply mode. The daemon folds outfits, validates owned paths, and submits a patch through its existing single writer with the expected version. Output is the desired change/check report. No stored formation object, no role registry and no machine-specific IDs in the implementation. Reuse authorization and refusal checks from form.set; this is not an alternate privileged write path.
2. One CLI entry beside the form verbs, using `internal/cmdkit` and the SDK. Suggested shape: `figaro form apply --id @FULL-ID -O ROLE-OUTFIT --owned charter,supervisor,projects --check -j`. This is a proposed interface, not a command available today. The final owned paths come from the agreed outfits, not this illustrative invocation. `--check` never writes. Apply reports changed/unchanged/missing/conflict/refused, with diagnostics on stderr. It never sends a prompt or silently mints a target. The new method needs a capability entry and minor release; a missing capability refuses explicitly. Do not bolt ownership enforcement onto optimistic browser edits.
3. Shared Nix position submodules `staff`, `colonel`, `commander`: compose outfits and explicit full role-ID bindings, generate bounded apply invocations after outfit installation. Package the mechanism in figaro, compose our actual hierarchy in the host flake. No private data or credentials enter a Nix derivation.
4. A separately visible topology step for the reviewed edges, with exact role IDs and no broad normalize/reparent sweep. A failed or parked edge remains pending in the report. Report partial application across roles; do not promise a multi-form transaction.

### What the planner must preserve

Owned paths are explicit leaves or deliberately owned whole objects, with overlap validation. Do not replace an entire role snapshot, or replace a parent object if that erases unowned descendants. An outfit may apply a declared value only at an owned path. A removed field is removed only when the caller explicitly still owns that path; stop managing a position does not delete its role.

Never derive ownership from a list of arbitrary keys found on the current form. Preserve `target-aria`, handoff, comments, unowned notes, studies, inference configuration, authentication material and live runtime state. In the first role-charter slice, reserved runtime/identity/security paths are rejected from the owned-path list rather than passed through from an overbroad outfit. Later deliberate policy management must use its own reviewed authorization contract, not accidentally arrive through a charter apply.

Outfit composition must reject conflicting typed charter parts at Nix evaluation, not concatenate contradictory instructions. The apply operation uses the installed immutable outfit generation and reports its identity; a check against one outfit generation cannot silently apply another. A role version conflict or an outfit generation change yields a bounded re-check, not an infinite reconciliation loop.

The flake owns desired charter and reviewed presentation values. Occupants and their succession remain runtime-owned. The desired supervisor and drawn parent must be reported separately if they differ; the reader does not treat one as proof of authority.

### Failure and activation behavior

Missing, ambiguous, moved or bound-form targets refuse the existing-ID path. Require the exact inventoried identity; names and display handles are not adoption keys. Preserve canonical legacy IDs such as `@96447061` as returned by the daemon; do not pad or manufacture a 32-hex replacement.

A stopped daemon is a visible deferred apply, not a reason to start it. An old daemon lacking a required capability is an explicit incompatibility, not a shell fallback. A host switch must not call a model, restart the daemon, arm officer heartbeats, or change a worker's provider. Keep the apply result available for the operator's later retry. No new always-on reconciler is needed for this first slice.

Checks stay small: preservation of unowned nested values; explicit owned removal; no-op second application; version conflict; wrong/missing ID; refusal of reserved paths; outfit-generation mismatch; separately failed/parked topology. Exercise the actual command in an isolated daemon. Do not add another broad figaro test suite as a prerequisite.

## Slice 2: optional missing-position create and bind

This is a daemon capability, not `form ls` followed by a shell `form new`.

Current `FormCreateRequest` in `api/rpc/topology.go` accepts parent, patch and outfit names; the daemon chooses a new ID. `FormBindRequest` creates a dormant figaro separately. CLI `internal/cli/cast_new.go:autocast` explicitly reports partial creation/casting. None is a keyed atomic ensure operation. Calling them in sequence is not idempotent missing-position support.

The smallest shape to investigate is an explicit declared role ID with optional atomic create-and-bind under that identity. Repeated/concurrent calls must return the same role and occupant. The chosen role is itself the durable binding: do not add another runtime identity registry with a second name for the position. New ID allocation is an explicit provisioning step outside Nix evaluation, agreed before apply; it is not a UUID freshly generated on every switch.

Before enabling this optional path, define its store-level crash recovery and identity reservation: create/bind visible coherently, retries after a crash cannot leave a second occupant, and a pre-existing role's occupant is never overwritten. Validate parent/outfit/create/set authority before durable effects. Capability-negotiated callers fail closed on older daemons. No client or browser implements a read-then-create fallback. Auto-create remains off until the King provides agreed charters, target identities and separate activation permission.

## Existing identities, not a new authority

[existing-ids.json](existing-ids.json) is a read-only evidence snapshot from this node's `figaro form ls -j`, captured November 15. It is not an apply input or a runtime registry. Holder values and presentation may change; a real apply re-resolves and verifies the stable role ID.

Adopt in place:

| Role | Exact ID returned by daemon | Intended treatment |
|---|---|---|
| King | `@96447061` | Keep the existing commander. |
| Figaro Engineer | `@ee9271497f3823d7f50922bec4899818` | Keep software/releases owner; not the new Italy Engineer. |
| Il Zanni | `@62c5c9bf8794ec5b8d1d00d05dd26c69` | Existing UI owner for the merged staff, subject to agreed charter. |
| El Formista | `@fbcfb5141604ef50e5c8dce0ef570711` | Keep headless state-client owner beneath Figaro Engineer. |
| El Aduanero | `@f48f567a727d703effa5e2fa13ebd625` | Remain beneath King; capacity/router owner. |
| El Zapador | `@662ee4fedbd0fdbad7ad6adef20cdeee` | Existing Spain executor; no new executor. |
| Spain Inspector | `@53c6b72b` | Keep its narrow auth/secrets/router/deploy responsibility. |

Observed identity traps:

- Formista's charter says Engineer lieutenant, but its current parent and presentation are `null`. Moving its display beneath Figaro Engineer needs the reviewed `@topology` operation. Do not rewrite historical ancestry to make the chart tidy.
- `@b906c598b1857b6bfe350e077cf67ff6` (edge) and El Zapador point to the same current holder. `@042d641e` (old Coordinador) and King also point to the same holder. These are existing aliases/old seats, not invitations to create or delete more officers. A shared holder is not evidence that two role identities are interchangeable.
- No role named Italy Engineer, Topo or sys1 appears in this node's current form inventory. Topo may reuse Zanni as approved; Italy Engineer, sys1 officer and Inspector-General identities remain unresolved until the King confirms the existing owner bindings, including any other node. This local result is not proof that no such role exists anywhere.
- The roster's old `role: @f062f8d7` is not the current Figaro Engineer identity. Do not use that stale ledger field as an adoption authority.

## Formista and the order-site boundary

Formista was contacted directly on the existing role. Its old Anthropic turn failed rate-limited; the King then explicitly set both holder and role to `rou2/chatgpt-personal/gpt-6.1-sol`, methods enabled, and authorized resumption until subscription exhaustion. The bounded client-seam request was sent once after that correction. No new owner or paid fallback.

Existing library paths: `figaro-forms/packages/figaro-forms/src/{client.ts,replica.ts}` and its `auth`, `semantics`, React and Jotai entry points. Formista confirmed that role/topology subscriptions and access/version metadata already exist: no new registry or renderer seam. It found a real reconnect bug: caller-local @topology may restart at a lower version with no new incarnation, while Replica rejects the fresh seed; reconnect denial can also leave the old tree/access displayed. Source-only GO given for a bounded connection-generation refresh, stale/resyncing/current status, stale-callback exclusion and authority invalidation on refusal. No deployment authorized. Its inspected figaro baseline was ca031c804; the relevant topology/view/access source is unchanged through serving 180d4906. Zanni owns chart components and the metrics-server join.

For source writes, one raw `figaro.set` with `if_version` already supports existing-role CAS. Do not attach `client`/`seq` to that request: the store rejects sequence plus conditional version. `Replica.edit` is unconditional optimistic editing and is not the reconciliation path. Topology arrange has no optimistic origin/last_seq contract either. A disconnect/timeout after a write has unknown outcome: re-read/check before deciding what remains, never blindly retry.

The order reader uses live role data and caller-filtered topology, enriched by named figscope/rou2 observations. It must not send an owner's complete form snapshots to the browser and then hide fields in JavaScript. Admin is the proposed initial viewer; Jack is still conditional on the approved identity binding. Other logged-in users get the `no owned orders` state and no create path. Unauthenticated users still follow the service's normal login/refusal behavior.

Uniform auth and sandboxing remain explicit Italy dependencies. Existing caller authorization must continue to filter every read; a visual supervisor edge grants no new read, write, create, tool or sandbox permission. Initial read-only deployment stays behind existing approved authentication. New grants or deployment rules go through their owner and the narrow Spain review, not through this source-preparation note.

## Handback

This note prepares the first source change without applying the formation. Next inputs from King: final charter outfit fields; explicit new/existing bindings for Italy Engineer, sys1 and Inspector-General; reviewed presentation edges. Next input from Formista: current client contract and smallest missing seam, if any.

Then implement existing-ID check/adopt first in an isolated worktree. Optional atomic creation follows as a separately testable daemon change. No runtime formations are created while those inputs are pending. Pager/topology UI work remains with favorite-amuser `5dc37e866b8006b38542e24a3be0e566` on `lane/pager-ui-integration`; coordinate any topology backend overlap rather than editing that lane.
