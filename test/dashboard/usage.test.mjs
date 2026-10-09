import test from 'node:test';
import assert from 'node:assert/strict';
import { projectedAt, projectionPoints, allowanceRange, trajectory } from '../../internal/api/dashboard/screens/burn.js';
import { defaultWindow, zoomWindow, panWindow, HOUR, DAY, WEEK, MIN_SPAN, MAX_SPAN } from '../../internal/api/dashboard/screens/viewport.js';
import { S, accountScope, setAccountSelection, fetchData } from '../../internal/api/dashboard/core.js';
// The dashboard keeps the account selection in localStorage; Node has none.
const store=new Map();
globalThis.localStorage={getItem:k=>store.has(k)?store.get(k):null,setItem:(k,v)=>store.set(k,String(v)),removeItem:k=>store.delete(k)};
const storedPick=()=>JSON.parse(store.get('cliproxy-dashboard-prefs')||'{}').accounts;
const ACCTS=[{id:'a',provider:'claude',email:'a@x'},{id:'b',provider:'claude',email:'b@x'},{id:'c',provider:'codex',email:'c@x'}];
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
test('mixed-provider and single-provider picks keep their exact scope',()=>{
 S.data={accounts:ACCTS};S.ui={};
 setAccountSelection(['a','c']);assert.deepEqual(accountScope().ids,['a','c']);assert.equal(accountScope().some,true);assert.equal(accountScope().prov,'all');
 setAccountSelection(['a','b']);assert.deepEqual(accountScope().ids,['a','b']);assert.equal(accountScope().prov,'claude');assert.equal(accountScope().some,false);
 setAccountSelection(['a']);assert.equal(accountScope().prov,'claude');assert.equal(accountScope().some,true);
 assert.deepEqual(storedPick(),['a']);
});
test('an empty pick falls back to all accounts',()=>{
 S.data={accounts:ACCTS};S.ui={};
 setAccountSelection(['c']);
 setAccountSelection([]);
 assert.equal(S.ui.accounts,null);assert.equal(storedPick(),undefined);
 const sc=accountScope();assert.equal(sc.ids.length,3);assert.equal(sc.prov,'all');assert.equal(sc.some,false);
});
test('no pick means every provider, even when only one provider has accounts left',()=>{
 // The last Codex account was removed, but its history and sessions remain.
 S.data={accounts:ACCTS.filter(a=>a.provider==='claude')};S.ui={accounts:null};
 const sc=accountScope();
 assert.equal(sc.prov,'all');assert.equal(sc.some,false);assert.deepEqual(sc.ids,['a','b']);
});
test('picked ids that fresh data no longer lists are pruned and stay gone',async()=>{
 S.data={accounts:ACCTS};S.ui={};
 setAccountSelection(['a','c']);
 const replies=[ACCTS.filter(a=>a.id!=='a'),ACCTS.filter(a=>a.id==='b'),ACCTS];
 const oldFetch=globalThis.fetch;
 globalThis.location={hash:'#/usage'};
 globalThis.fetch=async()=>({ok:true,json:async()=>({accounts:replies.shift()})});
 try {
  await fetchData();assert.deepEqual(S.ui.accounts,['c']);assert.deepEqual(storedPick(),['c']);
  await fetchData();assert.equal(S.ui.accounts,null);assert.equal(storedPick(),undefined);
  // Account a comes back: the pick does not silently return.
  await fetchData();assert.equal(S.ui.accounts,null);assert.equal(accountScope().ids.length,3);
 } finally {globalThis.fetch=oldFetch;delete globalThis.location;}
});
test('exhaustion uses observed history rather than forecasting permanently idle refills',()=>{
 const exhausted={...ser,burned:0,burn_per_hour:0,start:new Date(now-4*HOUR).toISOString(),step_seconds:3600,used:[600,800,1000,1000,1000]};
 const t=trajectory(exhausted,now);
 assert.equal(t.rate,10);
 assert.equal(projectedAt(t,now,WEEK,t.reset+5*HOUR),50);
 assert.equal(trajectory({...exhausted,used:[1000,1000,1000,1000,1000]},now).rate,null);
 assert.equal(trajectory({...exhausted,utilization:.5,used:[500,500,500,500,500]},now).rate,0);
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


test('known reset clocks are counted even when future demand is zero or unknown',()=>{
 const from=tr.reset-2*HOUR,to=tr.reset+WEEK+HOUR;
 assert.deepEqual(allowanceRange(ser,{...tr,rate:0},now,from,to),{used:0,resets:2});
 assert.deepEqual(allowanceRange(ser,{...tr,rate:null},now,from,to),{used:null,resets:2});
});


test('unknown burn still draws the known refill without a fabricated decline',()=>{
 const unknown={...tr,rate:null,leftNow:0};
 assert.equal(projectedAt(unknown,now,WEEK,tr.reset-HOUR),0);
 assert.equal(projectedAt(unknown,now,WEEK,tr.reset),100);
 assert.equal(projectedAt(unknown,now,WEEK,tr.reset+HOUR),null);
 const points=projectionPoints(unknown,now,tr.reset+2*WEEK,WEEK,t=>t);
 assert.deepEqual(points.slice(0,3),[{x:now,v:0},{x:tr.reset,v:0},{x:tr.reset,v:100,move:false,marker:true}]);
 assert.equal(points.filter(p=>p.marker).length,3);
 assert.ok(points.slice(3).every(p=>p.move&&p.marker&&p.v===100));
 const partial=projectionPoints({...unknown,leftNow:50},now,tr.reset+HOUR,WEEK,t=>t);
 assert.deepEqual(partial,[{x:tr.reset,v:100,move:true,marker:true}]);
});

test('chart renderer keeps unknown intervals disconnected and reset points visible',async()=>{
 const {timeChart}=await import('../../internal/api/dashboard/core.js');
 const html=timeChart({id:'reset-test',format:'line',cols:[{values:{}},{values:{}}],series:[{key:'a',color:'#123456',proj:[{x:0,v:0},{x:.25,v:0},{x:.25,v:100,marker:true},{x:.75,v:100,move:true,marker:true}]}],height:100,max:100});
 assert.match(html,/M0.00 100.00 L250.00 100.00 L250.00 0.00 M750.00 0.00/);
 assert.equal((html.match(/class="pt"/g)||[]).length,2);
});
