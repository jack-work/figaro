# Lean order workbench: complete UI rewrite

date: 2026-11-15
source: Gluck rejected the previous prototype and supplied a smaller standing roster

## The job

The page answers two questions: who owns this work, and what is temporary? It no longer renders the daemon's historical roster as the command structure. The target contains eleven positions: King; eight direct officers; Major and Zanni / Topo under Engineer. No Mamo, standing Vigía, library officer, ebac steward, figscope officer or permanent TV implementation team is added.

All UI goes through Zanni. Figaro Engineer owns figaro, terminal pager, ebac, forms and figscope through transient work. Taller owns Keel CI and API/CLI/review capabilities, not Keel UI. Zapador owns health, thermals and alerting through transient work. Big TV delegates backends and infrastructure and requests UI from Zanni. Escribano remains the publishing officer.

The runtime target is not claimed applied. King owns accepted handoffs and retirement; the worker only publishes this review artifact. Parent confirmed Mamo was already retired and asked other owners for consolidation handoffs. Active migration work remains separate.

## Visual system

- Canvas: cool mist `#eef2f8`, with a quiet graph grid.
- Surface: white `#ffffff`.
- Ink: slate `#1d2c3b`.
- Secondary text: `#65778b`.
- Focus and engineering: cobalt `#3165d8`.
- Transient work: jade `#187e75`; optional UI handoff uses violet, never another command color.
- Typography: self-contained Commissioner variable font, licensed under OFL, with a small monospace utility layer. No CDN or runtime font request.

The signature is the explicit Engineer compound containing exactly two standing staff. Other implementation demand is a small transient-work annotation on the accountable officer, not another box. Command structure, ownership inspection and worker lifecycle are separate views of the same eleven-position fixture.

## Lines are structure, not decoration

`order-layout.mjs` is a new purpose-built compound layout for the lean target. Command routes share a bus only when they have the same supervisor. Collinear shared segments are unioned before rendering, so they are drawn once. Different supervisors' command lines do not cross, and no route enters a node's interior. Branch junction dots mean a real shared supervisor, not a crossing.

Peer communication is not dumped over the whole graph. Selecting a domain officer can reveal one explicit UI handoff to Zanni, with a small crossing bridge where necessary. Clear it to return to command-only view. The inspector states reports-to, owns, scope boundary and expected transient tasks.

This is not another claim that a generic Sugiyama solver can fix an oversized roster. The earlier layered engine remains historical source only. The new pattern deliberately supports one expanded team with two staff; extending the formation requires a reviewed layout change rather than silently admitting every historical role.

## Source and rebuilding

- `order-model.json`: review-only target fixture and ownership text. Not a runtime registry or flake manifest.
- `order-layout.mjs`: pure positions, routes, merged command segments and UI handoff geometry.
- `organization-chart.template.html`: new workbench interface, interactions, styling and renderer.
- `assets/commissioner.woff2`, `assets/commissioner-OFL.txt`: bundled font and license from Google Fonts' Commissioner directory.
- `build-order.mjs`: deterministic self-contained artifact generation using Node standard libraries.
- `organization-chart.html`: Keel review artifact, with data, layout and font embedded.

Rebuild with `node docs/proposals/project-italy/build-order.mjs`. Run the geometry checks with `node docs/proposals/project-italy/order-layout.test.mjs`.

The three tabs work: Command map, Who owns what, Fan-out rules. Search finds accountable owners, including distinguishing terminal pager from health alerting and CI from incidental text. On phones, the readable ownership list opens first; the graph remains available with pan and zoom. Keyboard selection, inspector close, UI handoff, fit and zoom are implemented. There is no create, launch, cast, retire or model-inference action.

## Handoff, not role multiplication

Transient owners manually refork and carry context near 60%, case by case. Preserve decisions, current evidence and open obligations, then retire the old worker after accepted handoff with a short TTL, normally ten minutes. These tasks do not use standing-role succession. The view annotates expected workstreams; it does not invent live workers or a queue count.

After visual approval, Zanni / Topo should receive this UI work. Replace the target fixture with the authorized runtime role/topology projection, compose the node and inspector primitives into Zanni, and preserve the no-duplicate-owner and manual-worker-handoff rules. Keep authorization and reconnect freshness in the state client. Do not run this one-off review worker alongside a new permanent UI owner.

## Verification

Geometry checks cover the eleven-position roster, exact eight direct reports, two Engineer staff, no node overlap, no duplicate command strokes, no cross-supervisor command intersections and no command/UI route through a role node. Browser checks at 320, 390, 768, 1024 and 1440 px exercise all tabs, owner lookup, real keyboard Enter, inspector, UI handoff and zoom. No page overflow, clipped node content, console errors or external HTTP requests. Local desktop, inspector and phone renders were inspected; no user browser or device was touched.

No runtime roles, timers, permissions, daemon, service, Nix switch or target-cache build changed. Earlier research sources remain relevant to general layout, but the current design follows the smaller ownership structure and the frontend-design rewrite, not a third patch to the old page.
