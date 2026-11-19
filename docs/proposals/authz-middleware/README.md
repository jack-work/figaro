# Authorization as middleware

Proposal, 2026-11-19, on figaro v0.76.1 (`50d40a9da`). Design only; no code changes.

Gluck: "It's really middleware and I want to be damn sure it's thoroughly isolated as a separate module from the rest of the code, and applied modularly on certain hooks or endpoints or actions rather than peppered in throughout the codebase."

## Where it stands

The skeleton is already middleware. Every daemon RPC passes one guard before its handler:

- `internal/angelus/protocol.go:197` wraps every method in `authz.Guard` (`internal/authz/authz.go:236`), which authenticates, builds a `Request` (subject, door, method, aria, params) and asks one `Policy`.
- The policy is `authz.Enforce` (`authz.go:312`). It maps the method to a verb through `rpc.VerbOf` (`api/rpc/verbs.go:151`, table at `verbs.go:38`); a method with no verb is refused. That table is the declared-action table, and it is default deny.
- Connection admission (`authz.Admit`, wired at `angelus/door.go:143`), authentication (`authz.Authenticated`) and claim dropping (`Policies.DroppingClaimsNotHeld`, `authz.go:436`) wrap the same chain at `protocol.go:200-206`.

The peppering is everything the guard lets through on purpose and leaves to the handler. Measured on `50d40a9da`:

| | count |
|---|---|
| non-test files importing `internal/authz` outside it | 63 (50 in `internal/angelus`) |
| methods the guard waves through as "checked by handler" (`authz.go:290`) | 13 |
| methods the guard passes for per-row filtering (`authz.go:300`) | up to 6, by form id |
| in-handler `h.allowed` / `asPeerNode` decisions | 43 call sites in 21 files |
| in-handler key-level refusals (`refuseCallerKeys`, `refuseCallerPatch`, `checkCallerKeys`, `refuseProtected`) | 15 call sites in 6 files |
| `Policies.Shadow / WouldRefuse / Enforces` switch checks in handlers | 12 |
| decision code inside `internal/authz` | 3,831 lines (+5,976 test) |
| decision code living in `internal/angelus` (`box_keys.go` alone) | 926 lines |

Most of the 63 importers do not decide anything: they read identity from the context (`authz.FigaroOf` 33 uses, `PrincipalOf` 24, `PrincipalText` 24, `SubjectOf` 20). That is identity plumbing that happens to live in the authz package.

## Inventory of decisions

Each row is one kind of decision, the hook it sits on today, and where it should sit.

| decision | today | hook | target |
|---|---|---|---|
| connection admitted (peer uid, door) | `authz.Admit`, `door.go:143` | connection | stays: transport middleware |
| caller identity (peercred, bearer, issuer, API key, node leaf) | `authz/authn.go`, `apikey.go`, `peer/door.go:139` | per call | stays: authn middleware; moves to its own package |
| verb on resource (read/send/fork/set/kill/admin... on aria, daemon, pool, sandbox) | `Enforce`, `authz.go:312` | RPC method | stays: the one decision point |
| box door claim rules (`figaro_claim = "required"`, pinned box) | inside `Enforce` | RPC method | stays |
| per-row filtering (list, `@arias`, topology, troupe, recent forms) | handlers call `view.Allow` per row: `resolve_id.go:391-426`, `topology_consent.go:33`, `sandbox_grant.go:328`, `arias_form.go:198` | result | a `Filter(request, rows)` hook the guard runs on the response |
| troupe, GC, import, node learn (13 methods) | each handler calls `h.allowed` itself (`sandbox_grant.go:177`, e.g. `troupe_sign.go:83`) | RPC method | the resource is computable from params: declare it in the method table and decide in the guard |
| key grants on form writes (P5: `write:*#mantra`, protected keys, lifetime keys, retention) | `box_keys.go` 111-926, called from `protocol.go:259,503,610`, `form_apply.go:73`, `topology_handlers.go:126,350,800,808`, `reaped_restore.go:71` | form patch | a `Patch(request, before, patch)` hook, one call site in the form-write path |
| fan-out quota and mint budget (P6) | `box_keys.go:536-670`, `fanout_cache.go:698`, `topology_guard.go:51` | form patch and birth | the patch hook (budget keys) and the birth hook |
| hierarchy plans (move, purge, sweep, consent) | `authz.TopologyGuard` (`authz/topology_guard.go:95`), called by `internal/hierarchy` | hierarchy plan | already a hook (`hierarchy.Guard` interface); keep |
| outfit authority (dressing may not raise grants) | `dressing.go:147-153`, `protocol.go:252` | aria method | the patch hook (an outfit is a patch) |
| cast / succeed seats | `role_verbs.go:13-39`, `cast_placement.go:24` | RPC method | verb `cast`/`succeed` already exists: decide in the guard |
| owner-only switches (traceparent trust, owner-made births) | `traceparent.go:39`, `topology_handlers.go:368,968`, `form_apply.go:153` | per call | an `Owner(ctx)` fact the guard computes once and hands down; handlers read the fact, never the policy |
| local tool attribution (P4 peer check) | `daemon/tool_cgroups.go`, `daemon.go:432` | tool exec | stays: authn for tool callbacks |
| enforcement switches (key grants, child policy, quota: off, shadow, on) | `Policies.Shadow/WouldRefuse/Enforces` at 12 sites | everywhere | inside the decision point only; a hook returns a decision and the switch is applied once, by the guard |

## The boundary

Two packages, and nothing else decides.

