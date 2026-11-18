# Kingdom: status board

One Strategist plans both tracks and fans out forks that build parts. The Knight carries non-conflicting changes, produces the validated build and asks the Review Steward for a reviewer. Only the Engineer merges.

- Plans: [permissions.md](permissions.md) (Track P) and [estate.md](estate.md) (Track E). Deployment modes, revision 2: keel `proposal/deployment-modes` 480ce9e4a.
- Seats (roles; their names live in the charter, never in figaro Go): Figaro Strategist `@9bf485c2`, Figaro Knight `@d121c404`, Figaro Review Steward `@9e5bdab3`. Reviewers are turn-1 forks of the Strategist on Gemini, one per change.
- Validated builds: `/var/tmp/kingdom-builds/<part>`.

## The whole plan (14 items), board order

Working agreement: REVISION 2 of the plan root. The Strategist builds everything at once with its forks, keeps the final vision, and restacks it into non-conflicting commits for the Knight as main moves. Reviewers fork the Strategist at turn 1 (the common fork point) on Gemini.

| # | part | builder | branch | state (2026-11-18 02:xx) |
|---|---|---|---|---|
| 1 | IF1 study_incantation string | Knight | `lane/study-incantation-string` | in v0.74.3 (cutting) |
| 2 | P1 key registry | Knight | `lane/key-registry` | after IF1 |
| 2b | H1 place --under; succeed --successor drawn in place | fork oceanic-degree `a78f3129` | `lane/hierarchy-place` | review FIX (2 HIGH): back to the drawing board; rebuilt on P3 as v0.74.5 |
| 2d | F2 owner check ignores the figaro claim on the local door (pre-existing; cap bypass, role hijack) | Knight | off 0911f728f | building; v0.74.4 |
| 2c | P1b shape checks, figaro doctor keys | Knight | after P1 | queued, before P5 |
| 3 | E0 kingdom fan-out (charter `maxChildren`) | fork tavern-culprit `df4d5475` (drawn under the Engineer) | cheroot-config, off `proposal/system-charter-split` | finishing |
| 3b | R1 kingdom regiment | Knight | cheroot-config `lane/kingdom-regiment` e86f3dc (on 579193c) | SHIP (explode-nugget, no findings); owner lands eced4cd, ac170c8, e86f3dc, then flake.lock with f11866f + 579193c |
| 4 | P4 peer caller check | fork anagram-erratic `0e15cab0` | `lane/peer-caller-check` de5d64574 | fixes in (finite ttl from tools, succeed carries the seat ttl, peer-check.md); waits on v0.74.4 to restack, then review |
| 5 | E2 hush as a system service | fork erasable-unzip `56faa8d6`; drop-box refresh by the Strategist | hush `lane/hush-system-service` 70d3782 + `lane/hush-drop-refresh` | delta review; drop.sock now serves `POST /refresh/<name>` (status only) so rou2 can take `minter_control_socket` |
| 6 | P3 admission ledger | fork dominoes-elusive `439448d3` | `lane/admission-ledger` | building |
| 7 | E1 angelus socket activation | fork tavern-culprit `df4d5475` (Engineer slot) | `lane/socket-activation` | queued |
| 8 | P2 runtime.* migration | fork hydrant-rocket `78718b5c` | `lane/runtime-namespace` | building, stacked on P1; readers listed before landing |
| 9 | E3 figaro valet and the fence | tavern-culprit, after E1 | `lane/valet` | queued |
| 10 | P5 key-path grants, selectors, child_policy | hydrant-rocket, after P2 | `lane/key-grants` | queued |
| 11 | P6 quota, role verbs, succession charging | after P3, P5 | `lane/quota` | queued |
| 12 | E4 house mode module (staged) | after E1-E3 | `lane/house-mode` + cheroot-config | queued; deploys only after Gluck's Merlin cutover |
| 13 | P7 laws and reminder kinds | after P5 | `lane/laws` | queued |
| 14 | E8 the whole kingdom module | after P4-P6 | cheroot-config | queued |

## By morning (honest)

- Likely ready for the Engineer: IF1, P1, E0 (applies once `lane/managed-fanout` lands).
- Likely built, review may still be open: P4, E2, P3.
- If two more slots are granted: E1 built, P2 built and tested on a snapshot with its readers listed.
- Not by morning: P5, P6, P7, E3, E4, E8. Nothing is merged to main or deployed by the regiment; Merlin and spain are untouched.
