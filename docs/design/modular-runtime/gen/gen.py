import html
import json
import os
import re
import sys
from collections import defaultdict

sys.dont_write_bytecode = True
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import model as M

MOD = "github.com/jack-work/figaro/"
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.dirname(HERE)
ROOT = os.path.abspath(os.path.join(OUT, "..", "..", ".."))
SKIP_DIRS = {".git", "vendor", "scratch", "node_modules", "testdata", "gen"}

IMPORT_BLOCK = re.compile(r"^import\s*\((.*?)^\)", re.S | re.M)
IMPORT_ONE = re.compile(r'^import\s+(?:[\w.]+\s+)?"([^"]+)"', re.M)
QUOTED = re.compile(r'"([^"]+)"')


def esc(s):
    return html.escape(str(s), quote=True)


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


def fmt(n):
    return f"{n:,}"


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


CSS = """
:root{--bg:#fbfaf7;--ink:#1d1b17;--mute:#6b665c;--line:#d8d2c4;--box:#fff;--hl:#f6d77a;--bad:#c0392b;--ok:#7b8794;--acc:#2f5d8a}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1180px;margin:0 auto;padding:20px 24px 60px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:17px;margin:28px 0 8px}h3{font-size:14px;margin:0}
p{margin:6px 0}a{color:var(--acc)}code,.mono{font:12.5px ui-monospace,SFMono-Regular,Menlo,monospace}
nav.top{display:flex;flex-wrap:wrap;gap:6px 14px;margin:4px 0 14px;font-size:13px}
nav.top a.on{font-weight:700;color:var(--ink);text-decoration:none}
.meta{color:var(--mute);font-size:12.5px}
.bar{display:flex;flex-wrap:wrap;gap:6px;align-items:center;margin:8px 0}
.bar b{font-size:12px;color:var(--mute);margin-right:4px}
.bar button{font:12px ui-monospace,monospace;border:1px solid var(--line);background:#fff;border-radius:3px;padding:2px 7px;cursor:pointer}
.bar button.on{background:var(--ink);color:#fff;border-color:var(--ink)}
.bar label{font-size:12.5px;margin-right:8px;cursor:pointer}
#stage{position:relative}
#wires{position:absolute;inset:0;pointer-events:none;overflow:visible}
.band{display:grid;grid-template-columns:150px 1fr;gap:10px;margin:0 0 26px;align-items:start}
.band>.lbl{font-size:12px;color:var(--mute);text-transform:uppercase;letter-spacing:.06em;padding-top:8px}
.band>.lbl a{color:inherit}
.row{display:flex;flex-wrap:wrap;gap:16px}
.box{position:relative;z-index:1;background:var(--box);border:1.5px solid var(--ink);border-radius:4px;min-width:210px;flex:1 1 230px;max-width:520px}
.box.opt{border-style:dashed}.box.new{border-color:var(--acc);border-style:dotted}
.box.dim{opacity:.35}.box.hl{box-shadow:0 0 0 4px var(--hl)}
.box>summary{list-style:none;cursor:pointer;padding:7px 10px}
.box>summary::-webkit-details-marker{display:none}
.box>summary h3{margin-right:8px}.box>summary .n{float:right;color:var(--mute);font:12px ui-monospace,monospace}
.box .tags{display:block;margin-top:3px}
.tag{display:inline-block;font:11px ui-monospace,monospace;padding:0 5px;margin:1px 3px 1px 0;border:1px solid var(--line);border-radius:3px;color:var(--mute)}
.tag.i{border-color:#b9a36a;color:#7a6522}.tag.bad{border-color:var(--bad);color:var(--bad)}
.box .body{padding:4px 10px 10px;border-top:1px solid var(--line);font-size:13px}
table{border-collapse:collapse;width:100%;font-size:12.5px;margin:6px 0}
th,td{text-align:left;padding:3px 6px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--mute);font-weight:600}
td.r,th.r{text-align:right;font-variant-numeric:tabular-nums}
.fate-delete{color:var(--bad);text-decoration:line-through}.fate-split{color:#8a5a00}
.card{display:none;border:1px solid var(--line);background:#fff;border-radius:4px;padding:8px 12px;margin:8px 0}
.card.on{display:block}
.legend{font-size:12px;color:var(--mute)}
.legend i{display:inline-block;width:22px;height:0;border-top:2px solid var(--ok);vertical-align:middle;margin:0 4px 0 10px}
.legend i.bad{border-top:2px solid var(--bad)}

.tog{display:inline-block;width:16px;cursor:pointer;color:var(--mute)}
details[open]>summary .tog{transform:rotate(90deg)}
.box.ext{border-color:#9a948a}.box.sel{background:#1d1b17;color:#fff}.box.sel .n,.box.sel .tog{color:#ddd}
.box.dep{background:#e8f0f8;border-color:#2f5d8a}.box.anc{background:#fbeee0;border-color:#b0651b}
.box.sel .tag{color:#ddd;border-color:#666}
#panel{min-height:40px;margin:6px 0 14px}
.cols{display:grid;grid-template-columns:1fr 1.5fr 1.5fr;gap:14px}
.col{border-left:4px solid var(--line);padding:2px 10px;font-size:13px}.col ul{margin:4px 0;padding-left:18px}
.col.self{border-color:var(--ink)}.col.dep{border-color:#2f5d8a}.col.anc{border-color:#b0651b}
.legend i.dep{border-top:2px solid #2f5d8a}.legend i.anc{border-top:2px solid #b0651b}.legend i.bad{border-top:2px dashed var(--bad)}
svg text.cnt{font:11px ui-monospace,monospace;paint-order:stroke;stroke:#fbfaf7;stroke-width:4px}
.band{margin-bottom:46px}.row{gap:22px 26px}
"""

