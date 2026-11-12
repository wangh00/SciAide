import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';

const {ResearchRoute,researchRouteView}=loadTsx(new URL('./ResearchRoute.tsx',import.meta.url));
function detail(events=[]){return {run:{id:'run',researchStarterRunId:'plan',compilation:{nodes:[{id:'question',name:'明确问题'},{id:'method',name:'方法实现'},{id:'review',name:'独立审查'}]}},steps:[{id:'s0',nodeId:'question',ordinal:0,status:'completed',attempt:1},{id:'s1',nodeId:'method',ordinal:1,status:'queued',attempt:2},{id:'s2',nodeId:'review',ordinal:2,status:'queued',attempt:2}],events};}
const event=(type,payload,sequence=1)=>({id:`e${sequence}`,type,payload,sequence,createdAt:'2026-10-08T05:00:00Z'});
test('unrevised routes and ordinary retries have no history button',()=>{
 const d=detail([event('workflow.step_retried',{stepId:'s1'})]);
 const before=JSON.stringify(d),view=researchRouteView(d),html=renderToStaticMarkup(React.createElement(ResearchRoute,{detail:d,statusLabels:{}}));
 assert.equal(view.history.length,0);assert.equal(view.total,4);assert.equal(view.completed,2);
 assert.equal(view.currentSteps.length,3);assert.match(html,/AI 路线规划/);assert.doesNotMatch(html,/历史路线/);assert.equal(JSON.stringify(d),before);
});
test('only confirmed dialogue/review/manual repair events select the replay suffix',()=>{
 for(const e of [event('workflow.user_revision_queued',{startOrdinal:1,proposal:{nodeId:'method',summary:'采用新方法'}}),event('workflow.review_revision_queued',{producerStepId:'s1',producerNodeId:'method'}),event('workflow.upstream_revision_queued',{manual:true,producerStepId:'s1',producerNodeId:'method'})]){
  const d=detail([e]),before=JSON.stringify(d),view=researchRouteView(d);
  assert.equal(view.history.length,1);assert.equal(view.currentSteps.length,2);assert.equal(view.currentSteps[0].nodeId,'method');assert.equal(view.total,2);assert.equal(view.completed,0);assert.equal(view.planning,false);
  const html=renderToStaticMarkup(React.createElement(ResearchRoute,{detail:d,statusLabels:{queued:'等待'}}));
  assert.match(html,/历史路线 · 1/);assert.match(html,/本轮返修路线/);assert.match(html,/方法实现/);assert.match(html,/独立审查/);assert.doesNotMatch(html,/明确问题|AI 路线规划/);assert.equal(JSON.stringify(d),before);
 }
});
test('automatic repair/pending proposals/invalid or duplicate events do not invent rounds',()=>{
 const good=event('workflow.review_revision_queued',{producerStepId:'s1',producerNodeId:'method'},3);
 const d=detail([good,event('workflow.upstream_revision_queued',{automatic:true,producerStepId:'s1',producerNodeId:'method'},2),event('workflow.user_revision_queued',{startOrdinal:2,proposal:{nodeId:'method'}},4),event('workflow.revision_proposed',{proposal:{nodeId:'question'}},5),good]);
 const view=researchRouteView(d);assert.equal(view.rounds.length,2);assert.equal(view.current.start,1);
});
test('round order and current progress follow persistent sequence, not input order',()=>{
 const d=detail([event('workflow.user_revision_queued',{startOrdinal:2,proposal:{nodeId:'review',summary:'只重新审查'}},7),event('workflow.review_revision_queued',{producerStepId:'s1',producerNodeId:'method'},3)]);
 d.steps[2].status='completed';const view=researchRouteView(d);
 assert.equal(view.history.length,2);assert.equal(view.history[1].start,1);assert.equal(view.current.start,2);assert.equal(view.currentSteps.length,1);assert.equal(view.completed,1);assert.equal(view.total,1);
});
