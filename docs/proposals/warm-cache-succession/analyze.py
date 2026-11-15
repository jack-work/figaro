import collections,csv,json,math,sqlite3,statistics,sys
from datetime import datetime,timezone
from pathlib import Path

OUT=Path(sys.argv[1])
OUT.mkdir(parents=True,exist_ok=True)
c=sqlite3.connect(f'file:{Path.home()}/.local/state/figscope/figscope.db?mode=ro',uri=True)
keys=['figaro.id','figaro.turn','provider','model','service.version','gen_ai.usage.input_tokens','gen_ai.usage.cache_read.input_tokens','gen_ai.usage.cache_creation.input_tokens','gen_ai.response.finish_reason','round.first_output_ms']
expr=','.join("json_extract(attrs,'$.\""+k+"\"')" for k in keys)
rows=c.execute('select start_ns,end_ns,error,host,'+expr+" from spans where name='figaro.round' and end_ns>start_ns and end_ns<=1794760718881021038 order by start_ns").fetchall()
OUT.joinpath('safe-rounds.json').write_text(json.dumps(rows)+'\n')
registry={r['id']:r for r in json.loads(Path(sys.argv[2]).read_text())} if len(sys.argv)>2 else {}
turns=collections.defaultdict(list)
for r in rows:
 if r[4] and r[5] is not None:turns[(r[3],r[4],str(r[5]))].append(r)
byseat=collections.defaultdict(list)
for (host,seat,turn),rr in turns.items():byseat[(host,seat)].append((rr[0][0],turn,rr))
pairs=[]
excluded=collections.Counter()
for (host,seat),tt in byseat.items():
 tt.sort()
 for a,b in zip(tt,tt[1:]):
  prev,cur=a[2][-1],b[2][0]
  if prev[2] or cur[2] or prev[12]!='stop':excluded['error_or_not_stop']+=1;continue
  if prev[6:9]!=cur[6:9]:excluded['provider_model_version_change']+=1;continue
  if not prev[8]:excluded['unversioned']+=1;continue
  pt=sum(x or 0 for x in prev[9:12]);ct=sum(x or 0 for x in cur[9:12])
  if not pt or not ct:excluded['zero_usage']+=1;continue
  gap=(cur[0]-prev[1])/1e9
  if gap<0:excluded['overlap']+=1;continue
  warm=(prev[10] or 0)/pt>=0.5
  broke=(cur[10] or 0)<0.5*pt
  stable=0.75<=ct/pt<=1.5
  pairs.append(dict(host=host,seat=seat,handle=registry.get(seat,{}).get('handle',''),turn=b[1],at=datetime.fromtimestamp(cur[0]/1e9,timezone.utc).isoformat(),provider=cur[6],model=cur[7],version=cur[8],gap_s=round(gap,3),request_gap_s=round((cur[0]-prev[0])/1e9,3),previous_total=pt,current_total=ct,previous_read=prev[10] or 0,current_read=cur[10] or 0,previous_warm=warm,break_after_warm=warm and broke,stable_size=stable,first_output_s=(cur[13] or 0)/1000,first_round_s=(cur[1]-cur[0])/1e9))
with (OUT/'pairs.csv').open('w') as f:
 w=csv.DictWriter(f,fieldnames=list(pairs[0]));w.writeheader();w.writerows(pairs)
def q(v,p):
 v=sorted(v)
 return round(v[max(0,math.ceil(p*len(v))-1)],2) if v else None
def stats(pp):
 warm=[p for p in pp if p['previous_warm'] and p['stable_size']]
 breaks=[p for p in warm if p['break_after_warm']]
 bands=[]
 for lo,hi in [(0,60),(60,180),(180,300),(300,600),(600,1800),(1800,3600),(3600,86400),(86400,float('inf'))]:
  bb=[p for p in warm if lo<=p['gap_s']<hi]
  bands.append(dict(lo_s=lo,hi_s=hi if math.isfinite(hi) else None,n=len(bb),breaks=sum(p['break_after_warm'] for p in bb)))
 return dict(pairs=len(pp),warm_stable_pairs=len(warm),breaks=len(breaks),break_gap_s={k:q([p['gap_s'] for p in breaks],v) for k,v in [('p10',.1),('p50',.5),('p90',.9)]},first_output_s={k:q([p['first_output_s'] for p in pp if p['first_output_s']>0],v) for k,v in [('p50',.5),('p95',.95),('p99',.99)]},first_round_s={k:q([p['first_round_s'] for p in pp],v) for k,v in [('p50',.5),('p95',.95),('p99',.99)]},bands=bands)
cohorts={
 'all_versioned':pairs,
 'anthropic':[p for p in pairs if p['provider']=='anthropic'],
 'gpt_0699':[p for p in pairs if p['version']=='0.69.9' and p['model']=='chatgpt-personal/gpt-6.1-sol'],
 'gpt_0696':[p for p in pairs if p['version']=='0.69.6' and p['model']=='chatgpt-personal/gpt-6.1-sol'],
 'gpt_0698':[p for p in pairs if p['version']=='0.69.8' and p['model']=='chatgpt-personal/gpt-6.1-sol'],
 'codex_temp':[p for p in pairs if p['provider']=='codex-temp'],
}
summary=dict(captured_at=datetime.now(timezone.utc).isoformat(),rounds=len(rows),turns=len(turns),excluded=dict(excluded),cohorts={k:stats(v) for k,v in cohorts.items()})
perseat=[]
for name,pp in cohorts.items():
 if name not in ['anthropic','gpt_0699']:continue
 for seat in sorted({p['seat'] for p in pp}):
  ss=[p for p in pp if p['seat']==seat]
  st=stats(ss)
  if st['warm_stable_pairs']:
   perseat.append(dict(cohort=name,seat=seat,handle=registry.get(seat,{}).get('handle',''),**st))
summary['per_seat']=sorted(perseat,key=lambda x:(x['cohort'],-x['breaks'],-x['warm_stable_pairs']))
(OUT/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
print(json.dumps({k:v for k,v in summary.items() if k!='per_seat'},indent=2))
print('TOP SEATS')
for p in summary['per_seat'][:12]:print(p['cohort'],p['seat'][:8],p['handle'],p['warm_stable_pairs'],p['breaks'],p['break_gap_s'])
print('GPT SEATS')
for p in summary['per_seat']:
 if p['cohort']=='gpt_0699':print(p['seat'][:8],p['handle'],p['warm_stable_pairs'],p['breaks'],p['break_gap_s'])
