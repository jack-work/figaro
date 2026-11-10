import json
import os
import re
import sys
from collections import defaultdict

sys.dont_write_bytecode = True
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import model as M
import render as R
from render import esc, fmt

MOD = "github.com/jack-work/figaro/"
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.dirname(HERE)
ROOT = os.path.abspath(os.path.join(OUT, "..", "..", ".."))
SKIP_DIRS = {".git", "vendor", "scratch", "node_modules", "testdata", "gen"}

IMPORT_BLOCK = re.compile(r"^import\s*\((.*?)^\)", re.S | re.M)
IMPORT_ONE = re.compile(r'^import\s+(?:[\w.]+\s+)?"([^"]+)"', re.M)
QUOTED = re.compile(r'"([^"]+)"')



def go_files():
    for dp, dns, fns in os.walk(ROOT):
        dns[:] = [d for d in dns if d not in SKIP_DIRS and not d.startswith(".")]
        rel = os.path.relpath(dp, ROOT)
        if rel.split("/")[0] in ("scripts", "duck", "plans", "proposals", "docs", "nix", "skills", "assets"):
            continue
        for fn in fns:
            if fn.endswith(".go") and not fn.endswith("_test.go"):
                yield ("" if rel == "." else rel), fn


def imports_of(src):
    out = []
    for blk in IMPORT_BLOCK.findall(src):
        out += QUOTED.findall(blk)
    out += IMPORT_ONE.findall(src)
    return [i[len(MOD):] for i in out if i.startswith(MOD)]


def file_module(pkg, stem):
    for mod, stems in M.FILES.get(pkg, {}).items():
        if stem in stems:
            return mod
    return M.PKG.get(pkg)


def scan():
    files = []
    for pkg, fn in go_files():
        if pkg not in M.PKG:
            raise SystemExit(f"package {pkg!r} has no module in model.PKG")
        path = os.path.join(ROOT, pkg, fn)
        src = open(path, encoding="utf-8").read()
        stem = fn[:-3]
        mod = file_module(pkg, stem)
        fate = mod
        if mod in ("delete", "split"):
            mod = M.PKG[pkg]
        files.append({
            "pkg": pkg, "file": fn, "stem": stem, "lines": src.count("\n"),
            "module": mod, "fate": fate, "imports": sorted(set(imports_of(src))),
        })
    return files


def allowed(src, dst):
    return src == dst or dst in M.MODULES[src]["deps"]


def edges(files):
    e = defaultdict(lambda: {"count": 0, "sites": set()})
    for f in files:
        for dep in f["imports"]:
            dm = M.PKG.get(dep)
            if dm is None or dm == f["module"]:
                continue
            k = (f["module"], dm)
            e[k]["count"] += 1
            e[k]["sites"].add((f"{f['pkg']}/{f['file']}" if f["pkg"] else f["file"], dep))
    return e


def component_of(comp, f):
    if f["pkg"] not in comp["pkgs"]:
        return False
    if "files" in comp and f["pkg"] in M.FILES:
        return f["stem"] in comp["files"]
    return True


def assign_components(files):
    owner = {}
    for mod, comps in M.COMPONENTS.items():
        for i, c in enumerate(comps):
            if "files" not in c:
                continue
            for f in files:
                if f["module"] == mod and component_of(c, f):
                    owner[(f["pkg"], f["file"])] = (mod, i)
    for mod, comps in M.COMPONENTS.items():
        for i, c in enumerate(comps):
            if "files" in c:
                continue
            for f in files:
                key = (f["pkg"], f["file"])
                if key in owner:
                    continue
                if f["module"] == mod and f["pkg"] in c["pkgs"]:
                    owner[key] = (mod, i)
    for f in files:
        key = (f["pkg"], f["file"])
        if key not in owner:
            raise SystemExit(f"no component for {key} in module {f['module']}")
        f["comp"] = owner[key]
    return files



def steps_for(mod):
    return [s["id"] for s in M.STEPS if mod in s["touches"]]


def issues_for(mod):
    out = []
    for i in M.ISSUES:
        if i["module"] == "all":
            continue
        if mod in i["module"] or (mod == "agent" and "agent" in i["module"]):
            out.append(i["n"])
    return out


