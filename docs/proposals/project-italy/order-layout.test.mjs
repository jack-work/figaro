import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { layoutOrder, mergeCommandSegments, uiHandoffRoute } from './order-layout.mjs';

const model = JSON.parse(readFileSync(new URL('./order-model.json', import.meta.url), 'utf8'));
const original = JSON.stringify(model);
const layout = layoutOrder(model);
assert.equal(model.roles.length, 11);
assert.equal(layout.nodes.length, 11);
assert.equal(layout.edges.length, 10);
assert.equal(model.roles.filter(r => r.manager === 'king').length, 8);
assert.deepEqual(model.roles.filter(r => r.manager === 'engineer').map(r => r.id).sort(), ['major', 'zanni']);
assert.deepEqual(layout, layoutOrder(model));
assert.equal(JSON.stringify(model), original);
for (const id of ['mamo', 'vigia', 'formista', 'franc', 'ebac', 'figscope']) assert.ok(!model.roles.some(r => r.id === id));
const boxes = new Map(layout.nodes.map(n => [n.id, n]));
function hits(a, b, n) {
  return a.x === b.x
    ? a.x > n.x && a.x < n.x + n.width && Math.max(a.y, b.y) > n.y && Math.min(a.y, b.y) < n.y + n.height
    : a.y > n.y && a.y < n.y + n.height && Math.max(a.x, b.x) > n.x && Math.min(a.x, b.x) < n.x + n.width;
}
for (const n of layout.nodes) {
  assert.ok(n.x >= 0 && n.x + n.width <= layout.width && n.y >= 0 && n.y + n.height <= layout.height);
  for (const b of layout.nodes) if (n.id < b.id) assert.ok(!(n.x < b.x + b.width && n.x + n.width > b.x && n.y < b.y + b.height && n.y + n.height > b.y));
}
for (const e of layout.edges) {
  assert.ok(boxes.get(e.from).y + boxes.get(e.from).height < boxes.get(e.to).y);
  for (let i = 1; i < e.points.length; i++) {
    const a = e.points[i - 1], b = e.points[i];
    assert.ok(a.x === b.x || a.y === b.y);
    assert.ok(b.y >= a.y);
    for (const n of layout.nodes) assert.ok(!hits(a, b, n), `Command ${e.from}/${e.to} enters ${n.id}`);
  }
}
for (let i = 0; i < layout.segments.length; i++) {
  const a = layout.segments[i];
  for (const b of layout.segments.slice(i + 1)) {
    const av = a.x1 === a.x2, bv = b.x1 === b.x2;
    if (av === bv) {
      const overlap = av ? a.x1 === b.x1 && Math.max(a.y1, b.y1) < Math.min(a.y2, b.y2) : a.y1 === b.y1 && Math.max(a.x1, b.x1) < Math.min(a.x2, b.x2);
      assert.ok(!overlap, 'A command segment was drawn twice');
    } else if (a.owner !== b.owner) {
      const h = av ? b : a, v = av ? a : b;
      assert.ok(!(v.x1 >= h.x1 && v.x1 <= h.x2 && h.y1 >= v.y1 && h.y1 <= v.y2), 'Different command branches cross');
    }
  }
}
for (const r of model.roles) {
  const path = uiHandoffRoute(model, layout, r.id);
  if (!r.uiTo) assert.equal(path, null);
  else {
    assert.equal(path.to, 'zanni');
    for (let i = 1; i < path.points.length; i++) for (const n of layout.nodes) assert.ok(!hits(path.points[i - 1], path.points[i], n), `UI path from ${r.id} enters ${n.id}`);
  }
}
assert.ok(layout.segments.length < layout.edges.reduce((sum, e) => sum + e.points.length - 1, 0));
assert.deepEqual(mergeCommandSegments([{ from: 'a', points: [{ x: 0, y: 0 }, { x: 0, y: 10 }] }, { from: 'a', points: [{ x: 0, y: 0 }, { x: 0, y: 20 }] }]), [{ owner: 'a', x1: 0, y1: 0, x2: 0, y2: 20 }]);
console.log('11 positions; no node overlap, duplicate command strokes, cross-owner wire intersections or paths through nodes; UI handoffs point to Zanni');
