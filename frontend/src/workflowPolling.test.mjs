import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import ts from 'typescript';
const source=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
const start=source.indexOf('  const loadRun = useCallback(async (runId: string');const end=source.indexOf('\n  useEffect(',start);assert.ok(start>=0&&end>start);
function fixture(){
 const pending=[],details=[],busy=[],feedback=[];const context={useCallback:f=>f,activeRunIdRef:{current:'run'},activeWorkflowIdRef:{current:'workflow'},runRequestRef:{current:0},runLoadsInFlightRef:{current:new Set()},mutationRef:{current:''},project:{id:'project'},selectConversation:async()=>{},backend:()=>new Promise((resolve,reject)=>pending.push({resolve,reject})),normalizeWorkflowRunDetail:v=>v,setBusy:v=>busy.push(v),setRunDetail:v=>details.push(v),setProjectRuns:()=>{},setFeedback:v=>feedback.push(v),errorText:String};
 vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.loadRun=loadRun;',{}).outputText,context);
 return {context,pending,details,busy,feedback};
}
const snapshot={run:{id:'run',workflowId:'workflow'}};
test('polling cannot supersede manual refresh or strand load-run busy state',async()=>{
 const f=fixture();const manual=f.context.loadRun('run');await f.context.loadRun('run',true);assert.equal(f.pending.length,1);f.pending[0].resolve(snapshot);await manual;assert.deepEqual(f.busy,['load-run','']);assert.equal(f.details.length,1);
});
test('slow polls stay single-flight and their completed snapshots are applied',async()=>{
 const f=fixture();const poll=f.context.loadRun('run',true);for(let i=0;i<5;i++)await f.context.loadRun('run',true);assert.equal(f.pending.length,1);f.pending[0].resolve(snapshot);await poll;assert.equal(f.details.length,1);const next=f.context.loadRun('run',true);assert.equal(f.pending.length,2);f.pending[1].resolve(snapshot);await next;assert.equal(f.details.length,2);
});
test('manual refresh supersedes older poll without applying stale snapshot',async()=>{
 const f=fixture();const poll=f.context.loadRun('run',true);const manual=f.context.loadRun('run');f.pending[0].resolve(snapshot);await poll;assert.equal(f.details.length,0);f.pending[1].resolve(snapshot);await manual;assert.equal(f.details.length,1);assert.equal(f.busy.at(-1),'');
});
test('failed requests release single-flight guard and manual busy',async()=>{
 const f=fixture();const manual=f.context.loadRun('run');f.pending[0].reject(Error('offline'));await manual;assert.equal(f.context.runLoadsInFlightRef.current.size,0);assert.equal(f.busy.at(-1),'');assert.equal(f.feedback.length,1);
});
test('run switching discards former workflow response',async()=>{
 const f=fixture();const old=f.context.loadRun('run',true);f.context.activeWorkflowIdRef.current='next-workflow';f.pending[0].resolve(snapshot);await old;assert.equal(f.details.length,0);assert.equal(f.context.runLoadsInFlightRef.current.size,0);
});
test('mutating workflow defers background polls',async()=>{
 const f=fixture();f.context.mutationRef.current='retry:step';await f.context.loadRun('run',true);assert.equal(f.pending.length,0);
});