JS = """
(function(){
var stage=document.getElementById('stage'),svg=document.getElementById('wires'),panel=document.getElementById('panel');
var E=JSON.parse(document.getElementById('edges').textContent);
var NS='http://www.w3.org/2000/svg';
var graph=document.querySelector('input[name=graph]:checked');graph=graph?graph.value:'today';
var sel=null,slice=false,all=false;
function boxes(){return Array.prototype.slice.call(document.querySelectorAll('.box[id^="b-"]'))}
function key(el){return el.id.slice(2)}
function ext(k){var b=document.getElementById('b-'+k);return b&&b.dataset.ext==='1'}
function name(k){var b=document.getElementById('b-'+k);return b?b.querySelector('h3').textContent:k}
function live(){return E.filter(function(e){return e.kind===graph})}
function closure(start,dir){
 var es=live(),seen={},q=[start],out={};
 while(q.length){var n=q.shift();es.forEach(function(e){var a=dir>0?e.from:e.to,b=dir>0?e.to:e.from;
  if(a===n&&!seen[b]&&b!==start){seen[b]=1;out[b]=1;if(!ext(b))q.push(b)}})}
 return out;
}
function rect(el){var r=el.getBoundingClientRect(),s=stage.getBoundingClientRect();return{l:r.left-s.left,t:r.top-s.top,w:r.width,h:r.height}}
function mk(tag,attrs){var e=document.createElementNS(NS,tag);for(var k in attrs)e.setAttribute(k,attrs[k]);return e}
function defs(){var d=mk('defs',{});[['dep','#2f5d8a'],['anc','#b0651b'],['bad','#c0392b'],['all','#8a8f98']].forEach(function(c){
 var m=mk('marker',{id:'ah-'+c[0],viewBox:'0 0 10 10',refX:9,refY:5,markerWidth:7,markerHeight:7,orient:'auto-start-reverse'});
 m.appendChild(mk('path',{d:'M0 0L10 5L0 10z',fill:c[1]}));d.appendChild(m)});svg.appendChild(d)}
function draw(){
 while(svg.firstChild)svg.removeChild(svg.firstChild);
 svg.setAttribute('width',stage.scrollWidth);svg.setAttribute('height',stage.scrollHeight);defs();
 var dep=sel?closure(sel,1):{},anc=sel?closure(sel,-1):{};
 var show=live().filter(function(e){
  if(!sel)return all;
  var inDep=(e.from===sel||dep[e.from]&&!ext(e.from))&&dep[e.to],inAnc=(e.to===sel||anc[e.to]&&!ext(e.to))&&anc[e.from];
  return inDep||inAnc;
 }).map(function(e){var g=!sel?'all':((e.from===sel||dep[e.from])&&dep[e.to]?'dep':'anc');return{e:e,g:g}})
 .filter(function(x){var a=document.getElementById('b-'+x.e.from),b=document.getElementById('b-'+x.e.to);return a&&b&&a.offsetParent&&b.offsetParent});
 var ports={};
 show.forEach(function(x){
  var a=rect(document.getElementById('b-'+x.e.from)),b=rect(document.getElementById('b-'+x.e.to));
  var down=b.t>=a.t+a.h-2,up=b.t+b.h<=a.t+2;
  x.sa=down?'b':up?'t':(b.l>a.l?'r':'l');x.sb=down?'t':up?'b':(b.l>a.l?'l':'r');
  x.a=a;x.b=b;
  (ports[x.e.from+x.sa]=ports[x.e.from+x.sa]||[]).push([x,'a',b.l+b.w/2]);
  (ports[x.e.to+x.sb]=ports[x.e.to+x.sb]||[]).push([x,'b',a.l+a.w/2]);
 });
 Object.keys(ports).forEach(function(k){var l=ports[k];l.sort(function(p,q){return p[2]-q[2]});
  l.forEach(function(p,i){var x=p[0],end=p[1],r=x[end],side=end==='a'?x.sa:x.sb,f=(i+1)/(l.length+1);
   x[end+'p']=side==='b'?[r.l+r.w*f,r.t+r.h]:side==='t'?[r.l+r.w*f,r.t]:side==='r'?[r.l+r.w,r.t+r.h*f]:[r.l,r.t+r.h*f];x[end+'s']=side})});
 show.forEach(function(x){
  var p=x.ap,q=x.bp,dx=0,dy=0,k=Math.max(40,Math.abs(q[1]-p[1])/2);
  var c1=x.as==='b'?[p[0],p[1]+k]:x.as==='t'?[p[0],p[1]-k]:x.as==='r'?[p[0]+60,p[1]]:[p[0]-60,p[1]];
  var c2=x.bs==='t'?[q[0],q[1]-k]:x.bs==='b'?[q[0],q[1]+k]:x.bs==='l'?[q[0]-60,q[1]]:[q[0]+60,q[1]];
  var direct=x.e.from===sel||x.e.to===sel,col=x.e.bad?'bad':x.g,stroke={dep:'#2f5d8a',anc:'#b0651b',bad:'#c0392b',all:'#8a8f98'}[col];
  var path=mk('path',{d:'M'+p+' C'+c1+' '+c2+' '+q,fill:'none',stroke:stroke,'stroke-width':!sel?1.2:direct?2.4:1.1,'stroke-opacity':!sel?.5:direct?.95:.35,'marker-end':'url(#ah-'+col+')'});
  if(x.e.bad)path.setAttribute('stroke-dasharray','6 4');
  var t=mk('title',{});t.textContent=x.e.label;path.appendChild(t);svg.appendChild(path);
  if(sel&&direct&&x.e.count>1){var mx=(p[0]+q[0])/2*.5+(c1[0]+c2[0])/2*.5,my=(p[1]+q[1])/2*.5+(c1[1]+c2[1])/2*.5;
   var tx=mk('text',{x:mx,y:my+4,'text-anchor':'middle','class':'cnt',fill:stroke});tx.textContent=x.e.count;svg.appendChild(tx)}
 });
}
function list(title,set,cls){
 var ks=Object.keys(set);if(!ks.length)return '<div class="col '+cls+'"><b>'+title+'</b><p class=meta>none</p></div>';
 var es=live(),h='<div class="col '+cls+'"><b>'+title+' ('+ks.length+')</b><ul>';
 ks.sort(function(a,b){return name(a).localeCompare(name(b))}).forEach(function(k){
  var d=es.filter(function(e){return (e.from===sel&&e.to===k)||(e.to===sel&&e.from===k)})[0];
  h+='<li><a href="#" data-sel="'+k+'">'+name(k)+'</a>'+(d?' <span class=meta>direct'+(graph==='today'?', '+d.count+' imports':'')+'</span>':'')+(d&&d.bad?' <span class="tag bad">wrong way</span>':'')+'</li>'});
 return h+'</ul></div>';
}
function apply(){
 var dep=sel?closure(sel,1):{},anc=sel?closure(sel,-1):{};
 boxes().forEach(function(b){var k=key(b),hit=!sel||k===sel||dep[k]||anc[k];
  b.classList.toggle('sel',k===sel);b.classList.toggle('dep',!!dep[k]);b.classList.toggle('anc',!!anc[k]);
  b.classList.toggle('dim',!!sel&&!hit);b.style.display=slice&&sel&&!hit?'none':''});
 document.querySelectorAll('.band').forEach(function(bd){var vis=Array.prototype.some.call(bd.querySelectorAll('.box'),function(b){return b.style.display!=='none'});bd.style.display=vis?'':'none'});
 panel.innerHTML=sel?'<div class=cols><div class="col self"><b>selected</b><p>'+name(sel)+'</p><p class=meta>Esc or click it again to clear</p></div>'+list('depends on, transitively',dep,'dep')+list('used by, transitively',anc,'anc')+'</div>':'<p class=meta>Click a box title to select it: what it depends on turns blue, what depends on it turns orange, and only those wires are drawn. Dashed red wires are imports that point the wrong way.</p>';
 panel.querySelectorAll('a[data-sel]').forEach(function(a){a.addEventListener('click',function(ev){ev.preventDefault();pick(a.dataset.sel)})});
 draw();
}
function pick(k){sel=sel===k?null:k;clearTag();try{history.replaceState(null,'',sel?'#sel='+sel:location.pathname+location.search)}catch(e){}apply()}
function clearTag(){document.querySelectorAll('.bar button[data-pick]').forEach(function(o){o.classList.remove('on')});document.querySelectorAll('.card').forEach(function(o){o.classList.remove('on')});document.querySelectorAll('.box').forEach(function(b){b.classList.remove('hl')})}
boxes().forEach(function(b){b.querySelector('summary').addEventListener('click',function(ev){
 if(ev.target.closest('.tog'))return;ev.preventDefault();pick(key(b))})});
document.querySelectorAll('input[name=graph]').forEach(function(i){i.addEventListener('change',function(){graph=i.value;apply()})});
var sl=document.getElementById('slice');if(sl)sl.addEventListener('change',function(){slice=sl.checked;apply()});
var al=document.getElementById('allw');if(al)al.addEventListener('change',function(){all=al.checked;draw()});
document.addEventListener('keydown',function(ev){if(ev.key==='Escape'&&sel){sel=null;apply()}});
var picked=null;
document.querySelectorAll('.bar button[data-pick]').forEach(function(btn){
 btn.addEventListener('click',function(){
  var k=btn.dataset.pick;picked=picked===k?null:k;sel=null;apply();
  document.querySelectorAll('.bar button[data-pick]').forEach(function(o){o.classList.toggle('on',o.dataset.pick===picked)});
  document.querySelectorAll('.card').forEach(function(o){o.classList.toggle('on',o.id==='card-'+picked)});
  boxes().forEach(function(bx){var hit=picked&&(bx.dataset.tags||'').split(' ').indexOf(picked)>=0;
   bx.classList.toggle('hl',!!hit);bx.classList.toggle('dim',!!picked&&!hit)});
 });
});
document.querySelectorAll('details').forEach(function(d){d.addEventListener('toggle',draw)});
var h=/^#sel=(.+)$/.exec(location.hash);if(h&&document.getElementById('b-'+h[1]))sel=h[1];
addEventListener('resize',draw);addEventListener('load',apply);apply();
})();
"""

