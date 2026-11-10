from collections import defaultdict

NODE_H = 46
RANK_GAP = 74
NODE_GAP = 28
DUMMY_W = 8
CHAR_W = 7.4
MARGIN = 24


def node_width(label, sub):
    return max(150, int(max(len(label) * CHAR_W + 34, len(sub) * 6.6 + 24)))


def local_ranks(ids, links):
    adj = defaultdict(list)
    for a, b in links:
        adj[a].append(b)
    state, back = {}, set()

    def dfs(n):
        state[n] = 1
        for m in adj[n]:
            if state.get(m) == 1:
                back.add((n, m))
            elif m not in state:
                dfs(m)
        state[n] = 2

    for n in ids:
        if n not in state:
            dfs(n)
    dag = [(a, b) for a, b in links if (a, b) not in back]
    rank = {n: 0 for n in ids}
    for _ in range(len(ids)):
        changed = False
        for a, b in dag:
            if rank[b] < rank[a] + 1:
                rank[b] = rank[a] + 1
                changed = True
        if not changed:
            break
    return rank


def layout(nodes, edges):
    band_of = {n["id"]: n["band"] for n in nodes}
    bands = sorted({n["band"] for n in nodes})
    rank = {}
    offset = 0
    for band in bands:
        ids = [n["id"] for n in nodes if n["band"] == band]
        links = [(e["from"], e["to"]) for e in edges if band_of.get(e["from"]) == band and band_of.get(e["to"]) == band and e["from"] != e["to"]]
        lr = local_ranks(ids, links)
        for i in ids:
            rank[i] = offset + lr[i]
        offset += max(lr.values()) + 1
    nrank = offset
    width = {n["id"]: node_width(n["label"], n["sub"]) for n in nodes}
    layers = [[] for _ in range(nrank)]
    for n in nodes:
        layers[rank[n["id"]]].append(n["id"])
    chains = []
    seen = set()
    for e in edges:
        a, b = e["from"], e["to"]
        if a == b or (a, b) in seen:
            continue
        seen.add((a, b))
        ra, rb = rank[a], rank[b]
        if ra == rb:
            chains.append({"key": (a, b), "pts": [a, b]})
            continue
        step = 1 if rb > ra else -1
        pts = [a]
        for r in range(ra + step, rb, step):
            d = f"d:{'dn' if step > 0 else 'up'}:{b}:{r}"
            if d not in rank:
                rank[d] = r
                width[d] = DUMMY_W
                layers[r].append(d)
            pts.append(d)
        pts.append(b)
        chains.append({"key": (a, b), "pts": pts})
    up, down = defaultdict(list), defaultdict(list)
    for c in chains:
        p = c["pts"]
        for x, y in zip(p, p[1:]):
            hi, lo = (x, y) if rank[x] < rank[y] else (y, x)
            if rank[x] != rank[y] and lo not in down[hi]:
                down[hi].append(lo)
                up[lo].append(hi)
    pos = {}
    for layer in layers:
        for i, n in enumerate(layer):
            pos[n] = i

    def crossings():
        total = 0
        for r in range(nrank - 1):
            segs = [(pos[a], pos[b]) for a in layers[r] for b in down[a]]
            for i in range(len(segs)):
                for j in range(i + 1, len(segs)):
                    if (segs[i][0] - segs[j][0]) * (segs[i][1] - segs[j][1]) < 0:
                        total += 1
        return total

    def sweep(order, nbrs):
        for r in order:
            def bc(n):
                ns = nbrs[n]
                return sum(pos[m] for m in ns) / len(ns) if ns else pos[n]
            layers[r].sort(key=lambda n: (bc(n), pos[n]))
            for i, n in enumerate(layers[r]):
                pos[n] = i

    best = crossings()
    best_layers = [list(l) for l in layers]
    for it in range(24):
        if it % 2 == 0:
            sweep(range(1, nrank), up)
        else:
            sweep(range(nrank - 2, -1, -1), down)
        c = crossings()
        if c < best:
            best, best_layers = c, [list(l) for l in layers]
    layers[:] = best_layers
    for layer in layers:
        for i, n in enumerate(layer):
            pos[n] = i

    for r, layer in enumerate(layers):
        layers[r] = [n for n in layer if up[n] or down[n]] + [n for n in layer if not up[n] and not down[n]]
    x = {}
    for layer in layers:
        cur = 0
        for n in layer:
            x[n] = cur + width[n] / 2
            cur += width[n] + NODE_GAP

    def is_dummy(n):
        return str(n).startswith("d:")

    def gap(a, b):
        da, db = is_dummy(a), is_dummy(b)
        g = 8 if da and db else 16 if da or db else NODE_GAP
        return (width[a] + width[b]) / 2 + g

    def weight(a, b):
        return 8 if is_dummy(a) and is_dummy(b) else 2 if is_dummy(a) or is_dummy(b) else 1

    def place(layer, want, wts):
        off, cur = [], 0.0
        for i, n in enumerate(layer):
            if i:
                cur += gap(layer[i - 1], n)
            off.append(cur)
        blocks = []
        for i, n in enumerate(layer):
            blocks.append([want[n] - off[i], wts[n], 1])
            while len(blocks) > 1 and blocks[-2][0] > blocks[-1][0]:
                v2, w2, c2 = blocks.pop()
                v1, w1, c1 = blocks.pop()
                blocks.append([(v1 * w1 + v2 * w2) / (w1 + w2), w1 + w2, c1 + c2])
        i = 0
        for v, _, c in blocks:
            for _ in range(c):
                x[layer[i]] = v + off[i]
                i += 1

    for it in range(80):
        if it % 3 == 0:
            rs, nbr = range(1, nrank), [up]
        elif it % 3 == 1:
            rs, nbr = range(nrank - 2, -1, -1), [down]
        else:
            rs, nbr = range(nrank), [up, down]
        for r in rs:
            layer = layers[r]
            want, wts = {}, {}
            for n in layer:
                tw, tx = 0.0, 0.0
                for nb in nbr:
                    for m in nb[n]:
                        w = weight(n, m)
                        tw += w
                        tx += w * x[m]
                lonely = not up[n] and not down[n] and layer[0] != n
                want[n] = tx / tw if tw else (-1e6 if lonely else x[n])
                wts[n] = tw if tw else (1e-6 if lonely else 0.5)
            place(layer, want, wts)
    lo = min(x[n] - width[n] / 2 for n in x)
    for n in x:
        x[n] += MARGIN - lo
    total_w = max(x[n] + width[n] / 2 for n in x) + MARGIN
    y = {n: MARGIN + rank[n] * (NODE_H + RANK_GAP) for n in rank}
    total_h = MARGIN * 2 + nrank * NODE_H + (nrank - 1) * RANK_GAP
    boxes = {n["id"]: {"x": x[n["id"]] - width[n["id"]] / 2, "y": y[n["id"]], "w": width[n["id"]], "h": NODE_H, "rank": rank[n["id"]]} for n in nodes}

    ports = defaultdict(list)
    for c in chains:
        p = c["pts"]
        if len(p) < 2:
            continue
        a, b = p[0], p[-1]
        first, last = p[1], p[-2]
        sa = "b" if rank[first] > rank[a] else "t" if rank[first] < rank[a] else ("r" if x[b] > x[a] else "l")
        sb = "t" if rank[last] < rank[b] else "b" if rank[last] > rank[b] else ("l" if x[b] > x[a] else "r")
        c["sa"], c["sb"] = sa, sb
        ports[(a, sa)].append((x[first], c, "a"))
        ports[(b, sb)].append((x[last], c, "b"))
    for (n, side), lst in ports.items():
        lst.sort(key=lambda t: t[0])
        bx = boxes[n]
        for i, (_, c, end) in enumerate(lst):
            f = (i + 1) / (len(lst) + 1)
            if side in ("b", "t"):
                px = bx["x"] + 10 + (bx["w"] - 20) * f
                py = bx["y"] + bx["h"] if side == "b" else bx["y"]
            else:
                px = bx["x"] + bx["w"] if side == "r" else bx["x"]
                py = bx["y"] + bx["h"] * f
            c[end] = (round(px, 1), round(py, 1))
    paths = {}
    for c in chains:
        p = c["pts"]
        if "a" not in c:
            continue
        mids = [(x[d], y[d] + NODE_H / 2) for d in p[1:-1]]
        pts = [c["a"]] + mids + [c["b"]]
        d = f"M{pts[0][0]},{pts[0][1]}"
        if c["sa"] in ("l", "r"):
            (x0, y0), (x1, y1) = pts[0], pts[-1]
            mx = (x0 + x1) / 2
            d += f" C{mx},{y0} {mx},{y1} {x1},{y1}"
        else:
            for (x0, y0), (x1, y1) in zip(pts, pts[1:]):
                k = (y1 - y0) / 2
                d += f" C{round(x0, 1)},{round(y0 + k, 1)} {round(x1, 1)},{round(y1 - k, 1)} {round(x1, 1)},{round(y1, 1)}"
        mid = pts[len(pts) // 2] if len(pts) > 2 else ((pts[0][0] + pts[1][0]) / 2, (pts[0][1] + pts[1][1]) / 2)
        paths[c["key"]] = {"d": d, "mid": (round(mid[0], 1), round(mid[1], 1))}
    return {"boxes": boxes, "paths": paths, "w": round(total_w), "h": round(total_h), "crossings": best}
