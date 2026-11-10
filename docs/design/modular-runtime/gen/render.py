import html
import json
from collections import defaultdict

import model as M
from layout import layout

STYLE = """
:root{--bg:#f5f6f8;--panel:#fff;--ink:#14161b;--mute:#667080;--rule:#d6dae1;--node:#fff;--dep:#0b7a84;--anc:#bf650f;--bad:#d2372b;--sel:#14161b}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{padding:18px 22px 60px;max-width:1500px;margin:0 auto}
h1{font-size:21px;margin:0 0 2px;letter-spacing:-.01em}h2{font-size:16px;margin:30px 0 8px}h3{font-size:14px;margin:0 0 4px}
p{margin:6px 0}a{color:var(--dep)}code,.mono{font:12.5px ui-monospace,SFMono-Regular,Menlo,monospace}
.meta{color:var(--mute);font-size:12.5px}
nav{display:flex;flex-wrap:wrap;gap:4px 14px;margin:6px 0 10px;font-size:13px}nav a.on{color:var(--ink);font-weight:700;text-decoration:none}
.tools{display:flex;flex-wrap:wrap;align-items:center;gap:6px 10px;margin:10px 0;font-size:12.5px}
.tools .grp{display:flex;flex-wrap:wrap;align-items:center;gap:4px}
.tools b{color:var(--mute);font-weight:600;margin-right:2px}
.chip{font:11.5px ui-monospace,monospace;border:1px solid var(--rule);background:#fff;border-radius:999px;padding:1px 8px;cursor:pointer;color:var(--ink)}
.chip.on{background:var(--ink);color:#fff;border-color:var(--ink)}
.seg{display:inline-flex;border:1px solid var(--rule);border-radius:6px;overflow:hidden}
.seg button{border:0;background:#fff;padding:3px 10px;font:12.5px inherit;cursor:pointer}.seg button.on{background:var(--ink);color:#fff}
input[type=search]{font:13px inherit;padding:3px 8px;border:1px solid var(--rule);border-radius:6px;width:180px}
.work{display:grid;grid-template-columns:minmax(0,1fr) 340px;gap:16px;align-items:start}
@media (max-width:1100px){.work{grid-template-columns:1fr}.insp{position:static!important;max-height:none!important}}
.viewport{position:relative;overflow:auto;background:#fff;border:1px solid var(--rule);border-radius:8px;max-height:78vh;cursor:grab}
.viewport.drag{cursor:grabbing}
.canvas{position:relative;transform-origin:0 0}
.canvas svg{position:absolute;left:0;top:0;overflow:visible}
.e{fill:none;stroke:#9aa3b0;stroke-width:1.3;stroke-opacity:.55;transition:stroke-opacity .15s,stroke-width .15s}
.e.bad{stroke:var(--bad);stroke-dasharray:5 4;stroke-opacity:.45}
.e.hit{stroke:transparent;stroke-width:12;stroke-opacity:1;pointer-events:stroke;cursor:pointer}
.focus .e{stroke-opacity:.07}
.focus .e.bad{stroke-opacity:.12}
.e.on{stroke-opacity:.95!important;stroke-width:2.6}
.e.on.dep{stroke:var(--dep);marker-end:url(#m-dep)}.e.on.anc{stroke:var(--anc);marker-end:url(#m-anc)}.e.on.bad{stroke:var(--bad);marker-end:url(#m-bad)}
.e.on.far{stroke-width:1.4;stroke-opacity:.55!important}
.e.flow{stroke-dasharray:7 5;animation:flow 1.1s linear infinite}
@keyframes flow{to{stroke-dashoffset:-24}}
@media (prefers-reduced-motion:reduce){.e.flow{animation:none}}
.e.hidden,.hit.hidden{display:none}
.n{position:absolute;background:var(--node);border:1.5px solid var(--ink);border-radius:7px;padding:5px 9px;cursor:pointer;user-select:none;transition:opacity .15s,background .15s}
.n .t{font-weight:650;font-size:13px;white-space:nowrap;display:block}
.n .s{font:11px ui-monospace,monospace;color:var(--mute);white-space:nowrap;display:block}
.n.opt{border-style:dashed}.n.new{border-style:dotted;border-color:var(--dep)}.n.ext{border-color:#9aa3b0;background:#f0f2f5}
.n.tagged{box-shadow:0 0 0 4px #f4d35e}
.focus .n{opacity:.28}
.focus .n.sel,.focus .n.dep,.focus .n.anc{opacity:1}
.n.sel{background:var(--sel);color:#fff;border-color:var(--sel)}.n.sel .s{color:#c9ced6}
.n.dep{border-color:var(--dep);background:#e6f3f4}.n.anc{border-color:var(--anc);background:#fbefe2}
.n.dep.anc{background:linear-gradient(90deg,#e6f3f4 50%,#fbefe2 50%)}
.cnt{position:absolute;transform:translate(-50%,-50%);font:11px ui-monospace,monospace;background:#fff;border:1px solid currentColor;border-radius:999px;padding:0 5px;pointer-events:none;display:none}
.cnt.on{display:block}
.insp{position:sticky;top:10px;background:var(--panel);border:1px solid var(--rule);border-radius:8px;padding:12px 14px;max-height:86vh;overflow:auto;font-size:13px}
.insp h3{font-size:15px}
.insp code{word-break:break-all}.insp table{table-layout:fixed}.insp td.r,.insp th.r{width:56px}
.insp .lists{display:grid;gap:10px;margin-top:8px}
.insp ul{margin:2px 0;padding-left:16px}.insp li{margin:1px 0}
.insp .k{font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:var(--mute)}
.insp .k.dep{color:var(--dep)}.insp .k.anc{color:var(--anc)}.insp .k.bad{color:var(--bad)}
.badge{display:inline-block;font:10.5px ui-monospace,monospace;padding:0 5px;border-radius:999px;border:1px solid var(--rule);color:var(--mute);margin-left:4px}
.badge.bad{border-color:var(--bad);color:var(--bad)}
.legend{display:flex;flex-wrap:wrap;gap:4px 16px;font-size:12px;color:var(--mute);margin:6px 0}
.legend i{display:inline-block;width:24px;vertical-align:middle;margin-right:5px;border-top:2.5px solid}
.legend i.dep{border-color:var(--dep)}.legend i.anc{border-color:var(--anc)}.legend i.bad{border-top-style:dashed;border-color:var(--bad)}
table{border-collapse:collapse;font-size:12.5px;margin:6px 0;width:100%}
th,td{text-align:left;padding:3px 6px;border-bottom:1px solid var(--rule);vertical-align:top}th{color:var(--mute);font-weight:600}
td.r,th.r{text-align:right;font-variant-numeric:tabular-nums}
.fate-delete{color:var(--bad);text-decoration:line-through}.fate-split{color:#8a5a00}
table.dsm{width:auto;border:1px solid var(--rule);background:#fff}
.dsm th,.dsm td{border:1px solid #e7eaee;padding:2px 5px;text-align:center;font:11.5px ui-monospace,monospace}
.dsm th.row{text-align:right;white-space:nowrap;font-family:inherit}
.dsm th.col{writing-mode:vertical-rl;transform:rotate(180deg);white-space:nowrap;font-family:inherit;height:auto}
.dsm td.self{background:#eef0f3}.dsm td.ok{background:#e6f3f4;cursor:pointer}.dsm td.bad{background:#fbe1de;color:var(--bad);font-weight:700;cursor:pointer}
details.det{background:#fff;border:1px solid var(--rule);border-radius:8px;margin:8px 0;padding:6px 12px}
details.det>summary{cursor:pointer;font-weight:650}
.card{display:none}
"""