def overview(files, e):
    nodes, edges, details = [], [], {}
    for li, layer in enumerate(M.LAYERS):
        for mod in layer["modules"]:
            mf = [f for f in files if f["module"] == mod]
            cls = ["opt"] if mod in ("hierarchy", "federation") else ["new"] if mod == "bash" else []
            tags = steps_for(mod) + [f"#{n}" for n in issues_for(mod)]
            nodes.append({"id": mod, "band": li, "label": M.MODULES[mod]["name"], "sub": f"{fmt(sum(f['lines'] for f in mf))} lines, {len({f['pkg'] for f in mf})} pkgs", "cls": cls, "tags": tags})
            details[mod] = f'<p>{esc(M.MODULES[mod]["owns"])}</p><p><a href="{layer["page"]}">open the {esc(layer["name"])} page</a></p>' + (R.pkg_table(mf) if mf else "<p class=meta>no code yet</p>")
    for mod, m in M.MODULES.items():
        for d in m["deps"]:
            if d == "base":
                continue
            edges.append({"from": mod, "to": d, "kind": "target", "count": 1, "bad": False, "label": f"{M.MODULES[mod]['name']} may import {M.MODULES[d]['name']}"})
    for (a, b), v in sorted(e.items()):
        bad = not allowed(a, b)
        if b == "base" and not bad:
            continue
        edges.append({"from": a, "to": b, "kind": "today", "count": v["count"], "bad": bad, "sites": v["sites"],
                      "label": f"{M.MODULES[a]['name']} imports {M.MODULES[b]['name']}: {v['count']}" + (" (wrong way)" if bad else "")})
    total = sum(f["lines"] for f in files)
    nbad = sum(v["count"] for (a, b), v in e.items() if not allowed(a, b))
    intro = (f"The target stack from the design (#75) with today's code mapped onto it: {fmt(total)} non-test lines, {nbad} imports pointing the wrong way. "
             "Every module may import api + leaf libs; those wires are left out unless they point the wrong way. Switch between the target dependencies and today's imports; each layer page breaks a box into its parts.")
    return R.page("index.html", "figaro modular runtime: overview", intro, nodes, edges, details,
                  M.STEPS, [i for i in M.ISSUES if i["module"] != "all"], [("target", "target"), ("today", "today's imports")])


def layer_page(fname, title, mods, intro, files, e):
    order = [m for layer in M.LAYERS for m in layer["modules"]]
    comps = [(mod, i, c) for mod in mods for i, c in enumerate(M.COMPONENTS[mod])]
    cid = {(mod, i): f"{mod}-{i}" for mod, i, _ in comps}

    def comp_for(dep, dm):
        for mod, i, c in comps:
            if mod == dm and dep in c["pkgs"] and "files" not in c:
                return cid[(mod, i)]
        for mod, i, c in comps:
            if mod == dm and dep in c["pkgs"]:
                return cid[(mod, i)]
        return None

    ed = defaultdict(lambda: {"count": 0, "bad": False, "sites": set()})
    for f in files:
        here = f["module"] in mods
        src = cid[f["comp"]] if here else f["module"]
        for dep in f["imports"]:
            dm = M.PKG.get(dep)
            if dm is None:
                continue
            if dm in mods:
                tgt = comp_for(dep, dm)
                if tgt is None:
                    continue
            elif here:
                tgt = dm
            else:
                continue
            if tgt == src:
                continue
            k = (src, tgt)
            ed[k]["count"] += 1
            ed[k]["bad"] = ed[k]["bad"] or not allowed(f["module"], dm)
            ed[k]["sites"].add((f"{f['pkg']}/{f['file']}", dep))
    outside = sorted({x for k in ed for x in k if x in M.MODULES and x not in mods}, key=order.index)
    nodes, details = [], {}
    for o in outside:
        nin = sum(v["count"] for (a, b), v in ed.items() if a == o)
        nout = sum(v["count"] for (a, b), v in ed.items() if b == o)
        sub = ", ".join(x for x in (f"{nin} imports in" if nin else "", f"{nout} imports out" if nout else "") if x)
        page_of = next(l["page"] for l in M.LAYERS if o in l["modules"])
        band = -1 if order.index(o) < min(order.index(m) for m in mods) else len(order)
        nodes.append({"id": o, "band": band, "label": M.MODULES[o]["name"], "sub": sub, "cls": ["ext"]})
        details[o] = f'<p>{esc(M.MODULES[o]["owns"])}</p><p><a href="{page_of}">open its page</a></p>'
    for mod, i, c in comps:
        k = cid[(mod, i)]
        cf = [f for f in files if f.get("comp") == (mod, i)]
        cls = ["opt"] if mod in ("hierarchy", "federation") else ["new"] if mod == "bash" else []
        nodes.append({"id": k, "band": order.index(mod), "label": c["name"], "sub": f"{fmt(sum(f['lines'] for f in cf))} lines", "cls": cls, "tags": c["tags"].split()})
        body = [f"<p class=meta>{esc(M.MODULES[mod]['name'])}</p>"]
        if c["note"]:
            body.append(f"<p>{esc(c['note'])}</p>")
        body.append(R.file_table(cf) if cf else "<p class=meta>no code yet</p>")
        sites = sorted(x for (a, b), v in ed.items() if a == k and v["bad"] for x in v["sites"])
        if sites:
            body.append("<p><b>Imports to cut:</b></p><table><tr><th>file</th><th>imports</th></tr>")
            body += [f"<tr><td><code>{esc(a)}</code></td><td><code>{esc(b)}</code> ({esc(M.PKG[b])})</td></tr>" for a, b in sites]
            body.append("</table>")
        details[k] = "".join(body)
    label = {n["id"]: n["label"] for n in nodes}
    edges = []
    for (a, b), v in sorted(ed.items()):
        edges.append({"from": a, "to": b, "kind": "today", "count": v["count"], "bad": v["bad"], "sites": v["sites"],
                      "label": f"{label[a]} imports {label[b]}: {v['count']}" + (" (wrong way)" if v["bad"] else "")})
    ctags = {t for _, _, c in comps for t in c["tags"].split()}
    steps = [s for s in M.STEPS if s["id"] in ctags]
    issues = [i for i in M.ISSUES if f"#{i['n']}" in ctags]
    return R.page(fname, title, intro, nodes, edges, details, steps, issues, [("today", "today's imports")])