PAGES = [
    ("index.html", "overview"),
    ("runtime.html", "runtime + WAL"),
    ("hierarchy.html", "hierarchy + federation"),
    ("bindings.html", "bindings"),
    ("agent.html", "agent, bash, sandbox"),
    ("host.html", "host"),
    ("clients.html", "clients"),
]


def nav(cur):
    out = ['<nav class="top">']
    for f, t in PAGES:
        out.append(f'<a href="{f}"{" class=on" if f == cur else ""}>{esc(t)}</a>')
    out.append('<a href="README.md">plan</a><a href="steps.md">steps</a><a href="decisions.md">decisions</a><a href="issues.md">issues</a><a href="mapping.md">mapping</a></nav>')
    return "".join(out)


def page(cur, title, body, edge_list):
    return "\n".join([
        "<!doctype html><html lang=en><head><meta charset=utf-8>",
        f"<meta name=viewport content='width=device-width,initial-scale=1'><title>{esc(title)}</title>",
        f"<style>{CSS}</style></head><body><main>",
        f"<h1>{esc(title)}</h1><p class=meta>figaro {M.VERSION} ({M.BASE}). Lines are non-test lines (wc -l), generated by gen/gen.py.</p>",
        nav(cur), body,
        f"<script type='application/json' id=edges>{json.dumps(edge_list)}</script><script>{JS}</script>",
        "</main></body></html>\n",
    ])