SCRIPT = """
(function(){
var D=JSON.parse(document.getElementById('graph').textContent);
var work=document.querySelector('.work'),vp=document.querySelector('.viewport'),cv=document.querySelector('.canvas'),insp=document.getElementById('insp');
var kind=D.kinds[0],sel=null,selE=null,hover=null,tag=null,zoom=1;
var nodeEl={},pathEl={},hitEl={},cntEl={};
document.querySelectorAll('.n').forEach(function(n){nodeEl[n.dataset.id]=n});
D.edges.forEach(function(e,i){e.i=i;pathEl[i]=document.getElementById('e'+i);hitEl[i]=document.getElementById('h'+i);cntEl[i]=document.getElementById('c'+i)});
var gEdges=document.getElementById('edges');
function live(){return D.edges.filter(function(e){return e.kind===kind})}
function isExt(id){return D.nodes[id]&&D.nodes[id].ext}
function closure(start,dir){var es=live(),out={},q=[start];
 while(q.length){var n=q.shift();es.forEach(function(e){var a=dir>0?e.from:e.to,b=dir>0?e.to:e.from;
  if(a===n&&b!==start&&!out[b]){out[b]=1;if(!isExt(b))q.push(b)}})}return out}
function name(id){return D.nodes[id]?D.nodes[id].label:id}
function link(id){return '<a href="#" data-go="'+id+'">'+name(id)+'</a>'}
function li(id,e,extra){return '<li>'+link(id)+(e?'<span class="badge'+(e.bad?' bad':'')+'">'+(kind==='today'?e.count+' import'+(e.count===1?'':'s'):'direct')+(e.bad?', wrong way':'')+'</span>':'')+(extra||'')+'</li>'}
function render(){
 var es=live(),focus=!!(sel||selE||hover),dep={},anc={},on={};
 var root=sel||hover;
 if(root){dep=closure(root,1);anc=closure(root,-1)}
 D.edges.forEach(function(e){var vis=e.kind===kind;pathEl[e.i].classList.toggle('hidden',!vis);hitEl[e.i].classList.toggle('hidden',!vis)});
 es.forEach(function(e){
  var c=null;
  if(selE!==null){if(e.i===selE)c='dep'}
  else if(root){
   if((e.from===root||dep[e.from]&&!isExt(e.from))&&dep[e.to])c='dep';
   else if((e.to===root||anc[e.to]&&!isExt(e.to))&&anc[e.from])c='anc';
  }
  on[e.i]=c;
 });
 cv.classList.toggle('focus',focus);
 Object.keys(nodeEl).forEach(function(id){var n=nodeEl[id];
  var se=selE!==null?D.edges[selE]:null;
  n.classList.toggle('sel',id===root||(se&&(id===se.from||id===se.to)));
  n.classList.toggle('dep',!!dep[id]);n.classList.toggle('anc',!!anc[id]);
  n.classList.toggle('tagged',!!tag&&(D.nodes[id].tags||[]).indexOf(tag)>=0)});
 var raise=[];
 D.edges.forEach(function(e){var p=pathEl[e.i],c=on[e.i],direct=c&&(e.from===root||e.to===root||selE===e.i);
  p.classList.toggle('on',!!c);p.classList.toggle('dep',c==='dep');p.classList.toggle('anc',c==='anc');
  p.classList.toggle('far',!!c&&!direct);
  if(cntEl[e.i]){cntEl[e.i].classList.toggle('on',!!direct&&kind==='today');cntEl[e.i].style.color=e.bad?'var(--bad)':c==='anc'?'var(--anc)':'var(--dep)'}
  if(c)raise.push(p)});
 raise.forEach(function(p){gEdges.appendChild(p)});
 if(!hover)panel(dep,anc);
}
function panel(dep,anc){
 var h='';
 if(selE!==null){var e=D.edges[selE];
  h='<div class=k>import</div><h3>'+link(e.from)+' &rarr; '+link(e.to)+'</h3><p class=meta>'+(kind==='today'?e.count+' imports':'target dependency')+(e.bad?' &middot; <b style="color:var(--bad)">wrong way</b>':'')+'</p>';
  if(e.sites&&e.sites.length){h+='<table><tr><th>file</th><th>imports</th></tr>';e.sites.forEach(function(s){h+='<tr><td><code>'+s[0]+'</code></td><td><code>'+s[1]+'</code></td></tr>'});h+='</table>'}
 }else if(sel){var n=D.nodes[sel],es=live();
  h='<div class=k>selected</div><h3>'+n.label+'</h3><p class=meta>'+n.sub+'</p>';
  var dd=es.filter(function(e){return e.from===sel}),uu=es.filter(function(e){return e.to===sel});
  h+='<div class=lists><div><div class="k dep">depends on: '+dd.length+' direct, '+Object.keys(dep).length+' in all</div><ul>';
  dd.sort(function(a,b){return b.count-a.count}).forEach(function(e){h+=li(e.to,e)});
  Object.keys(dep).filter(function(k){return !dd.some(function(e){return e.to===k})}).forEach(function(k){h+='<li class=meta>'+link(k)+' (through others)</li>'});
  h+='</ul></div><div><div class="k anc">used by: '+uu.length+' direct, '+Object.keys(anc).length+' in all</div><ul>';
  uu.sort(function(a,b){return b.count-a.count}).forEach(function(e){h+=li(e.from,e)});
  Object.keys(anc).filter(function(k){return !uu.some(function(e){return e.from===k})}).forEach(function(k){h+='<li class=meta>'+link(k)+' (through others)</li>'});
  h+='</ul></div></div>';
  var det=document.getElementById('detail-'+sel);if(det)h+='<hr>'+det.querySelector('.body').innerHTML;
 }else if(tag){var c=document.getElementById('card-'+tag);h=c?c.innerHTML:'';
 }else h=document.getElementById('insp-help').innerHTML;
 insp.innerHTML=h;
 insp.querySelectorAll('a[data-go]').forEach(function(a){a.addEventListener('click',function(ev){ev.preventDefault();choose(a.dataset.go)})});
}
function choose(id){selE=null;tag=null;chips();sel=sel===id?null:id;hover=null;
 try{history.replaceState(null,'',sel?'#sel='+sel:location.pathname+location.search)}catch(e){}
 render();if(sel)reveal(nodeEl[sel])}
function reveal(el){var r=el.getBoundingClientRect(),v=vp.getBoundingClientRect();
 if(r.left<v.left||r.right>v.right||r.top<v.top||r.bottom>v.bottom){vp.scrollLeft+=r.left-v.left-v.width/2+r.width/2;vp.scrollTop+=r.top-v.top-v.height/2+r.height/2}}
Object.keys(nodeEl).forEach(function(id){var n=nodeEl[id];
 n.addEventListener('click',function(ev){ev.stopPropagation();if(moved)return;choose(id)});
 n.addEventListener('mouseenter',function(){if(!sel&&selE===null){hover=id;render()}});
 n.addEventListener('mouseleave',function(){if(hover){hover=null;render()}})});
D.edges.forEach(function(e){hitEl[e.i].addEventListener('click',function(ev){ev.stopPropagation();sel=null;tag=null;chips();selE=selE===e.i?null:e.i;render()})});
vp.addEventListener('click',function(){if(moved)return;sel=null;selE=null;render()});
document.querySelectorAll('[data-kind]').forEach(function(b){b.addEventListener('click',function(){kind=b.dataset.kind;
 document.querySelectorAll('[data-kind]').forEach(function(o){o.classList.toggle('on',o===b)});selE=null;render()})});
function chips(){document.querySelectorAll('.chip[data-tag]').forEach(function(c){c.classList.toggle('on',c.dataset.tag===tag)})}
document.querySelectorAll('.chip[data-tag]').forEach(function(c){c.addEventListener('click',function(){tag=tag===c.dataset.tag?null:c.dataset.tag;sel=null;selE=null;chips();render()})});
document.querySelectorAll('.dsm td[data-e]').forEach(function(td){td.addEventListener('click',function(){sel=null;tag=null;chips();selE=+td.dataset.e;render();vp.scrollIntoView({block:'nearest'})})});
var q=document.getElementById('find');
if(q)q.addEventListener('keydown',function(ev){if(ev.key==='Enter'){var v=q.value.toLowerCase();var hit=Object.keys(D.nodes).filter(function(id){return name(id).toLowerCase().indexOf(v)>=0})[0];if(hit){sel=null;choose(hit)}}});
document.addEventListener('keydown',function(ev){if(ev.key==='Escape'){sel=null;selE=null;tag=null;chips();render()}
 if(ev.key==='/'&&document.activeElement!==q&&q){ev.preventDefault();q.focus()}});
function setZoom(z){zoom=Math.max(.3,Math.min(2,z));cv.style.transform='scale('+zoom+')';cv.parentNode.style.width=(D.w*zoom)+'px';cv.parentNode.style.height=(D.h*zoom)+'px'}
document.querySelectorAll('[data-zoom]').forEach(function(b){b.addEventListener('click',function(){var z=b.dataset.zoom;
 setZoom(z==='fit'?Math.min(1,(vp.clientWidth-4)/D.w):zoom*(+z))})});
var moved=false,dragging=null;
vp.addEventListener('mousedown',function(ev){if(ev.button!==0)return;dragging={x:ev.clientX,y:ev.clientY,l:vp.scrollLeft,t:vp.scrollTop};moved=false});
addEventListener('mousemove',function(ev){if(!dragging)return;var dx=ev.clientX-dragging.x,dy=ev.clientY-dragging.y;
 if(Math.abs(dx)+Math.abs(dy)>4){moved=true;vp.classList.add('drag')}vp.scrollLeft=dragging.l-dx;vp.scrollTop=dragging.t-dy});
addEventListener('mouseup',function(){dragging=null;vp.classList.remove('drag');setTimeout(function(){moved=false},0)});
var h=/^#sel=(.+)$/.exec(location.hash);if(h&&nodeEl[h[1]])sel=h[1];
setZoom(Math.max(.8,Math.min(1,(vp.clientWidth-4)/D.w)));render();if(sel)reveal(nodeEl[sel]);
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


def esc(s):
    return html.escape(str(s), quote=True)


def fmt(n):
    return f"{n:,}"


def nav(cur):
    out = ["<nav>"]
    for f, t in PAGES:
        out.append(f'<a href="{f}"{" class=on" if f == cur else ""}>{esc(t)}</a>')
    out.append('<a href="README.md">plan</a><a href="steps.md">steps</a><a href="decisions.md">decisions</a><a href="issues.md">issues</a><a href="mapping.md">mapping</a></nav>')
    return "".join(out)


def file_table(files):
    rows = ["<table><tr><th>file</th><th class=r>lines</th></tr>"]
    ordered = sorted(files, key=lambda f: (f["fate"] not in ("delete", "split"), -f["lines"]))
    shown, rest = ordered[:14], ordered[14:]
    for f in shown:
        cls, note = "", ""
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


def tag_badges(tags):
    return "".join(f"<span class=badge>{esc(t)}</span>" for t in tags)


def chips(steps, issues):
    out = []
    if steps:
        out.append('<div class=grp><b>step</b>' + "".join(f'<button class=chip data-tag="{s["id"]}">{s["id"]}</button>' for s in steps) + "</div>")
    if issues:
        out.append('<div class=grp><b>issue</b>' + "".join(f'<button class=chip data-tag="#{i["n"]}">#{i["n"]}</button>' for i in issues) + "</div>")
    return "".join(out)


def cards(steps, issues):
    out = []
    for s in steps:
        out.append(
            f'<div class=card id="card-{s["id"]}"><div class=k>step</div><h3>{s["id"]} {esc(s["title"])}</h3><p class=meta>{esc(s["status"])}; from {esc(s["from"])}; lines {esc(s["est"])}</p>'
            f'<p>{esc(s["what"])}</p><p><b>Breaks:</b> {esc(s["breaks"])}</p><p class=meta>needs {esc(", ".join(s["needs"]) or "nothing")}; gate: {esc(s["gate"])}</p><p class=meta>Highlighted boxes are the ones this step touches.</p></div>'
        )
    for i in issues:
        out.append(
            f'<div class=card id="card-#{i["n"]}"><div class=k>issue</div><h3>#{i["n"]} {esc(i["title"])}</h3>'
            f'<p><b>{esc(i["rel"])}</b> {esc(i["steps"])}: {esc(i["how"])}</p></div>'
        )
    return "".join(out)


HELP = (
    "<div id=insp-help hidden><div class=k>how to read this</div><h3>Pick a box</h3>"
    "<p>Hover a box to preview its direct imports. Click it to trace: everything it depends on, directly or through others, turns teal; everything that depends on it turns amber; the rest fades. "
    "Direct wires are thick, with an arrowhead and their import count; indirect ones are thin.</p>"
    "<p>Click a wire to list the imports behind it, file by file. Red dashed wires point the wrong way for the target stack.</p>"
    "<p>Drag to pan, the zoom buttons to scale, / to find a box, Esc to clear. The matrix under the graph shows every pair at once; click a cell.</p></div>"
)


def dsm(nodes, edges, lay, kind):
    order = sorted(nodes, key=lambda n: (lay["boxes"][n["id"]]["rank"], lay["boxes"][n["id"]]["x"]))
    idx = {}
    for i, e in enumerate(edges):
        if e["kind"] == kind:
            idx[(e["from"], e["to"])] = (i, e)
    out = ["<table class=dsm><tr><th></th>"]
    out += [f'<th class=col>{esc(n["label"])}</th>' for n in order]
    out.append("</tr>")
    for a in order:
        out.append(f'<tr><th class=row>{esc(a["label"])}</th>')
        for b in order:
            if a["id"] == b["id"]:
                out.append("<td class=self></td>")
                continue
            hit = idx.get((a["id"], b["id"]))
            if not hit:
                out.append("<td></td>")
                continue
            i, e = hit
            cls = "bad" if e["bad"] else "ok"
            val = e["count"] if kind == "today" else "&bull;"
            out.append(f'<td class={cls} data-e="{i}" title="{esc(e["label"])}">{val}</td>')
        out.append("</tr>")
    out.append("</table>")
    return "".join(out)


def page(fname, title, intro, nodes, edges, details, steps, issues, kinds, extra=""):
    lay = layout(nodes, edges)
    W, H = lay["w"], lay["h"]
    svg = [f'<svg width="{W}" height="{H}" viewBox="0 0 {W} {H}"><defs>']
    for c, col in (("dep", "#0b7a84"), ("anc", "#bf650f"), ("bad", "#d2372b"), ("base", "#9aa3b0")):
        svg.append(f'<marker id="m-{c}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" fill="{col}"/></marker>')
    svg.append('</defs><g id=edges>')
    hits, counts = [], []
    for i, e in enumerate(edges):
        p = lay["paths"].get((e["from"], e["to"]))
        if not p:
            continue
        cls = "e" + (" bad" if e["bad"] else "")
        mk = "bad" if e["bad"] else "base"
        svg.append(f'<path id="e{i}" class="{cls}" d="{p["d"]}" marker-end="url(#m-{mk})"><title>{esc(e["label"])}</title></path>')
        hits.append(f'<path id="h{i}" class="e hit" d="{p["d"]}"><title>{esc(e["label"])}</title></path>')
        if e["kind"] == "today":
            counts.append(f'<span class=cnt id="c{i}" style="left:{p["mid"][0]}px;top:{p["mid"][1]}px">{e["count"]}</span>')
    svg.append("</g><g>" + "".join(hits) + "</g></svg>")
    boxes = []
    for n in nodes:
        b = lay["boxes"][n["id"]]
        cls = "n " + " ".join(n.get("cls", []))
        boxes.append(f'<div class="{cls}" data-id="{esc(n["id"])}" style="left:{b["x"]:.0f}px;top:{b["y"]:.0f}px;width:{b["w"]}px;height:{b["h"]}px"><span class=t>{esc(n["label"])}</span><span class=s>{esc(n["sub"])}</span></div>')
    toggles = ""
    if len(kinds) > 1:
        toggles = '<div class=grp><b>graph</b><span class=seg>' + "".join(
            f'<button data-kind="{k}"{" class=on" if j == 0 else ""}>{label}</button>' for j, (k, label) in enumerate(kinds)) + "</span></div>"
    graph = {
        "kinds": [k for k, _ in kinds],
        "w": W, "h": H,
        "nodes": {n["id"]: {"label": n["label"], "sub": n["sub"], "ext": "ext" in n.get("cls", []), "tags": n.get("tags", [])} for n in nodes},
        "edges": [{"from": e["from"], "to": e["to"], "kind": e["kind"], "count": e["count"], "bad": e["bad"], "sites": [list(s) for s in sorted(e.get("sites", []))]} for e in edges],
    }
    det = []
    for n in nodes:
        d = details.get(n["id"])
        if d:
            det.append(f'<details class=det id="detail-{esc(n["id"])}"><summary>{esc(n["label"])} <span class=meta>{esc(n["sub"])}</span>{tag_badges(n.get("tags", []))}</summary><div class=body>{d}</div></details>')
    parts = [
        "<!doctype html><html lang=en><head><meta charset=utf-8>",
        f"<meta name=viewport content='width=device-width,initial-scale=1'><title>{esc(title)}</title>",
        f"<style>{STYLE}</style></head><body><main>",
        f"<h1>{esc(title)}</h1><p class=meta>figaro {M.VERSION} ({M.BASE}). Non-test lines (wc -l). Generated by gen/gen.py; layered layout with {lay['crossings']} wire crossings.</p>",
        nav(fname),
        f"<p>{esc(intro)}</p>",
        f'<div class=tools>{toggles}<div class=grp><b>find</b><input id=find type=search placeholder="box name, Enter"></div>'
        '<div class=grp><b>zoom</b><span class=seg><button data-zoom="0.8">&minus;</button><button data-zoom="fit">fit</button><button data-zoom="1.25">+</button></span></div></div>',
        f"<div class=tools>{chips(steps, issues)}</div>",
        '<div class=legend><span><i class=dep></i>depends on</span><span><i class=anc></i>used by</span><span><i class=bad></i>wrong way</span><span>dashed box: optional module; dotted: new code; grey: outside this page</span></div>',
        '<div class=work><div class=viewport><div class=sizer>',
        f'<div class=canvas style="width:{W}px;height:{H}px">', "".join(svg), "".join(boxes), "".join(counts), "</div></div></div>",
        '<aside class=insp id=insp></aside></div>',
        HELP, cards(steps, issues),
        extra,
        f"<h2>Matrix</h2><p class=meta>Row imports column. Rows and columns follow the graph's layers, top to bottom, so a cell right of the diagonal points down; red cells point the wrong way.</p>{dsm(nodes, edges, lay, kinds[-1][0])}",
        "<h2>Boxes</h2>", "".join(det),
        f"<script type='application/json' id=graph>{json.dumps(graph)}</script><script>{SCRIPT}</script>",
        "</main></body></html>\n",
    ]
    return "\n".join(parts)
