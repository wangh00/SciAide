import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';

const panel = loadTsx(new URL('./ResearchStatePanel.tsx', import.meta.url));
const {createWorkingSourceReader, validWorkingSources} = loadTsx(new URL('./researchWorkingSources.tsx', import.meta.url));
const note = {id:'state-2',projectId:'p',researchTaskId:'task',version:2,status:'working_note',
 currentFocus:'原关注',rationale:'原理由',unknowns:['旧未知项'],nextStep:'新下一步',
 sourceChatRunId:'chat-2',sourceUserMessageId:'human-2',sourceCallId:'call-2',createdAt:'2026-11-15T02:00:00Z'};
const origin = (field, version) => ({field,status:'recorded',stateId:`state-${version}`,version,
 sourceChatRunId:`chat-${version}`,sourceUserMessageId:`human-${version}`,sourceCallId:`call-${version}`,createdAt:'2026-11-15T01:00:00Z'});
const provenance = (projectId='p', researchTaskId='task', version=2) => ({projectId,researchTaskId,version,stateId:`state-${version}`,status:'unverified_submission_provenance',
 fields:['currentFocus','rationale','unknowns'].map(field=>origin(field,1)).concat(origin('nextStep',2))});

test('sparse working-note source view distinguishes inherited fields from this submission', () => {
 const html=renderToStaticMarkup(React.createElement(panel.ResearchStatePanelView, {view:{phase:'ready',value:note},sourceView:{phase:'ready',value:provenance()},onRetry(){},onOpenDiscovery(){}}));
 assert.match(html,/本版提交与字段来源/);
 assert.match(html,/继承自版本 1/);
 assert.match(html,/本版显式提交/);
 assert.match(html,/字段来源不代表用户确认、证据支持或科学批准/);
 for(const text of ['chat-1','human-1','call-1','chat-2','human-2','call-2']) assert.ok(html.includes(text),text);
});

test('field provenance is loaded only on request and coalesces without writing or calling models', async () => {
 const calls=[],views=[];let resolve;
 const reader=createWorkingSourceReader({projectId:'p',taskId:'task',stateId:'state-2',version:2,onChange:v=>views.push(v),service:(...args)=>{calls.push(args);return new Promise(r=>resolve=r);}});
 assert.equal(calls.length,0);
 const first=reader.load();await reader.load();await reader.load();
 assert.deepEqual(calls,[['WorkflowFacade','GetResearchWorkingStateProvenance','p','task',2]]);
 resolve(provenance());await first;await reader.load();
 assert.equal(calls.length,1);assert.equal(views.at(-1).phase,'ready');
 reader.dispose();await reader.load();assert.equal(calls.length,1);
});

test('late provenance cannot cross task, project or working-note version', async () => {
 for (const [projectId,taskId,version] of [['other','task',2],['p','other',2],['p','task',3]]) {
  let resolve;const oldViews=[],newViews=[];
  const old=createWorkingSourceReader({projectId:'p',taskId:'task',stateId:'state-2',version:2,onChange:v=>oldViews.push(v),service:()=>new Promise(r=>resolve=r)});
  const work=old.load();old.dispose();
  const current=createWorkingSourceReader({projectId,taskId,stateId:`state-${version}`,version,onChange:v=>newViews.push(v),service:async()=>provenance(projectId,taskId,version)});
  await current.load();resolve(provenance());await work;
  assert.deepEqual(oldViews.map(v=>v.phase),['loading']);assert.equal(newViews.at(-1).value.version,version);assert.equal(newViews.at(-1).value.projectId,projectId);
 }
});

test('incomplete, foreign and forged field sources fail closed without falling back to last submission', async () => {
 const scope={projectId:'p',taskId:'task',stateId:'state-2',version:2};
 for(const change of [v=>v.projectId='other',v=>v.version=3,v=>v.stateId='old',v=>v.fields.pop(),v=>v.fields[0]=v.fields[1],v=>v.fields[0].field='approved',v=>v.fields[0].sourceCallId='',v=>v.fields[0].sourceCallId=42,v=>v.fields[0].version=3,v=>v.fields[0].status='approved',v=>v.fields[0].stateId='state-2',v=>v.fields[0].status='unavailable']) {
  const value=provenance();change(value);assert.equal(validWorkingSources(value,scope),false);
  const views=[];const reader=createWorkingSourceReader({...scope,onChange:v=>views.push(v),service:async()=>value});await reader.load();assert.equal(views.at(-1).phase,'error');
 }
 const good=provenance();good.fields[0]={field:'currentFocus',status:'unavailable'};good.fields[1]={field:'rationale',status:'not_recorded'};
 assert.equal(validWorkingSources(good,scope),true);
 const html=renderToStaticMarkup(React.createElement(panel.ResearchStatePanelView,{view:{phase:'ready',value:note},sourceView:{phase:'ready',value:good},onRetry(){},onOpenDiscovery(){}}));
 assert.match(html,/来源无法核对/);assert.match(html,/尚未显式记录/);
});

test('source read failures stay explicit and may retry without changing the note', async () => {
 let count=0;const views=[];
 const reader=createWorkingSourceReader({projectId:'p',taskId:'task',stateId:'state-2',version:2,onChange:v=>views.push(v),service:async()=>{if(++count===1)throw Error('fixture unavailable');return provenance();}});
 await reader.load();assert.equal(views.at(-1).phase,'error');await reader.load();assert.equal(views.at(-1).phase,'ready');assert.equal(count,2);
 const html=renderToStaticMarkup(React.createElement(panel.ResearchStatePanelView,{view:{phase:'ready',value:note},sourceView:{phase:'error'},onRetry(){},onOpenDiscovery(){}}));
 assert.match(html,/无法核对当前版本/);assert.match(html,/重试字段来源/);assert.match(html,/原理由/);
});