def md_mapping(files, e):
    out = [f"# Today's packages on the target stack\n\nGenerated by `gen/gen.py` from figaro {M.VERSION} ({M.BASE}). Lines are non-test lines (wc -l).\n"]
    out.append("\n## Per module\n\n| module | packages | files | lines |\n|---|---:|---:|---:|\n")
    for layer in M.LAYERS:
        for mod in layer["modules"]:
            mf = [f for f in files if f["module"] == mod]
            out.append(f"| {M.MODULES[mod]['name']} | {len({f['pkg'] for f in mf})} | {len(mf)} | {fmt(sum(f['lines'] for f in mf))} |\n")
    dele = [f for f in files if f["fate"] == "delete"]
    split = [f for f in files if f["fate"] == "split"]
    out.append(f"\nDeleted outright by the plan: {', '.join('`'+f['pkg']+'/'+f['file']+'`' for f in dele)} ({fmt(sum(f['lines'] for f in dele))} lines). ")
    out.append(f"Split across layers (counted under runtime): {', '.join('`'+f['pkg']+'/'+f['file']+'`' for f in split)} ({fmt(sum(f['lines'] for f in split))} lines).\n")
    out.append("\n## Per package\n\n| package | module | lines | files moving elsewhere |\n|---|---|---:|---|\n")
    by = defaultdict(list)
    for f in files:
        by[f["pkg"]].append(f)
    for p in sorted(by):
        fs = by[p]
        moved = sorted({f"{f['stem']} -> {f['fate']}" for f in fs if f["fate"] != M.PKG[p]})
        out.append(f"| `{p or '(root)'}` | {M.PKG[p]} | {fmt(sum(f['lines'] for f in fs))} | {', '.join(moved)} |\n")
    out.append("\n## Imports that point the wrong way\n\nAn import from module A to module B where B is not below A in the target stack. Each must be cut, inverted or moved by a step.\n\n| from | to | imports | sites |\n|---|---|---:|---|\n")
    for (a, b), v in sorted(e.items(), key=lambda kv: -kv[1]["count"]):
        if allowed(a, b):
            continue
        sites = sorted(v["sites"])
        shown = "; ".join(f"`{s}` -> `{d}`" for s, d in sites[:6]) + (f"; and {len(sites) - 6} more" if len(sites) > 6 else "")
        out.append(f"| {a} | {b} | {v['count']} | {shown} |\n")
    return "".join(out)


