import assert from 'node:assert/strict';
import { layoutLayered } from './layered-layout.mjs';

function validate(result, width) {
  const byId = new Map(result.nodes.map(n => [n.id, n]));
  for (const n of result.nodes) {
    assert.ok(n.x >= 0 && n.x + n.width <= width, `outside width: ${n.id}`);
    assert.ok(n.y >= 0 && n.y + n.height <= result.height);
    for (const b of result.nodes) {
      if (n.id >= b.id) continue;
      assert.ok(!(n.x < b.x + b.width && n.x + n.width > b.x && n.y < b.y + b.height && n.y + n.height > b.y), `overlap: ${n.id}/${b.id}`);
    }
  }
  for (const e of result.edges) {
    const a = byId.get(e.from), b = byId.get(e.to);
    assert.ok(a.rank < b.rank && a.y + a.height < b.y);
    assert.equal(e.points[0].y, a.y + a.height);
    assert.equal(e.points.at(-1).y, b.y);
    for (let i = 1; i < e.points.length; i++) {
      const p = e.points[i - 1], q = e.points[i];
      assert.ok(p.x === q.x || p.y === q.y, 'nonorthogonal segment');
      assert.ok(q.y >= p.y, 'upward authority edge');
      assert.ok(p.x >= 0 && p.x <= width && q.x >= 0 && q.x <= width);
      for (const n of result.nodes) {
        const hit = p.x === q.x
          ? p.x > n.x && p.x < n.x + n.width && Math.max(p.y, q.y) > n.y && Math.min(p.y, q.y) < n.y + n.height
          : p.y > n.y && p.y < n.y + n.height && Math.max(p.x, q.x) > n.x && Math.min(p.x, q.x) < n.x + n.width;
        assert.ok(!hit, `edge ${e.from}/${e.to} passes through ${n.id}`);
      }
    }
  }
  assert.ok(result.metrics.crossingsAfter <= result.metrics.crossingsBefore);
}

const crossing = { nodes: ['p', 'q', 'r', 's'].map(id => ({ id, width: 100, height: 60 })), edges: [{ from: 'p', to: 's' }, { from: 'q', to: 'r' }] };
const reduced = layoutLayered(crossing, { width: 700 });
assert.equal(reduced.metrics.crossingsBefore, 1);
assert.equal(reduced.metrics.crossingsAfter, 0);
assert.deepEqual(reduced, layoutLayered(crossing, { width: 700 }));
validate(reduced, 700);
assert.throws(() => layoutLayered({ nodes: [{ id: 'a', width: 100, height: 50 }, { id: 'b', width: 100, height: 50 }], edges: [{ from: 'a', to: 'b' }, { from: 'b', to: 'a' }] }), /cycle/);
assert.throws(() => layoutLayered(crossing, { width: 120 }), /Width budget/);
assert.throws(() => layoutLayered({ nodes: crossing.nodes, edges: [{ from: 'missing', to: 'p' }] }), /endpoint/);
assert.deepEqual(layoutLayered({ nodes: [], edges: [] }).nodes, []);
let seed = 713;
const random = () => ((seed = (seed * 1664525 + 1013904223) >>> 0) / 4294967296);
for (let run = 0; run < 24; run++) {
  const count = 12 + Math.floor(random() * 35);
  const graph = { nodes: Array.from({ length: count }, (_, i) => ({ id: `n${i}`, width: 180 + Math.floor(random() * 50), height: 65 + Math.floor(random() * 40) })), edges: [] };
  for (let i = 1; i < count; i++) graph.edges.push({ from: `n${Math.floor(random() * i)}`, to: `n${i}` });
  const original = JSON.stringify(graph);
  Object.freeze(graph.nodes);
  Object.freeze(graph.edges);
  for (const width of [320, 620, 900, 1200]) {
    const result = layoutLayered(graph, { width });
    validate(result, width);
    assert.equal(JSON.stringify(graph), original);
    assert.deepEqual(result, layoutLayered(graph, { width }));
    assert.ok(result.metrics.columns <= 4);
  }
}
console.log('layered layout: crossing reduction, deterministic output, width limits, DAG direction, node/edge clearance and input preservation passed');