def picker(include_steps, include_issues):
    out = []
    if include_steps:
        out.append('<div class=bar><b>step</b>')
        for s in include_steps:
            out.append(f'<button data-pick="{s["id"]}">{s["id"]}</button>')
        out.append("</div>")
    if include_issues:
        out.append('<div class=bar><b>issue</b>')
        for i in include_issues:
            out.append(f'<button data-pick="#{i["n"]}">#{i["n"]}</button>')
        out.append("</div>")
    return "".join(out)


def cards(include_steps, include_issues):
    out = []
    for s in include_steps:
        out.append(
            f'<div class=card id="card-{s["id"]}"><h3>{s["id"]} {esc(s["title"])} <span class=meta>({esc(s["status"])}; from {esc(s["from"])}; lines {esc(s["est"])})</span></h3>'
            f'<p>{esc(s["what"])}</p><p><b>Breaks:</b> {esc(s["breaks"])}</p><p class=meta>needs {esc(", ".join(s["needs"]) or "nothing")}; gate: {esc(s["gate"])}</p></div>'
        )
    for i in include_issues:
        out.append(
            f'<div class=card id="card-#{i["n"]}"><h3>#{i["n"]} {esc(i["title"])}</h3>'
            f'<p><b>{esc(i["rel"])}</b> {esc(i["steps"])}: {esc(i["how"])}</p></div>'
        )
    return "".join(out)


