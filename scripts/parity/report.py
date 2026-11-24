import argparse
import html
import json
import os
import shutil

ap = argparse.ArgumentParser()
ap.add_argument("out")
ap.add_argument("--web", help="directory holding <project>/<scenario>/<step>.png")
args = ap.parse_args()
out = os.path.abspath(args.out)
doc = json.load(open(os.path.join(out, "scenarios.json")))
results = {}
rpath = os.path.join(out, "pager", "results.json")
if os.path.exists(rpath):
    for r in json.load(open(rpath)):
        results[(r["scenario"], r["step"])] = r
projects = []
if args.web:
    for name in sorted(os.listdir(args.web)):
        if os.path.isdir(os.path.join(args.web, name)):
            projects.append(name)
            shutil.copytree(os.path.join(args.web, name), os.path.join(out, "web", name), dirs_exist_ok=True)
rows = []
for scenario in doc["scenarios"]:
    rows.append("<h2>%s <small>%s · %s</small></h2>" % (html.escape(scenario["id"]), html.escape(scenario["title"]), html.escape(scenario["slice"])))
    for step in scenario["steps"]:
        res = results.get((scenario["id"], step["id"]))
        badge = "pager n/a" if res is None else ("pager ok" if res["ok"] else "pager FAIL")
        txt = os.path.join(out, "pager", scenario["id"], step["id"] + ".txt")
        pane = html.escape(open(txt).read().rstrip()) if os.path.exists(txt) else "no capture"
        shots = ""
        for project in projects:
            rel = "web/%s/%s/%s.png" % (project, scenario["id"], step["id"])
            if os.path.exists(os.path.join(out, rel)):
                shots += '<figure><img src="%s"><figcaption>%s</figcaption></figure>' % (rel, html.escape(project))
            else:
                shots += "<figure><figcaption>%s: none</figcaption></figure>" % html.escape(project)
        probs = "" if not res or res["ok"] else "<pre class=bad>%s</pre>" % html.escape("\n".join(res["problems"]))
        rows.append('<section><h3>%s <span class=%s>%s</span></h3>%s<div class=pair><pre class=pane>%s</pre><div class=shots>%s</div></div></section>' % (html.escape(step["id"]), "ok" if res and res["ok"] else "bad", badge, probs, pane, shots))
page = """<!doctype html><meta charset=utf-8><title>parity</title><style>
body{font:14px system-ui;margin:1.5rem;background:#f6f1e8;color:#222}h2 small{font-weight:400;color:#666}
.pair{display:flex;gap:1rem;align-items:flex-start}.pane{background:#111;color:#ddd;padding:.6rem;font:12px/1.15 ui-monospace,monospace;overflow:auto;max-width:48rem;margin:0}
.shots{display:flex;gap:1rem;flex-wrap:wrap}.shots img{max-height:34rem;border:1px solid #bbb}.ok{color:#276}.bad{color:#a22}figure{margin:0}
</style><h1>pager and web, step by step</h1>%s""" % "\n".join(rows)
with open(os.path.join(out, "report.html"), "w") as fh:
    fh.write(page)
print(os.path.join(out, "report.html"))
