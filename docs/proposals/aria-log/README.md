# Work order: aria logs, same-path authz, elicitations everywhere

Figaro Engineer, 2026-11-20. Skeleton, written before the tail of Gluck's 11-19 comment arrived; revise when it does.
Target: done by the weekend (11-22/23). Owner: Figaro Engineer; lanes minted by the King on KIC routes.
Source: Gluck's comment on Engineer koala-oomph (8b2ac262) turn 179; captains-log 2026/11/20/gluck-patronage-ingrain-aria-log.md.

## Goal

1. A figaro can edit its own bound form and reach the figaro hierarchy from inside its sandbox, authorized exactly as outside: one code path, no second implementation.
2. A figaro files its own elicitation on its own form; the approvals app renders and permissions an elicitation on any form, deep-linked; officers' elicitations climb to the King by patronage.
3. A new primitive, the aria log: a plain aria with a bound form that works as an append-only log of data, read and written through the figaro APIs; figaro is a superset of it.
4. Keel views aria logs. The captains-log moves onto an aria log. "Ingrain" re-reminds a figaro from it. Skills give way to the captains-log meanwhile.

## Order (each step ships alone)

| step | what | depends on | lane | size |
|---|---|---|---|---|
| 0 | Authz carve, first three steps of proposal/authz-middleware (d4576a91d): split authn into its own package; one Call hook for every RPC method with a declared action table (undeclared = denied, test-enforced); one Patch hook for every form write. Decision equivalence table over today's call sites: no decision changes. | none | A | 1 day |
| 1 | Same path from the sandbox: the box door carries every figaro verb (not a subset) into the same Call/Patch hooks, with the figaro's identity from the door claim. A sandboxed `figaro set`, `send`, `fork`, `state`, `ls` hit the same code as the CLI outside. Tests run every verb both ways and compare decisions. | 0 | A | 1 day |
| 2 | Self-elicitation: a figaro writes `elicit.<id>` on its own form (typed request: verb, target, values, reason, expiry). Decided requests leave a record (see open question 3). | 1 | B | half day |
| 3 | Approvals on any form: the API lists elicitations from any form the admin may see, deep-linked as `/f/<form>/e/<id>`; the classic bare path retires; the SPA follows (Zanni). Patronage: the King's role form keeps a list (`patronage.<officer>`) naming whose elicitations surface to him; a request climbs parent or patron until one with the authority can decide. | 2 | B + Zanni | 1 day |
| 4 | Aria log primitive: an aria with no provider whose turns are data entries; `send` appends (no model turn), `show`/`listen`/`read` read; the bound form holds the log's metadata and index. JSON-RPC methods generalized where needed (send without a turn, read by range). | 0 | C | 1-2 days |
| 5 | Keel viewer: keel renders an aria log (entries, form, links), read-only, over the figaro read API. | 4 | C | half day |
| 6 | Captains-log on an aria log: `cl` writes entries as aria-log appends; handles and citations live on the log's form; a one-time import of ~/.captains-log. Ingrain: a reminder kind that re-injects tagged entries into a figaro's context on a schedule or on event. | 4, 5 | D | 1 day |
| 7 | Skills give way: seats read the captains-log entries for their handles instead of skill files, where a skill is really memory; skills stay for procedures. | 6 | D | half day |

Lanes A-D run as A then B, with C in parallel from step 0, D after C. Gates as usual (build+vet per commit, -race at tip, nix). Opus review at steps 0-1 (authz) and 4 (store); Sonnet or Gemini elsewhere.

## Breaks to name

- Step 1: anything a box could not do before becomes possible if the same grants allow it outside; box grants must be audited before it ships.
- Step 3: the bare approvals path at / moves; links change.
- Step 6: `cl` storage changes; the git-backed instance becomes an import source.

## Open for Gluck

1. The tail of the 11-19 comment ("...keep the docum").
2. Aria log: one aria per log (captains-log = one aria) or one per subject (handle)?
3. Where a decided elicitation's record lives: the approvals audit log, or appended to an aria log (step 4 makes the latter natural).
4. Ingrain triggers: time, events (succession, a handle cited), or both.
