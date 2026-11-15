export function layoutLayered(graph, options = {}) {
  const padding = options.padding ?? 24;
  const gap = options.nodeGap ?? 30;
  const layerGap = options.layerGap ?? 54;
  const width = options.width ?? 1200;
  const maxColumns = options.maxColumns ?? 4;
  const sweeps = options.sweeps ?? 8;
  if (layerGap < 32) throw new Error('Layer gap must leave room for routing tracks');
  const nodes = graph.nodes.map((n, order) => ({ ...n, order: n.order ?? order }));
  const byId = new Map(nodes.map(n => [n.id, n]));
  if (byId.size !== nodes.length) throw new Error('Duplicate layout node');
  if (!nodes.length) return { nodes: [], edges: [], width, height: 0, layers: [], metrics: { crossingsBefore: 0, crossingsAfter: 0, dummyCount: 0, columns: 0 } };
  for (const n of nodes) {
    if (!(n.width > 0 && n.height > 0)) throw new Error('Positive node dimensions required');
    n.parents = [];
    n.children = [];
    n.depth = 0;
  }
  const edges = graph.edges.map((e, i) => ({ ...e, id: e.id ?? `edge-${i}` }));
  for (const e of edges) {
    if (!byId.has(e.from) || !byId.has(e.to)) throw new Error('Unknown layout endpoint');
    byId.get(e.from).children.push(e.to);
    byId.get(e.to).parents.push(e.from);
  }
  const compare = (a, b) => byId.get(a).order - byId.get(b).order || a.localeCompare(b);
  const indegree = new Map(nodes.map(n => [n.id, n.parents.length]));
  const ready = nodes.filter(n => !n.parents.length).map(n => n.id).sort(compare);
  const topology = [];
  while (ready.length) {
    const id = ready.shift();
    topology.push(id);
    for (const child of byId.get(id).children) {
      const n = byId.get(child);
      n.depth = Math.max(n.depth, byId.get(id).depth + 1);
      indegree.set(child, indegree.get(child) - 1);
      if (!indegree.get(child)) {
        ready.push(child);
        ready.sort(compare);
      }
    }
  }
  if (topology.length !== nodes.length) throw new Error('Command graph contains a cycle; authority edges are not reversed');
  const cellWidth = Math.max(...nodes.map(n => n.width));
  const columns = Math.max(1, Math.min(maxColumns, Math.floor((width - padding * 2 + gap) / (cellWidth + gap))));
  if (cellWidth + padding * 2 > width) throw new Error('Width budget cannot fit a node');
  const depthLayers = [];
  for (const id of topology) {
    const n = byId.get(id);
    (depthLayers[n.depth] ??= []).push(id);
  }
  const layers = [];
  for (const layer of depthLayers) {
    layer.sort(compare);
    for (let i = 0; i < layer.length; i += columns) layers.push(layer.slice(i, i + columns));
  }
  layers.forEach((layer, rank) => layer.forEach(id => { byId.get(id).rank = rank; }));
  const expanded = layers.map(layer => layer.slice());
  const incoming = new Map(), outgoing = new Map();
  const segments = [];
  let dummyCount = 0;
  function connect(from, to) {
    (incoming.get(to) ?? incoming.set(to, []).get(to)).push(from);
    (outgoing.get(from) ?? outgoing.set(from, []).get(from)).push(to);
    segments.push({ from, to });
  }
  for (const [i, e] of edges.entries()) {
    const a = byId.get(e.from), b = byId.get(e.to);
    let previous = a.id;
    for (let rank = a.rank + 1; rank < b.rank; rank++) {
      const id = `\u0000dummy-${i}-${rank}`;
      expanded[rank].push(id);
      connect(previous, id);
      previous = id;
      dummyCount++;
    }
    connect(previous, b.id);
  }
  const rankOf = new Map();
  expanded.forEach((layer, rank) => layer.forEach(id => rankOf.set(id, rank)));
  function positions(order) {
    const pos = new Map();
    order.forEach(layer => layer.forEach((id, i) => pos.set(id, i)));
    return pos;
  }
  function crossingCount(order) {
    const pos = positions(order);
    let count = 0;
    for (let rank = 0; rank + 1 < order.length; rank++) {
      const links = segments.filter(e => rankOf.get(e.from) === rank);
      for (let i = 0; i < links.length; i++) {
        for (let j = i + 1; j < links.length; j++) {
          const a = links[i], b = links[j];
          if ((pos.get(a.from) - pos.get(b.from)) * (pos.get(a.to) - pos.get(b.to)) < 0) count++;
        }
      }
    }
    return count;
  }
  const crossingsBefore = crossingCount(expanded);
  let best = expanded.map(a => a.slice()), bestScore = crossingsBefore;
  for (let sweep = 0; sweep < sweeps; sweep++) {
    const down = sweep % 2 === 0;
    const start = down ? 1 : expanded.length - 2;
    const end = down ? expanded.length : -1;
    for (let rank = start; rank !== end; rank += down ? 1 : -1) {
      const pos = positions(expanded);
      const old = new Map(expanded[rank].map((id, i) => [id, i]));
      const scores = new Map(expanded[rank].map(id => {
        const adjacent = (down ? incoming : outgoing).get(id) ?? [];
        return [id, adjacent.length ? adjacent.reduce((sum, p) => sum + pos.get(p), 0) / adjacent.length : old.get(id)];
      }));
      expanded[rank].sort((a, b) => scores.get(a) - scores.get(b) || old.get(a) - old.get(b));
    }
    const score = crossingCount(expanded);
    if (score < bestScore) {
      best = expanded.map(a => a.slice());
      bestScore = score;
    }
  }
  const ordered = best.map(layer => layer.filter(id => byId.has(id)));
  const contentWidth = columns * cellWidth + Math.max(0, columns - 1) * gap;
  const origin = (width - contentWidth) / 2;
  const rowTop = [], rowBottom = [];
  let y = padding;
  for (const [rank, layer] of ordered.entries()) {
    rowTop[rank] = y;
    const height = Math.max(...layer.map(id => byId.get(id).height));
    const start = Math.floor((columns - layer.length) / 2);
    for (const [i, id] of layer.entries()) {
      const n = byId.get(id);
      n.column = start + i;
      n.x = origin + n.column * (cellWidth + gap) + (cellWidth - n.width) / 2;
      n.y = y;
      if (rank === 0 && layer.length === 1) n.x = (width - n.width) / 2;
    }
    rowBottom[rank] = y + height;
    y += height + layerGap;
  }
  const channels = Array.from({ length: columns + 1 }, (_, i) => origin - gap / 2 + i * (cellWidth + gap));
  const trunks = new Map();
  for (const n of nodes) {
    if (!n.children.length) continue;
    const sx = n.x + n.width / 2;
    const targets = n.children.map(id => byId.get(id).x + byId.get(id).width / 2);
    const score = x => Math.abs(sx - x) + targets.reduce((sum, tx) => sum + Math.abs(tx - x), 0);
    trunks.set(n.id, channels.reduce((best, x) => score(x) < score(best) ? x : best, channels[0]));
  }
  const trunkGroups = new Map();
  for (const [id, x] of trunks) (trunkGroups.get(x) ?? trunkGroups.set(x, []).get(x)).push(id);
  for (const [x, ids] of trunkGroups) {
    ids.sort(compare);
    const step = ids.length > 1 ? Math.min(6, 20 / (ids.length - 1)) : 0;
    ids.forEach((id, i) => trunks.set(id, x + (i - (ids.length - 1) / 2) * step));
  }
  const sourceRows = new Map(), targetRows = new Map();
  for (const e of edges) {
    const a = byId.get(e.from), b = byId.get(e.to);
    (sourceRows.get(a.rank) ?? sourceRows.set(a.rank, new Set()).get(a.rank)).add(a.id);
    (targetRows.get(b.rank) ?? targetRows.set(b.rank, new Set()).get(b.rank)).add(a.id);
  }
  function trackOffset(rows, rank, id) {
    const ids = [...rows.get(rank)].sort(compare);
    return 12 + (ids.length > 1 ? ids.indexOf(id) * 10 / (ids.length - 1) : 0);
  }
  function simplify(points) {
    const out = [];
    for (const p of points) {
      if (out.length && out.at(-1).x === p.x && out.at(-1).y === p.y) continue;
      if (out.length > 1) {
        const a = out.at(-2), b = out.at(-1);
        if ((a.x === b.x && b.x === p.x) || (a.y === b.y && b.y === p.y)) out.pop();
      }
      out.push(p);
    }
    return out;
  }
  const routed = edges.map(e => {
    const a = byId.get(e.from), b = byId.get(e.to);
    const sx = a.x + a.width / 2, tx = b.x + b.width / 2;
    const exit = rowBottom[a.rank] + trackOffset(sourceRows, a.rank, a.id);
    const entry = rowTop[b.rank] - trackOffset(targetRows, b.rank, a.id);
    const channel = trunks.get(a.id);
    const points = b.rank === a.rank + 1
      ? [{ x: sx, y: a.y + a.height }, { x: sx, y: exit }, { x: tx, y: exit }, { x: tx, y: b.y }]
      : [{ x: sx, y: a.y + a.height }, { x: sx, y: exit }, { x: channel, y: exit }, { x: channel, y: entry }, { x: tx, y: entry }, { x: tx, y: b.y }];
    return { ...e, points: simplify(points), skippedLayers: b.rank - a.rank - 1 };
  });
  return {
    nodes: nodes.map(n => ({ id: n.id, x: n.x, y: n.y, width: n.width, height: n.height, rank: n.rank, depth: n.depth, column: n.column })),
    edges: routed,
    layers: ordered,
    width,
    height: y - layerGap + padding,
    metrics: { crossingsBefore, crossingsAfter: bestScore, dummyCount, columns, physicalLayers: ordered.length, logicalLayers: depthLayers.length }
  };
}
