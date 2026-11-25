import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';

const {ResearchStatePanel, ResearchStatePanelView, createResearchStateReader, researchStateRefreshIdentity} = loadTsx(new URL('./ResearchStatePanel.tsx', import.meta.url));
const app = fs.readFileSync(new URL('./App.tsx', import.meta.url), 'utf8');
const css = fs.readFileSync(new URL('./styles.css', import.meta.url), 'utf8');
const state = (projectId = 'p', researchTaskId = 'task', extra = {}) => ({
 id:'state', projectId, researchTaskId, version:1, currentFocus:'核对研究问题', rationale:'查明依据',
 nextStep:'比较来源', unknowns:['样本尚不清楚'], sourceChatRunId:'run', sourceUserMessageId:'message',
 sourceCallId:'call', createdAt:'2026-11-12T01:00:00Z', status:'working_note', ...extra,
});
const deferred = () => {let resolve, reject; const promise = new Promise((yes,no) => {resolve=yes; reject=no;}); return {promise, resolve, reject};};
const tick = () => new Promise(resolve => setImmediate(resolve));

test('actual state view distinguishes missing, loading, read errors and an unverified working note', () => {
 const render = (view, mode) => renderToStaticMarkup(React.createElement(ResearchStatePanelView, {view,mode,onRetry(){},onOpenDiscovery(){}}));
 assert.match(render({phase:'loading'}),/正在读取研究记录/);
 assert.match(render({phase:'empty'}),/尚未记录/);
 assert.match(render({phase:'error'}),/无法读取研究记录/);
 const html = render({phase:'ready',value:state()});
 for (const text of ['模型工作记录 · 未核验','不是已确认目标或科学结论','版本 1','核对研究问题','查明依据','样本尚不清楚','比较来源','下一步建议 · 未自动执行','对话运行：run','用户消息：message','工具调用：call','本任务文献发现']) assert.ok(html.includes(text), text);
 assert.doesNotMatch(html,/审查通过|已完成研究|已确认目标：/);
 assert.match(html,/保存时的题名\/摘要可回读/); assert.match(html,/不等于论文全文/);
 const partial = render({phase:'ready',value:state('p','task',{unknowns:null,currentFocus:'',createdAt:''})});
 assert.match(partial,/尚未记录/); assert.match(partial,/时间未记录/);
});

test('candidate planning is visibly distinct from discussion and remains non-executing', () => {
 const html = renderToStaticMarkup(React.createElement(ResearchStatePanelView, {view:{phase:'ready',value:state()},mode:'candidate',onRetry(){},onOpenDiscovery(){}}));
 assert.match(html,/候选路线协作状态/);
 assert.match(html,/候选规划 · 尚未采用 · 工作记录未核验/);
 assert.match(html,/候选路线尚未采用，不会自动执行/);
 assert.match(html,/候选协作工具/);
 assert.doesNotMatch(html,/研究讨论 · 模型工作记录/);
});

test('rendering or opening the discovery view never sends a message, invokes a model or starts a route', () => {
 const calls = [];
 const props = {projectId:'p',taskId:'task',service:(...args)=>{calls.push(args);return Promise.resolve(null)},refreshKey:'',onOpenDiscovery:()=>calls.push(['open'])};
 const html = renderToStaticMarkup(React.createElement(ResearchStatePanel, props));
 assert.match(html,/本任务文献发现/); assert.equal(calls.length,0);
 const tree = ResearchStatePanelView({view:{phase:'empty'},onRetry(){},onOpenDiscovery:props.onOpenDiscovery});
 function buttons(node){ if(!node || typeof node!=='object')return []; return [node.type==='button'?node:null,...React.Children.toArray(node.props?.children).flatMap(buttons)].filter(Boolean); }
 buttons(tree).find(item => item.props.children === '本任务文献发现').props.onClick();
 assert.deepEqual(calls,[['open']]);
 assert.match(app,/const \[researchOpen, setResearchOpen\] = useState<\{projectId: string; taskId: string\} \| null>/);
});

test('task reader reads only the scoped snapshot and refreshes coalesce without polling or model calls', async () => {
 const requests=[], events=[];
 const reader=createResearchStateReader({projectId:'p',taskId:'task',onChange:value=>events.push(value),service:(...args)=>{const d=deferred(); requests.push({args,...d}); return d.promise;}});
 const first=reader.refresh(); await reader.refresh(); await reader.refresh();
 assert.equal(requests.length,1); assert.deepEqual(requests[0].args,['WorkflowFacade','GetResearchWorkingState','p','task']);
 requests[0].resolve(state()); await first; await tick();
 assert.equal(requests.length,2); requests[1].resolve(state('p','task',{version:2})); await tick();
 assert.equal(requests.length,2); assert.equal(events.at(-1).value.version,2);
 reader.dispose(); await reader.refresh(); assert.equal(requests.length,2);
});