def md_steps():
    out = [f"# Steps\n\nGenerated from `gen/model.py`. Each step is one release, bottom-up, each net-negative in lines unless the row says otherwise (decision D11). Track S is the runtime, K the clients, X the sandbox; K and X run beside S.\n\n"]
    out.append("| step | title | from | needs | lines | status |\n|---|---|---|---|---|---|\n")
    for s in M.STEPS:
        out.append(f"| {s['id']} | {s['title']} | {s['from']} | {', '.join(s['needs']) or '-'} | {s['est']} | {s['status']} |\n")
    for s in M.STEPS:
        out.append(f"\n## {s['id']}: {s['title']}\n\n{s['what']}\n\n- From: {s['from']}\n- Needs: {', '.join(s['needs']) or 'nothing'}\n- Lines: {s['est']}\n- Breaks: {s['breaks']}\n- Gate: {s['gate']}\n")
        iss = [i for i in M.ISSUES if s["id"] in re.split(r"[ ,]+", i["steps"])]
        if iss:
            out.append("- Issues: " + ", ".join(f"#{i['n']} ({i['rel']})" for i in iss) + "\n")
    return "".join(out)


def md_decisions():
    out = ["# Decisions for Gluck\n\nGenerated from `gen/model.py`. Where the design (figaro-design, #75) and the carve plan (backlog section 3, the 11-04 audit, the 11-09 captains-log) disagree, or a question is still open. Each has a recommendation; none is taken.\n"]
    for d in M.DECISIONS:
        out.append(f"\n## {d['id']}: {d['title']}\n\n- One side: {d['a']}\n")
        if d["b"]:
            out.append(f"- Other side: {d['b']}\n")
        out.append(f"- Recommendation: {d['rec']}\n- Step: {d['step']}\n")
    return "".join(out)


def md_issues():
    out = [f"# Open issues against the plan\n\nEvery open issue on jack-work/figaro, read on 2026-11-09 (17 open). Generated from `gen/model.py`. \"Folds into\": do it as part of that step. \"Blocked by\": waits for it. \"Independent\": can land any time; the step named is where it meets the plan.\n\n"]
    out.append("| issue | module | relation | step | how |\n|---|---|---|---|---|\n")
    for i in M.ISSUES:
        out.append(f"| [#{i['n']}](https://github.com/jack-work/figaro/issues/{i['n']}) {i['title']} | {i['module']} | {i['rel']} | {i['steps']} | {i['how']} |\n")
    return "".join(out)


def main():
    files = assign_components(scan())
    e = edges(files)
    w = lambda name, s: open(os.path.join(OUT, name), "w").write(s)
    w("index.html", overview(files, e))
    w("runtime.html", layer_page("runtime.html", "runtime + WAL (and the api base)", ["runtime", "base"],
        "One layer: figwal and the runtime half of the store, with the actor. Steps S1 to S6 live here. Boxes are components; wires are today's imports between them and out of the layer.", files, e))
    w("hierarchy.html", layer_page("hierarchy.html", "hierarchy and federation (optional modules)", ["hierarchy", "federation"],
        "Fork, lineage and the drawn tree, spread today over five places with six clocks, become one optional module whose state is one reducible form (S7a-S7d). Federation sits beside it.", files, e))
    w("bindings.html", layer_page("bindings.html", "figaro bindings", ["bindings"],
        "A form bound to an aria is a figaro. The bindings drain the inbox, call run and group-commit the ops. Studies, librettos and roles live here.", files, e))
    w("agent.html", layer_page("agent.html", "run implementations: agent, bash, tools", ["agent", "bash", "sandbox"],
        "The agent step and its providers, the bash harness that proves the seam, and the tool and sandbox module both of them call.", files, e))
    w("host.html", layer_page("host.html", "host (figarod)", ["host"],
        "Composition: the module registry, RPC, authz, outfits, credentials, config, the UI stream and session state.", files, e))
    w("clients.html", layer_page("clients.html", "clients (figaro)", ["clients"],
        "The client binary after K1: cli, pager, input and sdk, talking to a local or remote figarod. Today cmd/figaro links the daemon too.", files, e))
    w("mapping.md", md_mapping(files, e))
    w("steps.md", md_steps())
    w("decisions.md", md_decisions())
    w("issues.md", md_issues())
    total = sum(f["lines"] for f in files)
    print(f"{len(files)} files, {total} lines, {sum(v['count'] for (a, b), v in e.items() if not allowed(a, b))} wrong-way imports")


main()
