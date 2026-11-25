import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';

const {createWorkingBasisReader,validWorkingBasis,WorkingBasisPage}=loadTsx(new URL('./researchWorkingBasis.tsx',import.meta.url));
const {validWorkingSources}=loadTsx(new URL('./researchWorkingSources.tsx',import.meta.url));
const basis=()=>({readCallId:'read-1',status:'linked_unverified_abstract',queryId:'q',candidateId:'c',sha256:'a'.repeat(64),offset:2,endOffset:4,totalRunes:8});
const scope=()=>({projectId:'p',taskId:'task',stateId:'state-2',version:2,field:'rationale',basis:basis()});
const result=()=>({projectId:'p',researchTaskId:'task',stateId:'state-2',version:2,field:'rationale',basis:basis(),content:'文🧪'});

test('explicit abstract relations read exact saved pages lazily, once, with no model/search/write',async()=>{
 const calls=[],views=[];let resolve;
 const reader=createWorkingBasisReader({...scope(),service:(...args)=>{calls.push(args);return new Promise(r=>resolve=r);},onChange:v=>views.push(v)});
 assert.equal(calls.length,0);const first=reader.load();await reader.load();
 assert.deepEqual(calls,[['WorkflowFacade','GetResearchWorkingStateBasis','p','task',2,'rationale','read-1']]);
 resolve(result());await first;await reader.load();assert.equal(calls.length,1);assert.equal(views.at(-1).value.content,'文🧪');
 reader.dispose();await reader.load();assert.equal(calls.length,1);
});

test('late saved-page responses cannot cross project, task, note version or field',async()=>{
 for(const change of [s=>s.projectId='other',s=>s.taskId='other',s=>{s.version=3;s.stateId='state-3';},s=>s.field='nextStep']){
  const oldViews=[],newViews=[];let resolve;
  const old=createWorkingBasisReader({...scope(),service:()=>new Promise(r=>resolve=r),onChange:v=>oldViews.push(v)});
  const pending=old.load();old.dispose();const next=scope();change(next);
  const current=createWorkingBasisReader({...next,service:async()=>({...result(),projectId:next.projectId,researchTaskId:next.taskId,stateId:next.stateId,version:next.version,field:next.field}),onChange:v=>newViews.push(v)});
  await current.load();resolve(result());await pending;
  assert.deepEqual(oldViews.map(v=>v.phase),['loading']);assert.equal(newViews.at(-1).phase,'ready');
 }
});

test('scope, receipt, range, hash and Unicode page length mismatches fail closed',async()=>{
 for(const change of [r=>r.projectId='other',r=>r.researchTaskId='other',r=>r.stateId='old',r=>r.version=3,r=>r.field='nextStep',r=>r.basis.readCallId='other',r=>r.basis.sha256='b'.repeat(64),r=>r.basis.offset=1,r=>r.basis.status='approved',r=>r.content='🧪',r=>r.content=null]){
  const value=result();change(value);const views=[];
  await createWorkingBasisReader({...scope(),service:async()=>value,onChange:v=>views.push(v)}).load();
  assert.equal(views.at(-1).phase,'error');
 }
});

test('missing historical pages do not fetch replacement or automatically retry',async()=>{
 let calls=0;const views=[];
 const reader=createWorkingBasisReader({...scope(),service:async()=>{if(++calls===1)throw Error('missing');return result();},onChange:v=>views.push(v)});
 await reader.load();assert.equal(calls,1);assert.equal(views.at(-1).phase,'error');
 await reader.load();assert.equal(calls,2);assert.equal(views.at(-1).phase,'ready');
 const missing={readCallId:'read-1',status:'unavailable',offset:0,endOffset:0,totalRunes:0};
 await createWorkingBasisReader({...scope(),basis:missing,service:async()=>{throw Error('must not fetch');},onChange(){throw Error('must not load');}}).load();
 assert.equal(validWorkingBasis(missing),true);
});

test('malformed or forged abstract relations cannot enter the field-source UI',()=>{
 for(const change of [b=>b.status='approved',b=>b.readCallId='',b=>b.readCallId=' read',b=>b.queryId='',b=>b.sha256='x',b=>b.offset=-1,b=>b.endOffset=20,b=>b.totalRunes=2,b=>b.endOffset=2]){const b=basis();change(b);assert.equal(validWorkingBasis(b),false);}
 const provenance={projectId:'p',researchTaskId:'task',stateId:'state-2',version:2,status:'unverified_submission_provenance',fields:['currentFocus','rationale','nextStep','unknowns'].map(field=>({field,status:'recorded',stateId:'state-2',version:2,sourceChatRunId:'chat',sourceUserMessageId:'human',sourceCallId:'call'}))};
 provenance.fields[1].basis=[basis()];assert.equal(validWorkingSources(provenance,scope()),true);
 provenance.fields[1].basis.push(basis());assert.equal(validWorkingSources(provenance,scope()),false);
});

test('page UI discloses model-selected unverified abstract, never approved support',()=>{
 const html=renderToStaticMarkup(React.createElement(WorkingBasisPage,{scope:scope(),service:async()=>result()}));
 assert.match(html,/模型关联的摘要页 · 未核验/);assert.match(html,/不是全文/);assert.match(html,/不代表支持该判断或科学批准/);assert.match(html,/不联网/);
 const missing=scope();missing.basis={readCallId:'read-1',status:'unavailable',offset:0,endOffset:0,totalRunes:0};
 const empty=renderToStaticMarkup(React.createElement(WorkingBasisPage,{scope:missing}));assert.match(empty,/原读取记录无法核对/);assert.doesNotMatch(empty,/<details/);
});
