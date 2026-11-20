# Work order: aria logs, same-path authz, elicitations everywhere, the captains-log as a memory graph

Figaro Engineer, 2026-11-20, revision 3 (Gluck's answers, the memory-graph vision, the Strategist's composition read and the Review Steward's coherence note 2026/11/20/memory-vision-coherence.md folded in).
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
| 1 | One canonical endpoint set (one PR): every endpoint the daemon listens on (the owner's CLI socket, each sandbox box's socket, the node endpoint between machines) carries every figaro verb through the same Call and Patch hooks; endpoints differ only in how the caller is identified (peer uid, the box's figaro claim, the node certificate). A figaro calling `figaro send` from inside its own daemon takes the same path. Every verb tested through each endpoint, decisions compared. | 0 | A | 1 day |
| 1b | Rename campaign: "door" becomes "endpoint" everywhere: docs, code identifiers, CLI flags, config keys (one-release alias for config, doctor names the old key). Mechanical, after step 0 lands so it doesn't collide with lane A's files. | 0 | A or a short lane | half day |
| 2 | Self-elicitation: a figaro writes `elicit.<id>` on its own form (typed: verb, target, values, reason, expiry). | 1 | B | half day |
| 3 | Approvals on any form, deep-linked `/f/<form>/e/<id>`; the bare path retires; the SPA follows (Zanni). Patronage: the King's role form keeps `patronage.<officer>`; a request climbs parent or patron until one with authority decides. A decided request is appended to the approvals aria log (the approvals audit file mirrors it until the log exists). | 2, 4 | B + Zanni | 1 day |
| 4 | Aria log primitive: an aria with no provider whose turns are data entries; `send` appends (no model turn); `show`/`listen`/`read` read, by range; the bound form holds metadata and indexes. JSON-RPC methods generalized where they assume a model turn. Same store path as aria turns. | 0 | C (wifeless-macaroni, running) | 1-2 days |
| 5 | Keel viewer: keel renders an aria log (entries, form, index graph) read-only over the figaro read API. | 4 | C | half day |
| 6 | Captains-log index form (schema below) on the one captains-log aria; owner-held `log.repo`, `log.ref`, `handles`, threshold; index summary, blob, head, refs; the walk log for heat. | 4 | Figaro lanes + Backend fan-out | 1 day |
| 7 | Import of ~/.captains-log: one pass, indexes first (from `cl` handles and INDEX.toml), entries as blobs addressed by keel URI, citations become index references. `cl` keeps working against the old tree until the cutover. | 6 | Backend fan-out | half day |
| 8 | Writing and proposing: a figaro appends a memory under an index and may rewrite that index's summary; a new note proposes an index; `figaro decide` (score below) accepts it at or above a tunable threshold, otherwise files it under the best other index; the writer may override, reviewed in retrospect. | 6 | Figaro lanes | 1 day |
| 9 | Discovery, heat, compaction, ingrain: a discovery pass over reads and references builds per-index heat and the reverse view; compaction keeps the often-searched, aggregates and storms, drops unactioned one-offs; a monthly meeting reviews next month's compaction list before it runs. Ingrain re-injects index summaries and pinned entries into a figaro's context on events (succession, an index cited, a fan-out child starting from an index) and on a slow schedule. | 7, 8 | Backend fan-out (discovery), Figaro lanes (ingrain) | 1-2 days |

Lanes A and C run now in parallel; B follows A; steps 6-9 start when step 4 lands ("an aria can exist without a figaro"). Gates as usual (build+vet per commit, -race at tip, nix). Opus review at steps 0-1 (authz) and 4 (store); Sonnet or Gemini elsewhere. Nothing merges before Gluck looks.

## Captains-log index form (step 6)

One aria, the captains-log, `system.kind = "log"` (a kind on the form, minted through form.bind; no new RPC method). Entries are the log's turns; a citation is `<log>:<turn>[.<node>]`. Keel URIs are derived by the viewer (step 5) and never stored, so moving keel or the repo moves no index. (Gluck's rule 7 said "indexes point at keel URIs": they do, through the viewer. Flagged for his OK.)

| key | writer | shape |
|---|---|---|
| `log.repo`, `log.ref`, `handles`, `index.decide.threshold` | owner (registry: owner-held, judged by the single Patch hook) | repo and ref for derived URIs; the handle allowlist; the filing threshold (default 0.7) |
| `index.<handle>.summary` | figaros with send on the log | short summary text |
| `index.<handle>.blob` | same | garage URI of the full description |
| `index.<handle>.head` | same, monotonic | the highest cited turn: the ONE value a figaro studies |
| `index.<handle>.refs.<n>` | append-only, one key per ref | `{to: <handle> or <log>:<turn>, date, by}`; forward references only |
| `runtime.heat.<handle>` | a daemon module only | `{reads_30d, cites_30d, last, storm}`, rolled up from a separate walk log (an aria log of read events) on the analysis schedule |

Entries never live in one growing list key: a studied form is mirrored whole into context. Studies key on `.head` and read entries by range.

## Filing a note (step 8)

`figaro decide` stays read-only. The filer asks `figaro decide -C @<log>#index` (choices from another form's subtree; needs read on the log) whether the note belongs under the proposed index; at or above `index.decide.threshold` it files there, otherwise under the best-scoring other index, otherwise it proposes a new one. The filer then appends the entry and patches the index itself. The decide outcome and any later override are appended as one entry, which is what "override reviewed in retrospect" reviews. Lane A adds decide rows to its equivalence table before step 1.

## Rulings that bind the lanes (rev 3)

1. Node kind: the log kind is named, and every switch over node kind (NodeLink, IsStump, MatchID, chargesQuota, S1 normalize plan, P3 fan-out cache) has a log branch, listed in lane C's design note. S1 never auto-detaches a log unless it is forked; log appends do not count toward S1's quiet trigger.
2. Fan-out: minting a log charges the minter's max_children and quota; the owner minting the captains-log is uncharged as usual.
3. Log rules (kind fixed at birth, no provider or model on a log) are judgeWrite rules ordered BEFORE the owner shortcut. Lane C rebases on lane A and adds log rows to the equivalence golden.
4. Access: roster kinds grant `read` and `send` on the captains-log through child_policy (kingdom charter data, El Mayor). Listed in lane A's step-1 breakage audit.
5. Context safety: anything a log feeds into a figaro's context (show, listen, ingrain) is fenced or escaped like #143, so log text cannot forge a system reminder or a law. Ingrain is delivered as the `ingrain` reminder kind (proposal/reminder-kinds), so laws can shape it.
6. Elicitations: `elicit.*` is dropped at birth (a Birth rule) before step 2 ships, so forks don't copy requests. A request's `state` field is display only; the decision is only the owner's patch (approvals already ignores it). One deep link: `/f/<form>/e/<id>`, with `/r/<form>/<id>` redirecting for one release.
7. Patronage: the officer writes `patron = <role>` on its own form; the King's list is the reverse view computed by the approvals service; no back-reference stored.
8. Git: import once (step 7), then git audits index snapshots exported from the log form's versions; `cl snapshot` is repointed, not retired. The import keeps every `cl` handle and kind (`spain.seats.*` included, so warm handoffs survive) and runs on a store snapshot, with fsync per append measured.
9. `figaro status -j` prints `"kind": "log"` for a log; its turn count is its entry count.

## Breaks to name

- Step 1: anything a box could not do before becomes possible where the same grants allow it outside; box grants are audited before it ships.
- Step 3: the bare approvals path at / moves; links change.
- Step 7: `cl` storage changes; the git-backed tree becomes an import source and then the audit log.

## Meanwhile (no tooling needed)

Seats add a "See also" list of index handles and dated entry paths to the `cl` entries they write, so the reference trail starts now.

## Open

1. Gluck: OK that keel URIs are derived by the viewer rather than stored (ruling: store `<log>:<turn>`)?
2. The decide score: yes/no per candidate (proposed) versus a single ranked choice.
3. The herald regiment (fan-out per conversation thread, Gluck controls when and to whom figaro responds) is the King's to charter; it reads the same log.
