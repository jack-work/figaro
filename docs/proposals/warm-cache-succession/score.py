import csv,json,math,statistics,sys
from collections import defaultdict
from datetime import datetime
sys.path.insert(0,sys.argv[2])
import pbreak
from pathlib import Path
OUT=Path(sys.argv[1])
rows=json.loads((OUT/'safe-rounds.json').read_text())
pairs=list(csv.DictReader((OUT/'pairs.csv').open()))
for p in pairs:
 for k in ['previous_warm','stable_size','break_after_warm']:p[k]=p[k]=='True'
 p['gap_s']=float(p['gap_s'])
outputs=defaultdict(list)
for r in rows:
 if r[4] and sum(x or 0 for x in r[9:12])>0:
  outputs[r[4]].append((r[1]/1e6,r[12],{'input_tokens':r[9] or 0,'cache_read_tokens':r[10] or 0,'cache_write_tokens':r[11] or 0}))
scored=[]
for p in pairs:
 if not (p['previous_warm'] and p['stable_size']):continue
 start=datetime.fromisoformat(p['at']).timestamp()*1000
 prevend=start-p['gap_s']*1000
 oo=[o for o in outputs[p['seat']] if o[0]<=prevend+1]
 f=pbreak.features(oo)
 if not f:continue
 scored.append({**{k:p[k] for k in ['seat','handle','provider','model','version','gap_s','break_after_warm']},**f,'p_break':pbreak.walk(f)})
print('tree replay from safe round-end timestamps (not original IR timestamps)')
for cohort,ss in [('anthropic',[s for s in scored if s['provider']=='anthropic']),('gpt_0699',[s for s in scored if s['model']=='chatgpt-personal/gpt-6.1-sol' and s['version']=='0.69.9'])]:
 n=len(ss)
 print(cohort,'n',n,'mean prediction',round(sum(s['p_break'] for s in ss)/max(1,n),4),'observed',round(sum(s['break_after_warm'] for s in ss)/max(1,n),4),'Brier',round(sum((s['p_break']-int(s['break_after_warm']))**2 for s in ss)/max(1,n),4))
 for prob in sorted({s['p_break'] for s in ss}):
  b=[s for s in ss if s['p_break']==prob]
  print(prob,len(b),sum(s['break_after_warm'] for s in b))
OUT.joinpath('tree-replay.json').write_text(json.dumps(scored,indent=2)+'\n')