def legend(graphs):
    radio = ""
    if graphs:
        radio = ('<b>graph</b><label><input type=radio name=graph value=target checked> target dependencies</label>'
                 '<label><input type=radio name=graph value=today> today\'s imports</label>')
    return (
        f'<div class=bar>{radio}<b>view</b><label><input type=checkbox id=slice> show only the selection</label>'
        '<label><input type=checkbox id=allw> all wires</label></div>'
        '<p class=legend>Solid box: required module. Dashed: optional. Dotted: new code. Click a title to select; the arrow on the title opens the files.'
        '<i class=dep></i>depends on<i class=anc></i>used by<i class=bad></i>wrong way</p>'
        '<div id=panel></div>'
    )


def file_table(files):
    rows = ["<table><tr><th>file</th><th class=r>lines</th></tr>"]
    ordered = sorted(files, key=lambda f: (f["fate"] not in ("delete", "split"), -f["lines"]))
    shown, rest = ordered[:14], ordered[14:]
    for f in shown:
        cls = ""
        note = ""
        if f["fate"] == "delete":
            cls, note = " class=fate-delete", " (deleted)"
        elif f["fate"] == "split":
            cls, note = " class=fate-split", " (split across layers)"
        name = f"{f['pkg']}/{f['file']}" if f["pkg"] else f["file"]
        rows.append(f"<tr><td{cls}><code>{esc(name)}</code>{note}</td><td class=r>{fmt(f['lines'])}</td></tr>")
    if rest:
        rows.append(f"<tr><td class=meta>{len(rest)} more files</td><td class=r>{fmt(sum(f['lines'] for f in rest))}</td></tr>")
    rows.append("</table>")
    return "".join(rows)


def pkg_table(files):
    by = defaultdict(lambda: [0, 0])
    for f in files:
        by[f["pkg"]][0] += f["lines"]
        by[f["pkg"]][1] += 1
    rows = ["<table><tr><th>package</th><th class=r>files</th><th class=r>lines</th></tr>"]
    for p, (n, k) in sorted(by.items(), key=lambda kv: -kv[1][0]):
        rows.append(f"<tr><td><code>{esc(p or '(root)')}</code></td><td class=r>{k}</td><td class=r>{fmt(n)}</td></tr>")
    rows.append("</table>")
    return "".join(rows)


