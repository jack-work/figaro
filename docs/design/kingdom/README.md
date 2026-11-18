# Kingdom: status board

One Strategist plans both tracks and fans out forks that build parts. The Knight carries non-conflicting changes, produces the validated build and asks the Review Steward for a reviewer. Only the Engineer merges.

- Plans: [permissions.md](permissions.md) (Track P) and [estate.md](estate.md) (Track E). Deployment modes, revision 2: keel `proposal/deployment-modes` 480ce9e4a.
- Seats (roles; their names live in the charter, never in figaro Go): Figaro Strategist `@9bf485c2`, Figaro Knight `@d121c404`, Figaro Review Steward `@9e5bdab3`. Reviewers are turn-1 forks of the Strategist on Gemini, one per change.
- Validated builds: `/var/tmp/kingdom-builds/<part>`.

## In flight (2026-11-18 03:4x)

| part | who | branch | state |
|---|---|---|---|
| IF1 study_incantation string | Knight | `lane/study-incantation-string` | building |
| P1 key registry | Knight | `lane/key-registry` | after IF1 |
| P4 peer caller check | fork anagram-erratic `0e15cab0` | `lane/peer-caller-check` | building |
| E0 kingdom fan-out (charter `maxChildren`) | fork tavern-culprit `df4d5475` | cheroot-config, off `proposal/system-charter-split` | finishing |
| E2 hush as a system service | fork erasable-unzip `56faa8d6` | hush `lane/hush-system-service` | building |

## Next

P3 (admission ledger) when a fork slot frees, then E1 (socket activation), E3 (valet), P2 (runtime.*, readers listed first), P5, P6, E4 (house mode, staged; deploys only after Gluck's Merlin cutover), P7, E8 (the whole kingdom module).
