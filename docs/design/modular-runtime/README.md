# figaro as a modular runtime: the staged plan

El Arquitecto, 2026-11-09, on figaro v0.58.4 (6d16a804). A design only: no code changes ride this branch.

It combines four inputs into one plan:

- your design: jack-work/figaro-design (the design, the take, the layering review), summarised in #75;
- the carve plan: backlog-2026-11 section 3 (steps 1-7; step 1 shipped in 0.58.3 at -1,263 lines) and the 11-04 forms/xwal audit rows still open (2, 3, 6, 8, 9, 10, 11, 12, 13);
- the standing order of 11-08: bottom-up, one step at a time at your approval, each step net-negative in lines;
- the captains-log of 11-09: primitive forms, per-session state, sandbox leases, lineage permissions.

| file | what |
|---|---|
| [index.html](index.html) | overview diagram: the target stack, today's code on it, wires, step and issue overlays |
| [runtime.html](runtime.html), [hierarchy.html](hierarchy.html), [bindings.html](bindings.html), [agent.html](agent.html), [host.html](host.html), [clients.html](clients.html) | one diagram per layer: components, files with line counts, the imports to cut |
| [steps.md](steps.md) | every step: what moves, what is deleted, what breaks, the gate |
| [decisions.md](decisions.md) | where the design and the carve plan disagree, and open questions; a recommendation for each |
| [issues.md](issues.md) | all 17 open GitHub issues against the steps |
| [mapping.md](mapping.md) | every package and split file on the target stack; every import that points the wrong way |
| [gen/](gen/) | `model.py` holds the plan's data; `python3 gen/gen.py` re-measures the tree and rewrites the HTML and the four generated files |

## 1. The target stack

Imports point down only. Nothing below the bindings names "conversation", "outfit" or "figaro".

| layer | module | owns |
|---|---|---|
| clients | figaro (client binary) | cli verbs, pager, input, sdk; loads one command set per module the server advertises |
| host | figarod | composition and the module registry; RPC, authz, outfits, credentials, UI stream, session state |
| run | figaro agent | the agent step: provider logs (owned), catch-up, marshalling, tool loop, decide, methods |
| run | bash harness | a second `run` (new) |
| run | tools + sandbox | tool execution behind api/sandbox; figbox |
| bindings | figaro bindings | aria + bound form = a figaro; `run(drained inbox) -> ops`; studies, librettos, roles, ForkPoints |
| optional | hierarchy | fork, lineage, drawn tree, Materialize, promote/demote/adopt, reaper roots |
| optional | federation | moves, peers, troupe |
| runtime | runtime + WAL | actors, aria and form primitives, ids, group commit, published versions, checkpoints, owned primitives, GC roots |
| base | api + leaf libs | wire types, form algebra, sandbox contract, config file, telemetry |

Two placements are mine, not the design's: tools + sandbox as a peer of the agent (carve step 5), and `internal/config` in base because both binaries read it. Both are open to you.

## 2. Today on that stack

From [mapping.md](mapping.md), measured on 6d16a804 (non-test lines):

| module | lines | note |
|---|---:|---|
| clients | 61,190 | internal/cli alone is 52,333 |
| host | 24,338 | angelus 13,152 |
| agent | 19,756 | providers 9,925 |
| runtime + WAL | 20,112 | includes the two split files, xwal_backend.go and xwal_store.go (2,656) |
| api + leaf libs | 12,756 | |
| federation | 4,564 | |
| tools + sandbox | 4,091 | |
| bindings | 3,998 | |
| hierarchy | 2,676 | spread over xwal, store and topo |
| deleted outright | 1,086 | flatten.go, migrate.go, meta_heal.go |

Under this mapping, 114 file-level imports point the wrong way. The largest groups:

- clients -> host, 50: `boot` in 34 places (paths, build identity, hush claim), `auth` for login flows, `authz`, `outfit`, and `cmd/figaro` -> `daemon`. Most of it is boot's shared half living in a host package. K1 moves it.
- agent -> host, 17: providers and decide call `auth.TokenResolver` themselves. The host should inject a token source.
- clients -> runtime, 11, and clients -> agent, 10: the 23 allowlisted edges of check-boundary.sh, seen file by file.
- clients -> federation, 8: the troupe verbs use troupe's key and certificate code directly.
- bindings -> host, 7: `authz` in form, study and server; `livelog/aria` in projector and read; `outfit` in server.
- runtime -> agent, 2: only through `migrate.go` and `meta_heal.go`. S1 deletes both.

## 3. The order

Three tracks. S is the runtime, bottom-up and serial; K (clients) and X (sandbox) run beside it. Each step is one release.