test('chat snapshot refresh identity follows this conversation and successful state tools only', () => {
 const run={id:'run',conversationId:'conversation',status:'running'};
 const pending={id:'call',toolName:'builtin.research.state.update',status:'running'};
 const complete={...pending,status:'completed'};
 const before=researchStateRefreshIdentity('conversation',run,[pending]);
 assert.equal(before,researchStateRefreshIdentity('conversation',run,[pending,{id:'x',toolName:'other',status:'completed'}]));
 assert.notEqual(before,researchStateRefreshIdentity('conversation',run,[complete]));
 assert.notEqual(before,researchStateRefreshIdentity('conversation',{...run,status:'completed'},[pending]));
 assert.equal(researchStateRefreshIdentity('other',run,[complete]),'');
 assert.equal(researchStateRefreshIdentity('',run,[complete]),'');
});

test('late state results cannot cross tasks or projects and wrong-scope snapshots fail closed', async () => {
 for (const replacement of [['p','other'],['other','task']]) {
  const old=deferred(), newer=deferred(), oldViews=[], newViews=[];
  const a=createResearchStateReader({projectId:'p',taskId:'task',onChange:v=>oldViews.push(v),service:()=>old.promise});
  const b=createResearchStateReader({projectId:replacement[0],taskId:replacement[1],onChange:v=>newViews.push(v),service:()=>newer.promise});
  const ra=a.refresh(); a.dispose(); const rb=b.refresh();
  newer.resolve(state(...replacement)); await rb; old.resolve(state()); await ra;
  assert.deepEqual(oldViews.map(v=>v.phase),['loading']); assert.equal(newViews.at(-1).value.researchTaskId,replacement[1]);
 }
 const views=[];
 const reader=createResearchStateReader({projectId:'p',taskId:'task',onChange:v=>views.push(v),service:async()=>state('p','wrong')});
 await reader.refresh(); assert.equal(views.at(-1).phase,'error');
 const empty=[]; const missing=createResearchStateReader({projectId:'p',taskId:'',onChange:v=>empty.push(v),service:()=>{throw Error('must not read')}});
 await missing.refresh(); assert.equal(empty.length,0);
});

const start=app.indexOf('  async function runSearch(event: FormEvent)',app.indexOf('function ResearchDiscovery('));
const end=app.indexOf('\n  async function updateReview(',start);
assert.ok(start>0 && end>start);
const handler=ts.transpileModule(app.slice(start,end)+'\nglobalThis.submitSearch=runSearch;',{}).outputText;
function searchFixture(taskId='', originFilter='all') {
 const d=deferred(), calls=[], updates=[];
 const setter=name=>value=>updates.push([name,typeof value==='function'?'update':value]);
 const context={project:{id:'p'}, taskId, originFilter, queryText:'example', selectedSources:['source'], discoveryBusy:false,
  discoveryScope:`p:${taskId}`, discoveryScopeRef:{current:`p:${taskId}`}, discoveryActiveRef:{current:true},
  candidateRequests:{current:{invalidate:()=>updates.push(['invalidate'])}}, first:values=>values[0], errorText:String,
  setSearching:setter('searching'),setFeedback:setter('feedback'),setOriginFilter:setter('origin'),setHistoryOffset:setter('offset'),
  setHistoryRevision:setter('revision'),setEvidenceCandidateId:setter('evidence'),setQueries:setter('queries'),setQueryId:setter('query'),
  setPage:setter('page'),setCandidateId:setter('candidate'),backend:(...args)=>{calls.push(args);return d.promise;}};
 vm.createContext(context);vm.runInContext(handler,context);
 const result=(projectId='p',resultTaskId=taskId)=>({query:{id:'q',projectId,researchTaskId:resultTaskId,sources:[],resultCount:1},page:{items:[{id:'candidate'}]}});
 return {context,calls,updates,...d,result,search:()=>context.submitSearch({preventDefault(){}})};
}

