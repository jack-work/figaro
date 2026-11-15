# Width-bounded Sugiyama prototype

date: 2026-11-15
source: Gluck rejected the wide chart, then the single-column chart, and selected Sugiyama for the next prototype

## Current pattern

The organization chart now uses our own small layered layout, implemented as a pure function in `layered-layout.mjs`. It has no DOM, state-library or rendering dependency. The standalone HTML embeds the same function and adds an SVG renderer, focus state and the existing registry.

Balanced uses up to four columns. Portrait caps it at three; Narrow at two. The actual column count also respects the measured viewport. Node text remains 18 px at the default 100% zoom. The diagram receives full width, with selected-role details below rather than taking a permanent sidebar. Phones naturally fall back to one column.

At a 1440 px viewport, the approved-target core diagram is 1385 × 1362 px with 28 positions. The previous hanging version was 4278 px tall. This is still a scrollable chart, not a promise that 28 readable nodes fit into a small landscape window.

## Algorithm

1. Verify the command graph is a DAG. Reject a cycle rather than silently reversing an authority edge. Expected peer communication is a separate graph and does not change command rank.
2. Assign logical depth by longest path from the roots, preserving parent-before-child order. Bound each depth's physical rows by the available width and selected column cap. Wrapped rows do not create another supervisor.
3. Split edges that skip physical rows into virtual dummy chains for ordering. Run alternating barycentric sweeps, retaining the best crossing count. Stable tie-breaking makes the result deterministic.
4. Assign coordinates to a fixed, width-bounded column grid, with variable node heights and clear row gaps. Root is centered. Peripheral unplaced roles have a separate grid and no invented command edges.
5. Route orthogonal edges through clear between-column channels and row gaps. Different sources receive distinct routing tracks where their trunks would otherwise coincide. Arrows enter a child's top port and never point authority upward.

Crossing diagnostics describe the expanded layer-order problem, not a proof that no final orthogonal wires cross. This is a Sugiyama-style implementation with an explicit screen-width policy, not an ELK replacement or a globally optimal layout solver.

## The portable seam

```js
const result = layoutLayered({
  nodes: [{ id: "king", width: 260, height: 114 }],
  edges: []
}, {
  width: 1200,
  maxColumns: 4,
  nodeGap: 30,
  layerGap: 54
});
```

Output contains node positions, logical/physical ranks, orthogonal route points, bounds and layout diagnostics. It does not resolve roles, fetch metrics, change topology, authorize users or write state. Selection/highlighting is a renderer concern. A Zanni component can consume the output directly and bind the authorized runtime projection through figaro-forms.

After visual approval, the proposed integration is: move the pure function and tests into Zanni's visualization package, replace this page's node drawing with shared Zanni components, and supply the runtime role/topology projection. Keep reconnect/refusal semantics in figaro-forms, not the layout. No integration or production deployment is commissioned by this prototype.

## Checks

The module tests deterministic output, input preservation, known crossing reduction, empty input, invalid endpoints, cycle refusal and width limits. Twenty-four generated trees across four width budgets verify no node overlap, downward authority, orthogonal routes and no edge crossing a node's interior. Browser checks exercise the actual inventory at 320, 390, 768, 1024 and 1440 px, live/target, registry filtering, alias focus, peer flow and resizing. Local rendered chart was visually inspected, not a user's live browser or device.

## Online research

Brave searches and primary documentation:

- [ELK Layered](https://eclipse.dev/elk/reference/algorithms/org-eclipse-elk-layered.html): describes the Sugiyama framework, layer ordering, coordinate assignment and orthogonal routing. We use the framework, not its implementation.
- [Hierarchical Drawing Algorithms, Healy and Nikolov](https://cs.brown.edu/people/rtamassi/gdhandbook/chapters/hierarchical.pdf): the phases and virtual vertices for long edges.
- [ELK graph wrapping](https://eclipse.dev/elk/reference/options/org-eclipse-elk-layered-wrapping-strategy.html): illustrates why prescribed drawing areas need an explicit wrapping policy. Our within-depth row budget is its own policy, not an implementation of ELK's chunk wrapping.
- Earlier [draw.io Stack from Level](https://www.drawio.com/docs/manual/layouts/org-chart-layout/) and [OrgChart vertical/hybrid layouts](https://github.com/dabeng/OrgChart#readme) informed the hanging prototype. It solved width but became too tall, so it is superseded here.

No CDN, third-party layout dependency or framework replacement was introduced. Role names and identities are unchanged except the already-approved Quartermaster display name for the existing rou2 seat. Runtime hierarchy, permissions, timers and services remain untouched by this worker.
