import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import ts from 'typescript';
import {loadTsx} from './testTsxLoader.mjs';

const {researchDiscussionAvailable, researchDiscussionConversation, normalizeResearchDiscussionEntry, researchDiscussionEntryAvailable} = loadTsx(new URL('./researchDiscussion.ts', import.meta.url));
const {DirectResearchTaskView} = loadTsx(new URL('./DirectResearchTaskView.tsx', import.meta.url));
const source = fs.readFileSync(new URL('./App.tsx', import.meta.url), 'utf8');
const detail = (status = 'running') => ({run:{id:'planner',projectId:'p',researchTaskId:'task',conversationId:'planner-conv',status},researchDiscussion:{projectId:'p',researchTaskId:'task',conversationId:'planner-conv',primaryConversationId:'human-conv',sourceWorkflowRunId:'planner',canExplore:true}});

test('candidate planning states do not claim research execution or whole-task failure', () => {
  const start=source.indexOf('const workflowRunLabels:');
  const end=source.indexOf('\nconst workflowStepLabels:',start);
  assert.ok(start>0 && end>start);
  const context={};
  vm.runInNewContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.label=workflowRunStatusLabel;',{}).outputText,context);
  const expected={queued:'候选规划排队中',running:'候选规划中',failed:'候选规划失败',cancelled:'候选规划已取消',interrupted:'候选规划已中断',paused:'候选规划已暂停',waiting_approval:'候选规划等待授权',waiting_human_confirmation:'候选规划等待确认',completed:'路线规划已完成'};
  for (const [status,label] of Object.entries(expected)) assert.equal(context.label({workflowPurpose:'research_starter',status,inputs:{}}),label);
  assert.equal(context.label({workflowPurpose:'research_starter',status:'completed',inputs:{discussion_only:true}}),'科研探索');
  assert.equal(context.label({workflowPurpose:'user_plan',status:'running',inputs:{}}),'执行中');
  assert.equal(context.label({workflowPurpose:'user_plan',status:'failed',inputs:{}}),'失败');
  assert.equal(context.label({workflowPurpose:'user_plan',status:'completed',inputs:{},outputs:{research_design:{status:'research_design_not_empirical_result'}}}),'研究设计已完成');
});

test('direct task identity is explicit and retains ordered reference selections', () => {
  const value = {projectId:'p',researchTaskId:'task',conversationId:'human',title:'Question',researchQuestion:'Full question',modelProfileId:'profile',modelId:'model',referenceAttachmentIds:['b','a'],permissionMode:'full_access',reasoningLevel:'medium',createdAt:new Date().toISOString(),canExplore:true};
  const normalized = normalizeResearchDiscussionEntry(value);
  assert.deepEqual(Array.from(normalized.referenceAttachmentIds),['b','a']);
  assert.equal(researchDiscussionEntryAvailable(normalized),true);
  assert.equal(researchDiscussionEntryAvailable({...normalized,canExplore:false}),false);
  for (const patch of [{projectId:''},{conversationId:''},{canExplore:'true'},{referenceAttachmentIds:null},{referenceAttachmentIds:['']}]) {
    assert.throws(() => normalizeResearchDiscussionEntry({...value,...patch}));
  }
});

test('direct task view renders task ownership and read-only history without a synthetic Run', () => {
  const entry = normalizeResearchDiscussionEntry({projectId:'p',researchTaskId:'task',conversationId:'human',title:'合成问题',researchQuestion:'完整研究问题',modelProfileId:'profile',modelId:'model',referenceAttachmentIds:['a','b'],permissionMode:'full_access',reasoningLevel:'medium',createdAt:'2026-11-16T01:00:00Z',canExplore:true});
  const render = value => renderToStaticMarkup(React.createElement(DirectResearchTaskView,{entry:value,refreshKey:'',service:async()=>null,onBack(){},onOpenDiscovery(){},onOpenArtifacts(){}}));
  const active = render(entry);
  assert.match(active,/研究讨论 · 按需探索/);
  assert.match(active,/完整研究问题/);
  assert.match(active,/2 项参考文献/);
  assert.doesNotMatch(active,/>讨论记录|>执行记录/);
  const historical = render({...entry,canExplore:false});
  assert.match(historical,/历史任务 · 只读/);
  assert.match(historical,/当前任务为历史只读/);
  assert.doesNotMatch(historical,/研究讨论可继续/);
});

test('host discussion capability does not depend on optional planner lifecycle', () => {
  for (const status of ['queued','running','waiting_approval','failed','cancelled','interrupted','completed']) {
    const d = detail(status);
    assert.equal(researchDiscussionAvailable(d), true);
    assert.equal(researchDiscussionConversation(d), 'human-conv');
  }
});

test('missing, closed, foreign or malformed identities cannot unlock discussion', () => {
  for (const patch of [{canExplore:false},{canExplore:'true'},{projectId:'other'},{researchTaskId:'other'},{conversationId:'other'},{primaryConversationId:''},{primaryConversationId:null}]) {
    const d = detail(); Object.assign(d.researchDiscussion,patch);
    assert.equal(researchDiscussionAvailable(d),false);
    assert.equal(researchDiscussionConversation(d),'planner-conv');
  }
  assert.equal(researchDiscussionAvailable(null),false);
  assert.equal(researchDiscussionAvailable({run:detail().run}),false);
});

test('composer allows separate human discussion during planning, not formal execution or pending adoption', () => {
  const start=source.indexOf('  useEffect(() => {\n    const reviewConversation');
  const end=source.indexOf('\n  useEffect(',start+1);
  assert.ok(start>0 && end>start);
  const code=ts.transpileModule(source.slice(start,end),{}).outputText;
  const locked=(d,pending=null,step=null)=>{
    let result;
    const context={runDetail:d,directEntry:null,pendingAdoptedRoute:pending,pendingDecisionStep:step,researchDiscussionAvailable,researchDiscussionEntryAvailable,
      workflowTerminal:s=>['completed','failed','cancelled','interrupted'].includes(s),useEffect:f=>f(),publishComposerLocked:v=>result=v};
    vm.runInNewContext(code,context);return result;
  };
  assert.equal(locked(detail()),false);
  assert.equal(locked(detail(),{}),true);
  const formal=detail();formal.researchDiscussion.canExplore=false;
  assert.equal(locked(formal),true);
  assert.equal(locked({run:detail().run}),true);
  const historical=detail('completed'); historical.run.workflowPurpose='research_starter'; historical.researchDiscussion.canExplore=false;
  assert.equal(locked(historical),true);
  formal.run.status='waiting_human_confirmation';
  assert.equal(locked(formal,null,{nodeKind:'agent_stage'}),false);
});