```
S0 figwal (shipped 0.58.3)
S1 delete migrations ─ S2 flatten Backend ─ S3 split store by layer ─ S4 one runtime layer
                                                                       ├─ S5 checkpoints ─ S7a Prefix ─ S7b fork form ─ S7c ForkPoints ─ S7d registry
                                                                       └─ S6 owned primitives ─ S8 run -> ops ─ S9 agent owns provider logs ─ S10 deletions
K1 client binary ─ K2 split cli ─ K3 command sets (after S7d)
X1 tools through api/sandbox ─ X2 sandbox identity and leased grants (after S7d; design first)
```

| step | from | lines | on disk |
|---|---|---|---|
| S1 delete one-shot migrations | design H, audit 11 | about -1,000 | refuses stores below layout 4 |
| S2 flatten the store contract | design A, review 1, audit 10 | -600 to -900 | none |
| S3 split the store by layer | design A, carve 2 | -300 to -500 | none |
| S4 one runtime layer | design B, review 3 and 6, audit 9 and 12 | -700 to -900 | none |
| S5 checkpointed reducible channel | design C, audit 3 and 8 | negative only with audit 3 and 8 riding | layout 5 + converter |
| S6 owned primitives | design E | at risk | none |
| S7a Prefix and Materialize | design D1-D2, audit 6 | -150 to -400 | flat fork dirs + converter |
| S7b fork state as one form | design D3 | large negative | markers retired + converter |
| S7c ForkPoints | design D4 | small negative | none |
| S7d manifest and registry | design D5-D6, carve 7 | negative if angelus tables fold | manifest names modules |
| S8 run -> ops | design F | likely positive | none |
| S9 agent owns provider logs | design G | negative | none |
| S10 final deletions | design H, review 8, audit 13 | negative | converters gone |
| K1 client binary | carve 4 | near zero | none; packaging breaks |
| K2 split cli | carve 4 | must pay with dead code | none |
| K3 command sets per module | design D6 | small | none |
| X1 tools through api/sandbox | carve 5 | small negative | none |
| X2 sandbox identity, leased grants | captains-log 11-09 | design first | to design |

Line estimates come from the review and the audit; none are measured yet. Each lane measures its own and reports before/after benchmarks for any hot path (IR reads, form writes, rotation, fork reads, cold list).

What each step breaks, its gate and its issues: [steps.md](steps.md). The next one to raise is S1. Its only gate is `schema.json` on gluck, spain and cheroot reporting layout 4.

## 4. Decisions for you

The full text is in [decisions.md](decisions.md). These five change the plan most:

- **D9, retention vs fork-anywhere.** Design C trims patches below each checkpoint. Your 11-04 rule keeps StateAt answerable at every version, so forks can cut anywhere. Recommendation: S5 keeps every patch and only moves the fold off the append path. Trimming waits for a gc design.
- **D11, the net-negative rule.** Design C, E and F add new types. C and E can pay for themselves with deletions. F (`run -> ops` plus a bash harness) probably cannot: allow it, or ship the harness as its own module.
- **D10, layout changes.** S5, S7a and S7b each change the layout. Recommendation: one converter per step, each deleted one release later (S10), rather than one combined bump.
- **D1 and D5, how the store splits.** The design cuts by stack layer; the review (cut 2) and carve step 6 cut by concern. Recommendation: by layer. Carve 6 then dissolves into S3, S4 and S7.
- **D6 and D7, order.** The carve puts the client binary and the sandbox before the actor runtime, and the registry last. Recommendation: K and X run as parallel tracks; the registry arrives with the hierarchy (S7d), since hierarchy is the first optional module.

## 5. Issues

All 17 open issues are in [issues.md](issues.md) and on the diagrams (the issue buttons). In short:

- **Folds into a step:** #72 adopt (S7b), #60 self-spawn (K1), #38 RunAs (X1), #47 narrow door (S7d), #56 shell bindings (session primitives, D14), #74 study a bound form (S3).
- **Blocked by a step:** #57 tool identity (X2); #28 partly (the protected name needs the reserved block, X2).
- **Should land before a step:** #73 usage fields, before S9; #66 effort rows, before or inside S9.
- **Independent:** #70, #71, #68, #51, #45; each names the step where it meets the plan.
- **#39 blocks X2:** lineage grants on boxes mean nothing while a box calls back as the user.

## 6. Not verified

- The line estimates per step: they come from the review and the audit.
- Which of `meta_heal.go` and `schema.go` is migration-only.
- Whether every patch-range reader can take a diff of folded versions (it matters only if D9 goes the design's way).
- Layout 4 on spain and cheroot: S1's gate checks it.
- The mapping of a few small packages (relay, recent, keyconf, navstack, primitive) is a judgement; move them in `gen/model.py` and rerun.
