import test from 'node:test';
import assert from 'node:assert/strict';
import { projectedAt, projectionPoints, allowanceRange, trajectory } from '../../internal/api/dashboard/screens/burn.js';
import { defaultWindow, zoomWindow, panWindow, HOUR, DAY, WEEK, MIN_SPAN, MAX_SPAN } from '../../internal/api/dashboard/screens/viewport.js';
import { usageScope } from '../../internal/api/dashboard/screens/usage-picker.js';
import { S } from '../../internal/api/dashboard/core.js';
const now = Date.parse('2026-10-08T14:00:00Z');
const ser = { long:true, utilization:1, burn_per_hour:.01, burned:.24, burned_since:new Date(now-DAY).toISOString(), reset_at:new Date(now+7*HOUR).toISOString(), last_at:new Date(now).toISOString(), window_seconds:WEEK/1000 };
const tr = trajectory(ser,now);
test('one-week defaults and anchored zoom retain the point under the cursor',()=>{
 for(const kind of ['allowance','history']) {const v=defaultWindow(kind,now);assert.equal(v.end-v.start,WEEK);const z=zoomWindow(v,.25,.3);assert.equal(z.end-z.start,WEEK/4);assert.equal(v.start+WEEK*.3,z.start+(z.end-z.start)*.3);}
});
test('pan preserves duration and zoom stays bounded',()=>{const v=defaultWindow('allowance',now);const p=panWindow(v,.2);assert.equal(p.end-p.start,WEEK);assert.equal(p.start-v.start,WEEK*.2);assert.equal(zoomWindow(v,.00001).end-zoomWindow(v,.00001).start,MIN_SPAN);assert.equal(zoomWindow(v,1e5).end-zoomWindow(v,1e5).start,MAX_SPAN);});
test('a month forecast depletes after every refill and remains at zero until reset',()=>{
 assert.equal(projectedAt(tr,now,WEEK,tr.reset),100);
 assert.equal(projectedAt(tr,now,WEEK,tr.reset+24*HOUR),76);
 assert.equal(projectedAt(tr,now,WEEK,tr.reset+120*HOUR),0);
 assert.equal(projectedAt(tr,now,WEEK,tr.reset+WEEK),100);
 assert.equal(projectedAt(tr,now,WEEK,tr.reset+3*WEEK+24*HOUR),76);
 const points=projectionPoints(tr,now,now+31*DAY,WEEK,x=>x);
 assert.equal(points.filter((p,i)=>i&&p.x===points[i-1].x&&p.v===100).length,5);
 assert.ok(points.every(p=>p.v>=0&&p.v<=100));
});
test('range usage integrates burn across resets without subtracting refills',()=>{
 const r=allowanceRange(ser,tr,now,tr.reset+24*HOUR,tr.reset+WEEK+24*HOUR);
 assert.deepEqual(r,{used:100,resets:1});
 assert.deepEqual(allowanceRange(ser,tr,now,now,now+3*HOUR),{used:0,resets:0});
});
test('unknown rates and stale resets do not produce invented projections',()=>{
 assert.equal(projectedAt({...tr,rate:null},now,WEEK,now+DAY),null);
 assert.equal(projectedAt({...tr,reset:now-1},now,WEEK,now+DAY),null);
 assert.deepEqual(projectionPoints(tr,now,now-DAY,WEEK,x=>x),[]);
});
test('mixed-provider subsets and empty selections retain their exact scope',()=>{
 S.data={accounts:[{id:'a',provider:'claude',email:'a@x'},{id:'b',provider:'claude',email:'b@x'},{id:'c',provider:'codex',email:'c@x'}]};
 S.ui.usAccounts=['a','c'];assert.deepEqual(usageScope().ids,['a','c']);assert.equal(usageScope().some,true);
 S.ui.usAccounts=[];assert.deepEqual(usageScope().ids,[]);
 S.ui.usAccounts=null;assert.equal(usageScope().ids.length,3);
});
test('exhaustion uses observed history rather than forecasting permanently idle refills',()=>{
 const exhausted={...ser,burned:0,burn_per_hour:0,start:new Date(now-4*HOUR).toISOString(),step_seconds:3600,used:[600,800,1000,1000,1000]};
 const t=trajectory(exhausted,now);
 assert.equal(t.rate,10);
 assert.equal(projectedAt(t,now,WEEK,t.reset+5*HOUR),50);
 assert.equal(trajectory({...exhausted,used:[1000,1000,1000,1000,1000]},now).rate,0);
});

test('a response for an old visible window cannot overwrite a newer window', async()=>{
 const {fetchData,wantUsageViewport}=await import('../../internal/api/dashboard/core.js');
 globalThis.location={hash:'#/usage'};
 S.ui={usSection:'history',historyViewport:defaultWindow('history',now)};
 const pending=[];
 const oldFetch=globalThis.fetch;
 globalThis.fetch=(url)=>new Promise(resolve=>pending.push({url,resolve}));
 try {
  const older=fetchData();
  S.ui.historyViewport=panWindow(S.ui.historyViewport,-1);
  const expected=wantUsageViewport();
  const newer=fetchData();
  assert.match(pending[1].url,/usage_start=/);
  pending[1].resolve({ok:true,json:async()=>({tag:'newer'})}); await newer;
  pending[0].resolve({ok:true,json:async()=>({tag:'older'})}); await older;
  assert.equal(S.data.tag,'newer');assert.equal(S.dataViewport,expected);
 } finally {globalThis.fetch=oldFetch;delete globalThis.location;}
});
