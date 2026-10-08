import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import ts from 'typescript';

const source=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
const start=source.indexOf('  async function send(event?: FormEvent)');
const end=source.indexOf('\n  async function attachDocuments',start);
assert.ok(start>0&&end>start);
const messageId='0d814b09-7687-4434-a453-24cbdbd2a911';
function fixture(){
  let resolve,reject;
  const pending=new Promise((yes,no)=>{resolve=yes;reject=no});
  const state={outgoing:[],input:'Follow-up question',busy:false,active:null,sending:false,attachments:[],notice:''};
  const calls=[],events=[];
  const setter=key=>value=>{state[key]=typeof value==='function'?value(state[key]):value};
  const context={webSearchEnabled:false,input:state.input,slashCommands:[],pendingAttachments:[],conversationId:'c',projectId:'p',profileId:'profile',modelId:'model',workspaceMode:'research',researchTaskTimeline:{projectId:'p',taskId:'task'},busy:false,researchConversationLocked:false,activeRun:null,activeRunIsWorkflowAI:false,selectedConversation:null,workspaceReasoningLevel:'medium',sendingRef:{current:false},conversationIdRef:{current:'c'},selectedProjectIdRef:{current:'p'},researchConversationIdRef:{current:'c'},autoFollowRef:{current:false},crypto:{randomUUID:()=>messageId},CustomEvent:class{constructor(type,options){this.type=type;this.detail=options.detail}},window:{dispatchEvent:e=>events.push(e)},completeContentReveals:()=>{},setOutgoingMessages:setter('outgoing'),setSending:setter('sending'),setInput:setter('input'),setPendingAttachments:setter('attachments'),setBusy:setter('busy'),setNotice:setter('notice'),setRetryStatus:()=>{},setActiveRun:setter('active'),setResearchConversation:()=>{},setConversations:()=>{},errorText:String,loadMessages:async()=>{throw Error('refresh unavailable')},backend:(facade,method,...args)=>{calls.push({facade,method,args});return method==='StartChat'?pending:Promise.reject(Error('refresh unavailable'));}};
  vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.sendMessage=send;',{}).outputText,context);
  return {state,context,calls,events,resolve:()=>resolve({id:'run',createdAt:'2026-09-11T12:00:00Z'}),reject,send:()=>context.sendMessage({preventDefault(){}})};
}
test('message appears before request resolves and repeated submit is coalesced',async()=>{
  const f=fixture(),work=f.send();
  assert.equal(f.state.input,'');assert.equal(f.state.outgoing.length,1);
  assert.equal(f.state.outgoing[0].message.status,'sending');assert.equal(f.state.outgoing[0].message.parts[0].text,'Follow-up question');
  assert.equal(f.calls[0].args[0].clientMessageId,messageId);
  await f.send();assert.equal(f.calls.length,1);
  f.resolve();await work;await Promise.resolve();
  assert.equal(f.state.outgoing[0].message.id,messageId);assert.equal(f.state.outgoing[0].message.status,'complete');
  assert.equal(f.events.at(-1).type,'research-message-saved');assert.equal(f.state.input,'');assert.equal(f.state.notice,'');assert.equal(f.state.sending,false);
});
test('failed submission retains visible failure and restores composer',async()=>{
  const f=fixture(),work=f.send();f.reject(Error('save failed'));await work;
  assert.equal(f.state.outgoing[0].message.status,'send_failed');assert.equal(f.state.input,'Follow-up question');assert.equal(f.state.busy,false);
  assert.match(f.state.notice,/save failed/);assert.equal(f.events.some(e=>e.type==='research-message-saved'),false);
});

test('direct repair has no second chat dispatch or composer injection',()=>{
 assert.doesNotMatch(source,/onDiscussRepair|discussResearchRepair|requestedText/);
 const retry=source.slice(source.indexOf('  async function retryStep('),source.indexOf('  async function confirmResearchRevision('));
 assert.match(retry,/"WorkflowFacade", "Retry"/);
 assert.doesNotMatch(retry,/StartChat|setInput|setPendingAttachments|ConfirmResearchRevision/);
 assert.match(retry,/\+\+runRequestRef.current/);
 assert.ok(retry.indexOf('await loadRun(')>retry.indexOf('finally'));
 assert.match(retry,/先确认采用的引用/);
});