def module_box(mod, files, bad_out):
    m = M.MODULES[mod]
    mf = [f for f in files if f["module"] == mod]
    total = sum(f["lines"] for f in mf)
    pk = len({f["pkg"] for f in mf})
    cls = "box" + (" opt" if mod in ("hierarchy", "federation") else "") + (" new" if mod == "bash" else "")
    st = steps_for(mod)
    iss = issues_for(mod)
    tags = " ".join(st + [f"#{n}" for n in iss])
    page_of = next(l["page"] for l in M.LAYERS if mod in l["modules"])
    out = [f'<details class="{cls}" id="b-{mod}" data-tags="{esc(tags)}"><summary><span class=tog title="open">&#9656;</span><h3 style="display:inline">{esc(m["name"])}</h3>'
           f'<span class=n>{fmt(total)} lines, {pk} pkgs</span><span class=tags>']
    out += [f"<span class=tag>{s}</span>" for s in st]
    out += [f"<span class='tag i'>#{n}</span>" for n in iss]
    if bad_out:
        out.append(f"<span class='tag bad'>{bad_out} wrong-way imports</span>")
    out.append(f'</span></summary><div class=body><p>{esc(m["owns"])}</p><p><a href="{page_of}">open the {esc(mod)} view</a></p>')
    out.append(pkg_table(mf) if mf else "<p class=meta>no code yet</p>")
    out.append("</div></details>")
    return "".join(out)


def overview(files, e):
    bad = defaultdict(int)
    for (a, b), v in e.items():
        if not allowed(a, b):
            bad[a] += v["count"]
    body = [
        "<p>The target stack from the design (figaro-design, issue #75), with today's code mapped onto it. Imports point down. "
        "Open a box for its packages; open a layer page for components, files and the imports to cut. "
        "Pick a step or an issue to see what it touches.</p>",
        legend(True), picker(M.STEPS, [i for i in M.ISSUES if i["module"] != "all"]),
        cards(M.STEPS, [i for i in M.ISSUES if i["module"] != "all"]),
        '<div id=stage><svg id=wires></svg>',
    ]
    for layer in M.LAYERS:
        body.append(f'<div class=band><div class=lbl><a href="{layer["page"]}">{esc(layer["name"])}</a></div><div class=row>')
        for mod in layer["modules"]:
            body.append(module_box(mod, files, bad.get(mod, 0)))
        body.append("</div></div>")
    body.append("</div>")
    total = sum(f["lines"] for f in files)
    nbad = sum(v["count"] for (a, b), v in e.items() if not allowed(a, b))
    body.append(f"<p class=meta>{fmt(total)} non-test lines in {len(files)} files; {nbad} file-level imports point the wrong way under this mapping (see mapping.md).</p>")
    el = []
    for mod, m in M.MODULES.items():
        for d in m["deps"]:
            el.append({"from": mod, "to": d, "kind": "target", "count": 1, "bad": False, "label": f"{mod} -> {d}"})
    for (a, b), v in e.items():
        bad = not allowed(a, b)
        el.append({"from": a, "to": b, "kind": "today", "count": v["count"], "bad": bad, "label": f"{a} -> {b}: {v['count']} imports" + (" (wrong way)" if bad else "")})
    return page("index.html", "figaro modular runtime: overview", "\n".join(body), el)