1. `internal/authn`: who is calling. Admission, peercred, bearers, issuers, API keys, node leaves, tool cgroups. Output: a `Subject` and an `Identity` in the context, plus read-only accessors (`FigaroOf`, `PrincipalOf`). Moving the accessors here removes most of today's 63 importers of `authz`.
2. `internal/authz`: may they. One type:

```go
type Decision struct { Allow bool; Hidden bool; Reason string; Fix string }

type Point interface {
    Call(Request) Decision
    Patch(Request, form.Snapshot, form.Patch) Decision
    Birth(Request, Birth) Decision
    Filter(Request, []Row) []Row
    Plan(Request, hierarchy.Plan) Decision
}
```

Each hook is invoked from exactly one place:

| hook | invoked by | today's sites it replaces |
|---|---|---|
| `Call` | `authz.Guard` around every RPC | `Enforce`, 13 handler-checked methods, cast/succeed, 43 `h.allowed` sites |
| `Patch` | the form-write path in the store adapter (one function every set/apply/birth patch already passes) | 15 key-refusal sites, outfit authority, quota/mint keys |
| `Birth` | fork/new admission | `fanout_cache.go:698`, birth policy, mint budget |
| `Filter` | `Guard`, on responses of methods declared as row-returning | per-row `view.Allow` loops |
| `Plan` | `internal/hierarchy` (exists as `hierarchy.Guard`) | `TopologyGuard` |

Rules, each enforced by a test:

- A method missing from the declaration table is refused (exists via `VerbOf`; extend the table to carry the resource extractor and whether rows are filtered).
- Only `internal/angelus/protocol.go`, the form-write adapter, the birth path and `internal/hierarchy` import `internal/authz`. A test walks `go list -deps` and fails on any other importer.
- Handlers receive facts (`Owner`, `Figaro`, `Door`) from the context, never a `Policies` value.
- Enforcement switches (`off | shadow | on` per family) are read in one place, `authz`, after a hook returns. No handler names a switch.

## External decisions (Kanidm, gateways)

Gluck's direction: service access (which APIs a figaro may call) is decided in Kanidm by gateways, rou2 among them; figaro-specific rules (hierarchy, who may message whom, fan-out, quota) stay in the daemon.

The boundary fits that without a new layer:

- Outbound API access never passes through the daemon's authz. A figaro's request leaves its box with its hush-issued identity (a JWT-SVID), and the gateway checks Kanidm. The daemon has no say and needs none.
- What the daemon must supply is the identity: the SVID's subject names the figaro (`spiffe://kelliher/node/<n>/figaro/<id>`), and the figaro's role names the Kanidm account (a service account per role, not per figaro: 2.2k figaros and growing would churn Kanidm, roles are stable). That mapping is an `authn` concern.
- If a daemon verb should ever defer to Kanidm (for example `use` on a provider), it is one more `Point` implementation composed in front of the local one. `Call` already has the shape a remote policy decision point needs: subject, verb, resource in; allow, deny and reason out.

## Refusals that say how to fix them

`Decision.Fix` is new: where a refusal has a remedy, it carries the exact command. Examples of what the hooks know:

- key grant: `figaro set --id <granter> system.scopes '<…> write:<target>#<key>'`
- box claim: `sandbox.boxes.<box>.figaro_claim` in config
- valet path (planned): `figaro set --id <figaro> system.valet.paths.<n>.path <dir>`

The agent shows the refusal and the fix to the model as the tool result, and the model relays it in chat. One format, one place. Today several refusals already name a key (`authz.go:322-324`, `box_keys.go:330`); the rest name nothing.

## The carve

Each step ships alone, keeps every decision identical, and is net-negative in lines or close to it. The proof for every step is a decision-equivalence table: the existing decision grid (`authz/decision_grid_test.go`) plus a new table that runs every inventory row above as (caller, method, params) -> allow/deny/hidden and must not change across the step.

1. **Equivalence table first.** Build the table from the inventory and pin today's answers. No production change.
2. **Split authn out.** Move admission, peercred, issuers, API keys and the context accessors to `internal/authn`. Mechanical; removes ~50 importers of `authz`. Breaks nothing on the wire.
3. **Declare resources.** Extend the method table with a resource extractor, then move the 13 handler-checked methods and the 43 `h.allowed` sites into `Call`. Handlers lose their checks.
4. **One patch hook.** Route every form write through one adapter that calls `Patch`; fold `box_keys.go`'s refusals, outfit authority and the quota/mint keys behind it. The biggest step; `box_keys.go` leaves `internal/angelus`.
5. **Birth and Filter.** Admission and fan-out quota behind `Birth`; per-row loops behind `Filter`.
6. **Switches inside.** `Shadow/WouldRefuse/Enforces` disappear from handlers.
7. **The import test.** Turn on the importer rule. From here a new check outside the hooks fails CI.
8. **`Fix` text.** Fill remedies per refusal family.

Steps 2 and 7 are small; 3 and 4 are each a lane with an Opus review (authz is on Gluck's Opus list). Steps 1-3 can start now; 4 waits for the valet-grants change (protected bound-form keys) so it is not carved twice.

## What breaks

Nothing on the wire or on disk in steps 1-7: decisions are pinned by the equivalence table. Refusal messages change wording in step 8. Anything outside figaro that imports `internal/authz` (none found) would need the new package.

## Open for Gluck

1. Kanidm account per role (proposed) or per figaro.
2. Whether the daemon ever defers a verb to Kanidm, or Kanidm stays gateway-only (proposed: gateway-only for now).
3. Order against the valet-grants change: valet first, then step 4 (proposed).
