# Vertical organization-chart layout

date: 2026-11-15
source: Gluck's feedback that the first chart was too horizontal to read

## Online research

Brave searches covered compact vertical org charts, hanging children, hybrid layouts and variable-sized tree layout. Primary documentation read:

- [draw.io org-chart layout](https://www.drawio.com/docs/manual/layouts/org-chart-layout/): its Stack from Level option stacks a node's children and their descendants in columns, expressly to stop broad org charts getting very wide.
- [OrgChart README](https://github.com/dabeng/OrgChart#readme): verticalLevel and hybrid layout align descendants vertically; compact layout is intended for dense branches. The documented benefit is keeping many nodes in a browser screen without forcing every sibling into one row.
- [d3-flextree README](https://github.com/Klortho/d3-flextree#readme): variable node sizes and a nonuniform tree layout can improve packing, but a top-down tree is not inherently narrow. A high-fan-out rank still needs an explicit stacking policy.

No external library, CDN, framework replacement or copied implementation was introduced. These are layout patterns, not a request to recreate another product's visual design.

## Applied decisions

1. Replace the sum-of-child-widths layout with preorder vertical stacking of whole subtrees. Depth adds a bounded indent, not another horizontal rank. All child nodes sit below their supervisor.
2. Compute node width from the actual viewport and maximum depth. Start at100% scale, with18px node titles. Default chart width equals its viewport at320,390,768and1440px; height grows instead of width.
3. Route each solid command arrow down a narrow left-side spine, then a short elbow into the top of its child. The spine stays outside descendant boxes. No command arrow travels upward.
4. Remove long exterior peer-wire tracks. Show selected-role communication as short dashed vertical message strips in the details panel: sender above receiver, purpose below. A reply keeps its true direction without routing a line back across the whole command tree. These are messages, not extra command positions.
5. Keep the live/approved-target split, exact registry identities, aliases and known placement drift. Nonselected roles remain readable rather than fading almost away. Focus highlights ancestors and incident command edges, and presents a vertical ancestor chain.
6. Retain optional zoom/pan, but do not require them to read the default. The panel moves below the chart on narrow screens. Resizing recomputes geometry instead of preserving a desktop-width canvas.

Quartermaster is Gluck's approved display name for the same rou2 role; the metadata snapshot keeps the old name as provenance. Proposal source also reflects the approved --managed option rather than --owned.

## Scope

This is a review-artifact UI change. Runtime hierarchy, permissions, timers and services are unchanged. The previously captured role inventory remains timestamped, not presented as a new live observation. The layout can be reused by the eventual order site, but this artifact does not deploy that renderer or prove its production authorization.