def layer_page(fname, title, mods, intro, files, e):
    comps = []
    for mod in mods:
        for i, c in enumerate(M.COMPONENTS[mod]):
            comps.append((mod, i, c))
    cid = {(mod, i): f"{mod}-{i}" for mod, i, _ in comps}
    ext = defaultdict(lambda: {"count": 0, "bad": False, "sites": set()})
    for f in files:
        if f["module"] not in mods:
            continue
        src = cid[f["comp"]]
        for dep in f["imports"]:
            dm = M.PKG.get(dep)
            if dm is None:
                continue
            if dm in mods:
                tgt = None
                for mod, i, c in comps:
                    if mod == dm and dep in c["pkgs"] and "files" not in c:
                        tgt = cid[(mod, i)]
                        break
                if tgt is None:
                    for mod, i, c in comps:
                        if mod == dm and dep in c["pkgs"]:
                            tgt = cid[(mod, i)]
                            break
                if tgt is None or tgt == src:
                    continue
            else:
                tgt = dm
            k = (src, tgt)
            ext[k]["count"] += 1
            ext[k]["bad"] = ext[k]["bad"] or not allowed(f["module"], dm)
            ext[k]["sites"].add((f"{f['pkg']}/{f['file']}", dep))
    inbound = defaultdict(int)
    inbound_to = defaultdict(int)
    for f in files:
        if f["module"] in mods:
            continue
        for dep in f["imports"]:
            dm = M.PKG.get(dep)
            if dm in mods:
                inbound[f["module"]] += 1
                for mod, i, c in comps:
                    if mod == dm and dep in c["pkgs"]:
                        inbound_to[(f["module"], cid[(mod, i)], mod)] += 1
                        break
    order = [m for layer in M.LAYERS for m in layer["modules"]]
    top = min(order.index(m) for m in mods)
    outside = sorted({t for (_, t) in ext if t in M.MODULES and t not in mods} | {m for m in inbound if m not in mods}, key=order.index)
    others_above = [o for o in outside if order.index(o) < top]
    others_below = [o for o in outside if order.index(o) > top]
    out_n = defaultdict(int)
    for (src, t), v in ext.items():
        if t in M.MODULES:
            out_n[t] += v["count"]
    ctags = {t for _, _, c in comps for t in c["tags"].split()}
    rel_steps = [s for s in M.STEPS if s["id"] in ctags or any(m in s["touches"] for m in mods)]
    rel_issues = [i for i in M.ISSUES if f"#{i['n']}" in ctags]
    body = [f"<p>{esc(intro)}</p>", legend(False), picker(rel_steps, rel_issues), cards(rel_steps, rel_issues), '<div id=stage><svg id=wires></svg>']
    def ext_box(o):
        counts = ", ".join(x for x in (f"{inbound[o]} imports in" if inbound.get(o) else "", f"{out_n[o]} imports out" if out_n.get(o) else "") if x)
        return (f'<details class="box ext" id="b-{o}" data-tags="" data-ext=1><summary><span class=tog title="open">&#9656;</span><h3 style="display:inline">{esc(M.MODULES[o]["name"])}</h3>'
                f'<span class=n>{counts}</span></summary><div class=body><p>{esc(M.MODULES[o]["owns"])}</p></div></details>')
    if others_above:
        body.append('<div class=band><div class=lbl>layers above</div><div class=row>')
        body += [ext_box(o) for o in others_above]
        body.append("</div></div>")
    for mod in mods:
        body.append(f'<div class=band><div class=lbl>{esc(M.MODULES[mod]["name"])}</div><div class=row>')
        for m2, i, c in comps:
            if m2 != mod:
                continue
            cf = [f for f in files if f.get("comp") == (mod, i)]
            n = sum(f["lines"] for f in cf)
            tags = c["tags"].split()
            bad = sum(v["count"] for (s, t), v in ext.items() if s == cid[(mod, i)] and v["bad"])
            cls = "box" + (" opt" if mod in ("hierarchy", "federation") else "") + (" new" if mod == "bash" else "")
            body.append(f'<details class="{cls}" id="b-{cid[(mod, i)]}" data-tags="{esc(" ".join(tags))}"><summary><span class=tog title="open">&#9656;</span><h3 style="display:inline">{esc(c["name"])}</h3><span class=n>{fmt(n)} lines</span><span class=tags>')
            body += [f"<span class=tag>{t}</span>" if not t.startswith("#") else f"<span class='tag i'>{t}</span>" for t in tags]
            if bad:
                body.append(f"<span class='tag bad'>{bad} wrong-way imports</span>")
            body.append("</span></summary><div class=body>")
            if c["note"]:
                body.append(f"<p>{esc(c['note'])}</p>")
            body.append(file_table(cf) if cf else "<p class=meta>no code yet</p>")
            sites = sorted(x for (s, t), v in ext.items() if s == cid[(mod, i)] and v["bad"] for x in v["sites"])
            if sites:
                body.append("<p><b>Imports to cut:</b></p><table><tr><th>file</th><th>imports</th></tr>")
                for a, b in sites:
                    body.append(f"<tr><td><code>{esc(a)}</code></td><td><code>{esc(b)}</code> ({esc(M.PKG[b])})</td></tr>")
                body.append("</table>")
            body.append("</div></details>")
        body.append("</div></div>")
    if others_below:
        body.append('<div class=band><div class=lbl>layers below</div><div class=row>')
        body += [ext_box(o) for o in others_below]
        body.append("</div></div>")
    body.append("</div>")
    el = []
    for (s, t), v in ext.items():
        el.append({"from": s, "to": t, "kind": "today", "count": v["count"], "bad": v["bad"], "label": f"{s} -> {t}: {v['count']} imports" + (" (wrong way)" if v["bad"] else "")})
    for (o, t, mod), n in inbound_to.items():
        bad = not allowed(o, mod)
        el.append({"from": o, "to": t, "kind": "today", "count": n, "bad": bad, "label": f"{o} -> {t}: {n} imports" + (" (wrong way)" if bad else "")})
    return page(fname, title, "\n".join(body), el)


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
