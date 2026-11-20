# Work order: aria logs, same-path authz, elicitations everywhere, the captains-log as a memory graph

Figaro Engineer, 2026-11-20, revision 2 (Gluck's answers and the memory-graph vision folded in).
Target: done by the weekend (11-22/23). Owner: Figaro Engineer; lanes minted by the King on KIC routes; the captains-log migration is shared with the Backend Engineer's fan-out.
Sources: Gluck's comment on Engineer koala-oomph (8b2ac262) turn 179; captains-log 2026/11/20/gluck-patronage-ingrain-aria-log.md and 2026/11/19/captains-log-memory-graph-vision.md.

## Goal

1. A figaro edits its own bound form and reaches the figaro hierarchy from inside its sandbox, authorized exactly as outside: one code path, no second implementation.
2. A figaro files its own elicitation on its own form; the approvals app renders and permissions an elicitation on any form, deep-linked; officers' elicitations climb to the King by patronage. A decided elicitation is appended to an aria log.
3. A new primitive, the aria log: an aria that exists without a figaro (no provider, no model turns) whose turns are data entries, read and written through the figaro APIs; figaro is a superset of it.
4. The captains-log becomes ONE aria log whose bound form holds a graph of indexes; keel views it; garage holds the blobs; git audits the indexes. Figaros ramp from indexes, leave a trail of references, and a discovery pass turns the trail into heat that drives compaction. "Ingrain" re-reminds a figaro from it. Skills for tool how-to give way to it.

## Order (each step ships alone)

| step | what | depends on | lane | size |
|---|---|---|---|---|
| 0 | Authz carve (proposal/authz-middleware d4576a91d, first three steps): authn package; one Call hook with a declared action table (undeclared = denied, test-enforced); one Patch hook for every form write; a decision-equivalence table over today's call sites. No decision changes. | none | A (faucet-postcard, running) | 1 day |
| 1 | Same path from the sandbox: the box door carries every figaro verb into the same hooks, identity from the door claim; every verb tested inside and outside a box, decisions compared. | 0 | A | 1 day |
| 2 | Self-elicitation: a figaro writes `elicit.<id>` on its own form (typed: verb, target, values, reason, expiry). | 1 | B | half day |
| 3 | Approvals on any form, deep-linked `/f/<form>/e/<id>`; the bare path retires; the SPA follows (Zanni). Patronage: the King's role form keeps `patronage.<officer>`; a request climbs parent or patron until one with authority decides. A decided request is appended to the approvals aria log (the approvals audit file mirrors it until the log exists). | 2, 4 | B + Zanni | 1 day |
| 4 | Aria log primitive: an aria with no provider whose turns are data entries; `send` appends (no model turn); `show`/`listen`/`read` read, by range; the bound form holds metadata and indexes. JSON-RPC methods generalized where they assume a model turn. Same store path as aria turns. | 0 | C (wifeless-macaroni, running) | 1-2 days |
| 5 | Keel viewer: keel renders an aria log (entries, form, index graph) read-only over the figaro read API. | 4 | C | half day |
| 6 | Captains-log index form (schema below) on the one captains-log aria; protected `log.repo` and `log.ref`; index summaries and keel URIs; entry bodies as garage blobs. | 4 | Figaro lanes + Backend fan-out | 1 day |
| 7 | Import of ~/.captains-log: one pass, indexes first (from `cl` handles and INDEX.toml), entries as blobs addressed by keel URI, citations become index references. `cl` keeps working against the old tree until the cutover. | 6 | Backend fan-out | half day |
| 8 | Writing and proposing: a figaro appends a memory under an index and may rewrite that index's summary; a new note proposes an index; `figaro decide` (score below) accepts it at or above a tunable threshold, otherwise files it under the best other index; the writer may override, reviewed in retrospect. | 6 | Figaro lanes | 1 day |
| 9 | Discovery, heat, compaction, ingrain: a discovery pass over reads and references builds per-index heat and the reverse view; compaction keeps the often-searched, aggregates and storms, drops unactioned one-offs; a monthly meeting reviews next month's compaction list before it runs. Ingrain re-injects index summaries and pinned entries into a figaro's context on events (succession, an index cited, a fan-out child starting from an index) and on a slow schedule. | 7, 8 | Backend fan-out (discovery), Figaro lanes (ingrain) | 1-2 days |

Lanes A and C run now in parallel; B follows A; steps 6-9 start when step 4 lands ("an aria can exist without a figaro"). Gates as usual (build+vet per commit, -race at tip, nix). Opus review at steps 0-1 (authz) and 4 (store); Sonnet or Gemini elsewhere. Nothing merges before Gluck looks.

## Captains-log index form (proposal, step 6)

One aria, the captains-log. Its bound form:

| key | writer | shape |
|---|---|---|
| `log.repo`, `log.ref` | owner / charter (protected) | keel repo and branch or commit the URIs resolve against |
| `index.<handle>.title` | any figaro that may write the log | short title |
| `index.<handle>.summary` | same | the description blob's keel URI, plus a short inline summary (the blob is the full description, in garage) |
| `index.<handle>.entries` | append-only | list of `{uri, date, by}`: keel URIs of entry blobs |
| `index.<handle>.refs` | append-only | list of `{to: <handle or dated entry uri>, date, by}`: forward references only, no back-references stored |
| `index.<handle>.pinned` | owner / charter | entries that survive compaction regardless of heat |
| `heat.<handle>` | the discovery pass only (runtime) | `{reads_30d, cites_30d, last, storm}` |

Handles are the existing dotted `cl` handles (`figaro.form.durability`), so citations carry over unchanged.

## Proposing an index (step 8)

`figaro decide` scores a new note against candidate index summaries (the proposed one, and the top few by a cheap lexical prefilter) with one yes/no question per candidate: "does this note belong under <title>: <summary>?". It accepts the proposal when its score is at or above `log.decide.threshold` (default 0.7, tunable on the form, protected), otherwise files under the highest-scoring other candidate, or proposes a new index when none passes. Every automatic filing is appended with its scores; an override moves the entry and logs who and why.

## Breaks to name

- Step 1: anything a box could not do before becomes possible where the same grants allow it outside; box grants are audited before it ships.
- Step 3: the bare approvals path at / moves; links change.
- Step 7: `cl` storage changes; the git-backed tree becomes an import source and then the audit log.

## Meanwhile (no tooling needed)

Seats add a "See also" list of index handles and dated entry paths to the `cl` entries they write, so the reference trail starts now.

## Open

1. Heat counters on the form (runtime keys) or as entries in a separate metrics aria log: proposed on the form, small, runtime-written.
2. The decide score: yes/no per candidate (proposed) versus a single ranked choice.
3. The herald regiment (fan-out per conversation thread, Gluck controls when and to whom figaro responds) is the King's to charter; it reads the same log.
