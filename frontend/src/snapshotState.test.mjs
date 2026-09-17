import assert from 'node:assert/strict';
import test from 'node:test';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import {shareSnapshot, singleFlight, conditionalViews} from './snapshotState.js';
import {mergeResearchTimeline} from './researchTimelineState.js';

test('field patches preserve static history, remove absent fields and reject missing baselines',async()=>{
 const read=conditionalViews(2),initial={steps:[{code:'original'}],elapsed:1,error:'old'};
 const first=await read('a',async()=>({revision:'v1',value:initial}));
 const next=await read('a',async()=>({revision:'v2',patch:{elapsed:2},removed:['error']}));
 assert.equal(next.steps,first.steps);assert.equal(next.elapsed,2);assert.equal(Object.hasOwn(next,'error'),false);assert.equal(first.error,'old');
 await assert.rejects(read('missing',async()=>({revision:'v',patch:{elapsed:3}})),/baseline/);
 assert.equal(await read('a',async()=>({revision:'null',value:null})),null);
 const newRun=await read('a',async()=>({revision:'new',value:{id:'new'}}));assert.equal(newRun.id,'new');
});

test('unchanged snapshots retain object identity, changed branches remain visible', () => {
  const old={steps:[{id:'s',text:'x'.repeat(100000)}],status:'running'};
  assert.equal(shareSnapshot(old,structuredClone(old)),old);
  const next=shareSnapshot(old,{...structuredClone(old),status:'completed'});
  assert.equal(next.steps,old.steps);assert.equal(next.status,'completed');assert.equal(old.status,'running');
  const missing={a:undefined};
  assert.notEqual(shareSnapshot(missing,{b:undefined}),missing);
  const unsafe=JSON.parse('{"__proto__":{"polluted":true}}');
  const safe=shareSnapshot({},unsafe);assert.equal(Object.getPrototypeOf(safe),null);assert.equal({}.polluted,undefined);assert.equal(safe.__proto__.polluted,true);
});
test('timeline refresh does not replace unchanged entries or array',()=>{
  const old=[{id:'m',sequence:'1',message:{text:'same',status:'streaming'}}];
  assert.equal(mergeResearchTimeline(old,structuredClone(old)),old);
  const next=mergeResearchTimeline(old,[{id:'m',sequence:'1',message:{text:'done',status:'complete'}}]);
  assert.notEqual(next,old);assert.equal(next[0].message.text,'done');
});
test('simultaneous reads share one request and release after rejection',async()=>{
  const read=singleFlight();let count=0,resolve;
  const load=()=>{count++;return new Promise(r=>resolve=r)};
  const a=read('a',load),b=read('a',load);await Promise.resolve();assert.equal(a,b);assert.equal(count,1);resolve(3);assert.equal(await a,3);
  await assert.rejects(read('a',async()=>{throw Error('failed')}));assert.equal(await read('a',async()=>4),4);
});
test('conditional views retain baselines, update changed data and evict bounded history',async()=>{
  const read=conditionalViews(2);const calls=[];
  const value={text:'exact code',status:'running'};
  const first=await read('a',async rev=>{calls.push(rev);return{revision:'v1',value}});
  const same=await read('a',async rev=>{calls.push(rev);return{revision:'v1',unchanged:true}});
  assert.equal(first,same);assert.deepEqual(calls,['','v1']);
  await read('b',async()=>({revision:'v2',value:{}}));await read('c',async()=>({revision:'v3',value:{}}));
  await read('a',async rev=>{assert.equal(rev,'');return{revision:'v4',value:{text:'new'}}});
});
test('conditional views do not treat an errored read as an unchanged success',async()=>{
  const read=conditionalViews(2);
  await read('a',async()=>({revision:'v1',value:{status:'running'}}));
  await assert.rejects(read('a',async()=>{throw Error('scope rejected')}));
  const next=await read('a',async revision=>{assert.equal(revision,'v1');return{revision:'v2',value:{status:'failed'}}});
  assert.equal(next.status,'failed');
});

function backendFixture(methods) {
  const source=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
  const start=source.indexOf('const readFlight = singleFlight();');
  const end=source.indexOf('\nfunction errorText',start);
  assert.ok(start>=0&&end>start);
  const context=vm.createContext({singleFlight,conditionalViews,window:{go:{wails:{ChatFacade:methods}}}});
  vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.readBackend=backend;',{}).outputText,context);
  return (method,...args)=>context.readBackend('ChatFacade',method,...args);
}

test('backend uses conditional polling and keeps unchanged snapshot identity',async()=>{
  let legacyCalls=0;const revisions=[];
  const read=backendFixture({GetRunSnapshot:async()=>{legacyCalls++;},PollRunSnapshot:async(id,revision)=>{
    assert.equal(id,'run');revisions.push(revision);
    return revision?{revision,unchanged:true}:{revision:'v1',value:{run:{id}}};
  }});
  const first=await read('GetRunSnapshot','run');
  assert.equal(await read('GetRunSnapshot','run'),first);
  assert.deepEqual(revisions,['','v1']);assert.equal(legacyCalls,0);
});

test('read started during mutation is not reused by post-mutation refresh',async()=>{
  let completeMutation;const pending=[];
  const read=backendFixture({StartChat:()=>new Promise(resolve=>{completeMutation=resolve}),GetRunSnapshot:async()=>{},PollRunSnapshot:()=>new Promise(resolve=>pending.push(resolve))});
  const mutation=read('StartChat');
  const during=read('GetRunSnapshot','run');await Promise.resolve();assert.equal(pending.length,1);
  completeMutation({});await mutation;
  const after=read('GetRunSnapshot','run');await Promise.resolve();assert.equal(pending.length,2);
  pending[1]({revision:'new',value:{status:'running'}});assert.equal((await after).status,'running');
  pending[0]({revision:'old',value:{status:'completed'}});await during;
  const latest=read('GetRunSnapshot','run');await Promise.resolve();
  pending[2]({revision:'new',unchanged:true});assert.equal((await latest).status,'running');
});

test('legacy backend still coalesces concurrent reads without caching settled data',async()=>{
  let calls=0,complete;
  const read=backendFixture({GetRunSnapshot:()=>{calls++;return new Promise(resolve=>{complete=resolve})}});
  const first=read('GetRunSnapshot','run'),second=read('GetRunSnapshot','run');await Promise.resolve();
  assert.equal(first,second);assert.equal(calls,1);complete({});await first;
  const next=read('GetRunSnapshot','run');await Promise.resolve();assert.equal(calls,2);complete({});await next;
});