test('late pre-retry poll cannot overwrite the new attempt during or after mutation',async()=>{
 const start=source.indexOf('  const loadRun = useCallback('),end=source.indexOf('\n  useEffect(() => {',start);
 assert.ok(start>0&&end>start);
 const fn=source.slice(start,end).replace('  const loadRun = useCallback(', 'const loadRun = ').replace(/, \[project.id, selectConversation\]\);\s*$/, ';');
 for(const during of [true,false]) {
  let finish;
  const response=new Promise(resolve=>{finish=resolve});
  const state={changes:[]};
  const context={activeRunIdRef:{current:'r'},activeWorkflowIdRef:{current:'w'},mutationRef:{current:''},runLoadsInFlightRef:{current:new Set()},runRequestRef:{current:0},project:{id:'p'},backend:()=>response,normalizeWorkflowRunDetail:v=>v,setRunDetail:v=>state.changes.push(v),setProjectRuns:()=>{},shareSnapshot:(_a,b)=>b,setBusy:()=>{},setFeedback:()=>{},errorText:String,selectConversation:async()=>{}};
  vm.createContext(context);vm.runInContext(ts.transpileModule(fn+'\nglobalThis.poll=loadRun;',{}).outputText,context);
  const work=context.poll('r',true);
  context.mutationRef.current='retry:gate';
  if(!during){context.runRequestRef.current++;context.mutationRef.current='';}
  finish({run:{id:'r',workflowId:'w',status:'failed'}});await work;
  assert.equal(state.changes.length,0);assert.equal(context.runLoadsInFlightRef.current.size,0);
 }
});

test('direct repair coalesces double clicks and refreshes only after the mutation unlocks',async()=>{
 const start=source.indexOf('  async function retryStep('),end=source.indexOf('  async function confirmResearchRevision(',start);
 let finish;const response=new Promise(resolve=>{finish=resolve});
 const calls=[],refreshes=[];
 const context={runDetail:{run:{id:'r',compilation:{nodes:[{id:'gate',tool:{qualifiedName:'builtin.research.workflow.review.gate'}}]}},reviewRevisionTargets:[{nodeId:'evidence_extraction',repeatsSideEffects:false}]},busy:'',confirmRetry:false,mutationRef:{current:''},runRequestRef:{current:0},project:{id:'p'},setBusy:()=>{},setFeedback:()=>{},setRunDetail:()=>{},setConfirmRetry:()=>{},loadProjectRuns:async()=>{},errorText:String,normalizeWorkflowRunDetail:v=>v,backend:(...args)=>{calls.push(args);return response},loadRun:async()=>refreshes.push(context.mutationRef.current)};
 vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.repair=retryStep;',{}).outputText,context);
 const args=[{id:'gate-step',nodeId:'gate'},'evidence_extraction',false,'current-review'];
 const work=context.repair(...args);await context.repair(...args);
 assert.equal(calls.length,1);assert.equal(refreshes.length,0);
 assert.equal(calls[0][0],'WorkflowFacade');assert.equal(calls[0][1],'Retry');
 assert.equal(calls[0][2].expectedReviewSha256,'current-review');assert.equal(calls[0][2].useRecommendation,false);
 finish({run:{id:'r',status:'queued'}});await work;
 assert.equal(context.runRequestRef.current,1);assert.deepEqual(refreshes,['']);
});

test('verified rejected tasks refresh pending proposals without reopening or executing the workflow',()=>{
 const start=source.indexOf('    if (!runDetail || !(runDetail.run.status === "completed"');
 const end=source.indexOf('\n  async function selectWorkflow',start);
 assert.ok(start>0&&end>start);
 const refresh=source.slice(start,end);
 assert.match(refresh,/runDetail\.run\.status === "failed" && runDetail\.discussionRevisionAvailable/);
 assert.match(refresh,/current\.run\.status === "failed" && current\.discussionRevisionAvailable/);
 assert.match(refresh,/"ListResearchRevisionProposals"/);
 assert.doesNotMatch(refresh,/"ConfirmResearchRevision"|"Retry"|"Start"/);
});
test('late send completion cannot select a run in another conversation',async()=>{
  for(const success of [true,false]){
    const f=fixture(),work=f.send();f.context.conversationIdRef.current='other';f.state.input='Other conversation draft';
    if(success)f.resolve();else f.reject(Error('late failure'));
    await work;
    assert.equal(f.state.input,'Other conversation draft');assert.equal(f.state.active,null);assert.equal(f.state.notice,'');
    assert.equal(f.state.outgoing[0].conversationId,'c');
  }
});

test('running or workflow-locked composer never sends or steers when Enter submits',async()=>{
 for(const flag of ['busy','researchConversationLocked']){const f=fixture();f.context[flag]=true;await f.send();assert.equal(f.calls.length,0);assert.equal(f.state.input,'Follow-up question');assert.equal(f.state.outgoing.length,0)}
});
