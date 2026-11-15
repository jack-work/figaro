export function mergeCommandSegments(edges) {
  const groups = new Map();
  for (const edge of edges) {
    for (let i = 1; i < edge.points.length; i++) {
      const a = edge.points[i - 1], b = edge.points[i];
      if (a.x === b.x && a.y === b.y) continue;
      if (a.x !== b.x && a.y !== b.y) throw new Error('Command segment is not orthogonal');
      const vertical = a.x === b.x;
      const fixed = vertical ? a.x : a.y;
      const start = Math.min(vertical ? a.y : a.x, vertical ? b.y : b.x);
      const end = Math.max(vertical ? a.y : a.x, vertical ? b.y : b.x);
      const key = `${edge.from}:${vertical ? 'v' : 'h'}:${fixed}`;
      if (!groups.has(key)) groups.set(key, { owner: edge.from, vertical, fixed, intervals: [] });
      groups.get(key).intervals.push([start, end]);
    }
  }
  const segments = [];
  for (const group of groups.values()) {
    group.intervals.sort((a, b) => a[0] - b[0]);
    const merged = [];
    for (const interval of group.intervals) {
      const last = merged.at(-1);
      if (last && interval[0] <= last[1]) last[1] = Math.max(last[1], interval[1]);
      else merged.push(interval.slice());
    }
    for (const [start, end] of merged) {
      segments.push({ owner: group.owner, x1: group.vertical ? group.fixed : start, y1: group.vertical ? start : group.fixed, x2: group.vertical ? group.fixed : end, y2: group.vertical ? end : group.fixed });
    }
  }
  return segments;
}

export function commandJunctions(segments) {
  const candidates = new Map();
  for (const a of segments) {
    for (const b of segments) {
      if (a.owner !== b.owner || a.x1 === a.x2 || b.y1 === b.y2) continue;
      const x = b.x1, y = a.y1;
      if (x < a.x1 || x > a.x2 || y < b.y1 || y > b.y2) continue;
      const key = `${a.owner}:${x}:${y}`;
      if (!candidates.has(key)) candidates.set(key, { owner: a.owner, x, y, directions: new Set() });
      const p = candidates.get(key);
      if (x > a.x1) p.directions.add('left');
      if (x < a.x2) p.directions.add('right');
      if (y > b.y1) p.directions.add('up');
      if (y < b.y2) p.directions.add('down');
    }
  }
  return [...candidates.values()].filter(p => p.directions.size > 2).map(({ owner, x, y }) => ({ owner, x, y }));
}

export function layoutOrder(model) {
  const roles = new Map(model.roles.map(r => [r.id, r]));
  if (roles.size !== model.roles.length) throw new Error('Duplicate standing position');
  const root = roles.get(model.root), team = roles.get(model.expandedTeam);
  if (!root || root.manager || !team || team.manager !== root.id) throw new Error('Invalid root or expanded team');
  for (const r of model.roles) {
    if (r.id !== root.id && r.manager !== root.id && r.manager !== team.id) throw new Error('Extend the prototype layout before adding another command tier');
  }
  const staff = model.roles.filter(r => r.manager === team.id);
  if (staff.length !== 2) throw new Error('The reviewed expanded team has two standing staff');
  const direct = model.roles.filter(r => r.manager === root.id && r.id !== team.id);
  const boxes = new Map();
  boxes.set(root.id, { id: root.id, x: 450, y: 32, width: 224, height: 104 });
  boxes.set(team.id, { id: team.id, x: 90, y: 224, width: 224, height: 118 });
  staff.forEach((r, i) => boxes.set(r.id, { id: r.id, x: 28 + i * 186, y: 420, width: 160, height: 118 }));
  direct.forEach((r, i) => boxes.set(r.id, { id: r.id, x: 440 + i % 3 * 230, y: 224 + Math.floor(i / 3) * 196, width: 200, height: 118, column: i % 3 }));
  const rootBox = boxes.get(root.id), teamBox = boxes.get(team.id);
  const center = b => b.x + b.width / 2;
  const edges = [{ from: root.id, to: team.id, points: [{ x: center(rootBox), y: rootBox.y + rootBox.height }, { x: center(rootBox), y: 178 }, { x: center(teamBox), y: 178 }, { x: center(teamBox), y: teamBox.y }] }];
  for (const r of direct) {
    const b = boxes.get(r.id), gutter = 420 + b.column * 230;
    edges.push({ from: root.id, to: r.id, points: [{ x: center(rootBox), y: rootBox.y + rootBox.height }, { x: center(rootBox), y: 178 }, { x: gutter, y: 178 }, { x: gutter, y: b.y - 24 }, { x: center(b), y: b.y - 24 }, { x: center(b), y: b.y }] });
  }
  for (const r of staff) {
    const b = boxes.get(r.id);
    edges.push({ from: team.id, to: r.id, points: [{ x: center(teamBox), y: teamBox.y + teamBox.height }, { x: center(teamBox), y: 374 }, { x: center(b), y: 374 }, { x: center(b), y: b.y }] });
  }
  if (boxes.size !== model.roles.length) throw new Error('A standing position has no layout');
  const segments = mergeCommandSegments(edges);
  return { width: 1124, height: Math.max(816, ...[...boxes.values()].map(b => b.y + b.height + 80)), nodes: [...boxes.values()], edges, segments, junctions: commandJunctions(segments), groups: [{ id: team.id, x: 16, y: 204, width: 370, height: 414 }], teamStaff: staff.map(r => r.id) };
}

export function uiHandoffRoute(model, layout, id) {
  const role = model.roles.find(r => r.id === id);
  if (!role?.uiTo || role.uiTo === id) return null;
  const boxes = new Map(layout.nodes.map(b => [b.id, b]));
  const source = boxes.get(id), target = boxes.get(role.uiTo);
  if (!source || !target) return null;
  const targetY = target.y + target.height / 2;
  let points;
  if (id === model.expandedTeam) points = [{ x: source.x + source.width, y: source.y + source.height / 2 }, { x: 398, y: source.y + source.height / 2 }, { x: 398, y: targetY }, { x: target.x + target.width, y: targetY }];
  else {
    const sx = source.x + source.width / 2, bottom = source.y + source.height;
    if (source.column !== undefined && source.y === 224) {
      const gutter = 426 + source.column * 230;
      points = [{ x: sx, y: bottom }, { x: sx, y: 382 }, { x: gutter, y: 382 }, { x: gutter, y: 554 }, { x: 398, y: 554 }];
    } else {
      const band = source.y === 616 ? bottom + 32 : 554;
      points = [{ x: sx, y: bottom }, { x: sx, y: band }, { x: 398, y: band }];
    }
    points.push({ x: 398, y: targetY }, { x: target.x + target.width, y: targetY });
  }
  return { from: id, to: role.uiTo, points };
}