test('explicit task search is owned by the opening task, not the selected history; global search stays manual', async () => {
 for(const [taskId,origin] of [['task','another-task'],['','another-task']]) {
  const f=searchFixture(taskId,origin), work=f.search();
  assert.equal(f.calls.length,1); assert.equal(f.calls[0][0],'ResearchFacade'); assert.equal(f.calls[0][1],'Search');
  const command=f.calls[0][2]; assert.equal(command.projectId,'p');
  if(taskId) assert.equal(command.researchTaskId,taskId); else assert.equal('researchTaskId' in command,false);
  f.resolve(f.result()); await work;
  assert.deepEqual(f.updates.find(v=>v[0]==='origin'),['origin',taskId || 'manual']);
  assert.match(f.updates.findLast(v=>v[0]==='feedback')[1],/已保存 1 条/);
 }
});

test('incomplete search results never update the selected query or claim success', async () => {
 const f=searchFixture('task'), work=f.search(), result=f.result(); result.query.sources=null;
 f.resolve(result); await work;
 assert.equal(f.updates.some(v=>v[0]==='query'),false);
 assert.match(f.updates.findLast(v=>v[0]==='feedback')[1],/检索结果不完整/);
});

test('late search completion after changing task, project or unmount cannot update the visible discovery', async () => {
 for(const replacement of ['p:other','other:task','unmounted']) {
  const f=searchFixture('task'), work=f.search();
  if(replacement==='unmounted')f.context.discoveryActiveRef.current=false; else f.context.discoveryScopeRef.current=replacement;
  f.resolve(f.result()); await work;
  assert.equal(f.updates.filter(v=>['origin','query','candidate','page'].includes(v[0])).length,0);
  assert.equal(f.updates.filter(v=>v[0]==='feedback').length,1);
 }
});

test('failed or wrong-owner searches do not claim success', async () => {
 for(const mismatch of [false,true]) {
  const f=searchFixture('task'), work=f.search();
  if(mismatch) f.resolve(f.result('p','other')); else f.reject(Error('fixture offline'));
  await work; assert.equal(f.updates.some(v=>v[0]==='query'),false);
  assert.doesNotMatch(String(f.updates.findLast(v=>v[0]==='feedback')[1]),/已保存/);
  assert.deepEqual(f.updates.at(-1),['searching',false]);
 }
});

test('only the visible research task can open a scoped discovery without starting chat', () => {
 const begin=app.indexOf('  const openDiscoveryForTask = useCallback('), finish=app.indexOf('  const researchWorkingRefreshKey',begin);
 assert.ok(begin>0 && finish>begin);
 const calls=[], ctx={useCallback:fn=>fn,workspaceMode:'research',projectId:'p',selectedProjectIdRef:{current:'p'},researchTaskTimeline:{projectId:'p',taskId:'task'},setResearchOpen:value=>calls.push(value)};
 vm.createContext(ctx);vm.runInContext(ts.transpileModule(app.slice(begin,finish)+'\nglobalThis.open=openDiscoveryForTask;',{}).outputText,ctx);
 ctx.open('task'); assert.equal(calls.length,1); assert.equal(calls[0].taskId,'task'); assert.equal(calls[0].projectId,'p');
 ctx.open('other'); ctx.workspaceMode='chat'; ctx.open('task'); ctx.workspaceMode='research';ctx.selectedProjectIdRef.current='other';ctx.open('task');
 assert.equal(calls.length,1);
});

test('small windows retain an expandable state and workflow controls without a second chat', () => {
 assert.match(app,/className="research-mobile-toggle" aria-expanded=\{mobilePanelOpen\}/);
 assert.match(app,/className="research-mobile-close" aria-label="收起研究面板"/);
 assert.match(app,/onClick=\{\(\) => void runAction\("Cancel"\)\}/);
 assert.match(css,/@media \(max-width: 880px\)[\s\S]*?research-route-panel\.is-mobile-open[^}]*display: grid/);
 assert.equal((app.match(/<form className="composer"/g)||[]).length,1);
});

test('candidate and discussion runs use distinct research labels in the task panel', () => {
 assert.match(app,/researchStarter \? !researchDiscussionAvailable\(runDetail\) && workflowTerminal\(runDetail\.run\.status\) \? "历史只读" : discussionOnly \? "研究讨论" : "候选路线" : `\$\{completedSteps\}\/\$\{totalSteps\} 阶段完成`/);
 assert.match(app,/researchStarter \? discussionOnly \? "讨论记录" : "候选路线" : "执行记录"/);
 assert.match(app,/mode=\{discussionOnly \? "discussion" : "candidate"\}/);
});
