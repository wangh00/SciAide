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
  const context={pendingQuestionKey:"",userInputRequest:null,userInputSubmitting:false,researchOpening:null,attachmentImportRef:{current:false},attachmentDraftScopeRef:{current:"p:research:c"},first:values=>values[0],importingAttachments:false,webSearchEnabled:false,input:state.input,slashCommands:[],pendingAttachments:[],conversationId:'c',projectId:'p',profileId:'profile',modelId:'model',workspaceMode:'research',researchTaskTimeline:{projectId:'p',taskId:'task'},busy:false,researchConversationLocked:false,activeRun:null,activeRunIsWorkflowAI:false,selectedConversation:null,workspaceReasoningLevel:'medium',sendingRef:{current:false},conversationIdRef:{current:'c'},selectedProjectIdRef:{current:'p'},researchConversationIdRef:{current:'c'},autoFollowRef:{current:false},crypto:{randomUUID:()=>messageId},CustomEvent:class{constructor(type,options){this.type=type;this.detail=options.detail}},window:{dispatchEvent:e=>events.push(e)},completeContentReveals:()=>{},setOutgoingMessages:setter('outgoing'),setSending:setter('sending'),setInput:setter('input'),setPendingAttachments:setter('attachments'),setBusy:setter('busy'),setNotice:setter('notice'),setRetryStatus:()=>{},setActiveRun:setter('active'),setResearchConversation:()=>{},setConversations:()=>{},errorText:String,loadMessages:async()=>{throw Error('refresh unavailable')},backend:(facade,method,...args)=>{calls.push({facade,method,args});return method==='StartChat'?pending:Promise.reject(Error('refresh unavailable'));}};
  context.cancellationRef={current:new Set()};
  vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.sendMessage=send;',{}).outputText,context);
  return {state,context,calls,events,resolve:()=>resolve({id:'run',createdAt:'2026-09-11T12:00:00Z'}),reject,send:()=>context.sendMessage({preventDefault(){}})};
}

test('review has no intermediate check chat event, special send mode or generated prompt',()=>{
 assert.doesNotMatch(source,/research-check-review|reviewCheck\?:|核对本轮待处理事项|<ResearchRevisionControls onDiscuss=/);
});

test('workflow review question answer resumes through shared question boundary, not StartChat',async()=>{
 const f=fixture();f.context.pendingQuestionKey='c:q';f.context.userInputRequest={questions:[{id:'scope'}]};
 f.context.userInputIndexesRef={current:new Map()};f.context.userInputAnswers={};f.context.busy=true;f.context.researchConversationLocked=true;
 f.context.updateUserInputAnswers=()=>{};f.context.focusUserInputCard=()=>{};
 const answers=[];f.context.submitUserInput=async value=>{answers.push(value);return true};
 await f.send();assert.equal(f.calls.length,0);assert.equal(answers.length,1);assert.equal(answers[0].scope.text,'Follow-up question');assert.equal(f.state.input,'');
});
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


test('first research turn creates only an owner and submits once through normal chat', async()=>{
  const f=fixture();f.context.conversationId='';f.context.conversationIdRef.current='';
  let opens=0,finish;
  f.context.researchOpening=()=>{opens++;return new Promise(resolve=>{finish=resolve;});};
  const work=f.send();await f.send();assert.equal(opens,1);assert.equal(f.calls.length,0);
  f.context.conversationIdRef.current='opening';finish({conversationId:'opening',taskId:'opening-task'});
  await Promise.resolve();await Promise.resolve();
  assert.equal(f.calls[0].method,'StartChat');assert.equal(f.calls[0].args[0].conversationId,'opening');
  assert.equal(f.state.outgoing[0].taskId,'opening-task');f.resolve();await work;
  assert.equal(opens,1);assert.equal(f.state.input,'');
});

test('failed opening preserves typed requirements without dispatching chat',async()=>{
  const f=fixture();f.context.conversationId='';f.context.conversationIdRef.current='';
  f.context.researchOpening=async()=>{throw Error('opening unavailable');};await f.send();
  assert.equal(f.calls.length,0);assert.equal(f.state.input,'Follow-up question');assert.equal(f.state.sending,false);
  assert.match(f.state.notice,/opening unavailable/);
});

test('entry file import occurs only after explicit send and uses the created owner',async()=>{
 const f=fixture();f.context.pendingAttachments=[{id:'draft:file',originalName:'file.txt',draftPath:'D:/file.txt'}];
 f.context.researchOpening=async()=>{f.context.conversationIdRef.current='opening';return {conversationId:'opening',taskId:'task'}};
 const backend=f.context.backend;
 f.context.backend=(facade,method,...args)=>method==='ImportConversationDocumentPaths'?Promise.resolve({attachments:[{id:'file',originalName:'file.txt',status:'ready'}],errors:[]}):backend(facade,method,...args);
 const work=f.send();await new Promise(resolve=>setTimeout(resolve,0));
 assert.deepEqual(Array.from(f.calls[0].args[0].attachmentIds),['file']);
 assert.equal(f.calls[0].args[0].conversationId,'opening');
 f.resolve();await work;assert.equal(f.state.attachments.length,0);
});

test('partial entry import keeps ready files and failed paths without sending an incomplete message',async()=>{
 const f=fixture();f.context.pendingAttachments=[{id:'draft:a',originalName:'a.txt',draftPath:'D:/a.txt'},{id:'draft:b',originalName:'b.txt',draftPath:'D:/b.txt'}];
 f.context.researchOpening=async()=>{f.context.conversationIdRef.current='opening';return {conversationId:'opening',taskId:'task'}};
 f.context.backend=async()=>({attachments:[{id:'a',originalName:'a.txt',status:'ready'}],errors:[{path:'D:/b.txt',message:'unreadable'}]});
 await f.send();assert.equal(f.state.sending,false);assert.equal(f.state.outgoing.length,0);
 assert.equal(f.state.attachments[0].id,'a');assert.equal(f.state.attachments[1].draftPath,'D:/b.txt');
 assert.equal(f.state.input,'Follow-up question');assert.match(f.state.notice,/消息尚未发送/);
});

test('failed entry import restores the draft instead of calling StartChat',async()=>{
 const f=fixture();f.context.pendingAttachments=[{id:'draft:a',originalName:'a.txt',draftPath:'D:/a.txt'}];
 f.context.researchOpening=async()=>{f.context.conversationIdRef.current='opening';return {conversationId:'opening',taskId:'task'}};
 f.context.backend=async()=>{throw Error('import unavailable')};await f.send();
 assert.equal(f.state.attachments[0].draftPath,'D:/a.txt');assert.equal(f.state.outgoing.length,0);
 assert.equal(f.state.input,'Follow-up question');assert.match(f.state.notice,/import unavailable/);
});

test('switching projects while importing cannot append or send to the new project',async()=>{
 const f=fixture();f.context.pendingAttachments=[{id:'draft:a',originalName:'a.txt',draftPath:'D:/a.txt'}];
 let finish;
 f.context.backend=()=>new Promise(resolve=>{finish=resolve});
 const work=f.send();f.context.selectedProjectIdRef.current='other';f.state.attachments=[];
 finish({attachments:[{id:'file',status:'ready'}],errors:[]});await work;
 assert.equal(f.state.attachments.length,0);assert.equal(f.state.outgoing.length,0);assert.equal(f.state.notice,'');
});

test('attachment handlers never open tasks and the entry chooser does not import materials',()=>{
 const attach=source.slice(source.indexOf('  async function attachDocuments'),source.indexOf('  async function readSlashSkills'));
 assert.doesNotMatch(attach,/await researchOpening|StartResearch|ChooseAndImportDocuments|"ImportDocumentPaths"/);
 assert.match(attach,/"ChooseConversationDocumentPaths"/);
 assert.match(attach,/attachmentImportRef.current = true/);
 assert.match(attach,/attachmentDraftScopeRef.current !== scope/);
});

function attachmentFixture() {
 const state={attachments:[],notice:'',importing:false},calls=[];
 let finish;
 const response=new Promise(resolve=>{finish=resolve});
 const setter=key=>value=>{state[key]=typeof value==='function'?value(state[key]):value};
 const context={projectId:'p',workspaceMode:'research',researchOpening:()=>{throw Error('upload must not open a task')},conversationId:'',conversationIdRef:{current:''},researchConversationIdRef:{current:''},pendingAttachments:[],pendingAttachmentsRef:{current:[]},sendingRef:{current:false},attachmentImportRef:{current:false},attachmentDraftScopeRef:{current:'p:research:entry'},researchConversationLocked:false,busy:false,activeRunIsWorkflowAI:false,first:values=>values[0],errorText:String,setPendingAttachments:setter('attachments'),setImportingAttachments:setter('importing'),setNotice:setter('notice'),backend:(facade,method,...args)=>{calls.push({facade,method,args});return response}};
 const start=source.indexOf('  async function attachDocuments'),end=source.indexOf('  async function readSlashSkills',start);
 vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.attach=attachDocuments;globalThis.drop=importDroppedDocuments;',{compilerOptions:{target:ts.ScriptTarget.ES2020}}).outputText,context);
 return {state,calls,context,finish,attach:()=>context.attach(),drop:paths=>context.drop(paths)};
}

test('double chooser clicks and concurrent drops are coalesced without opening a task',async()=>{
 const f=attachmentFixture(),work=f.attach();await f.attach();await f.drop(['D:/other.txt']);
 assert.equal(f.calls.length,1);assert.equal(f.calls[0].method,'ChooseConversationDocumentPaths');
 f.finish(['D:/file.txt']);await work;assert.equal(f.state.attachments[0].draftPath,'D:/file.txt');
 assert.equal(f.state.importing,false);
});

test('cancelled chooser creates neither attachments nor a task',async()=>{
 const f=attachmentFixture(),work=f.attach();f.finish([]);await work;
 assert.equal(f.calls.length,1);assert.equal(f.state.attachments.length,0);assert.equal(f.state.notice,'');
});

test('late file choice after switching scope is ignored',async()=>{
 const f=attachmentFixture(),work=f.attach();f.context.attachmentDraftScopeRef.current='other:research:entry';
 f.finish(['D:/file.txt']);await work;assert.equal(f.state.attachments.length,0);assert.equal(f.state.notice,'');
});

test('entry drop deduplicates paths, respects existing limit and never calls a facade',async()=>{
 const f=attachmentFixture();await f.drop(['D:\\folder\\file.txt','D:\\folder\\file.txt']);
 assert.equal(f.calls.length,0);assert.equal(f.state.attachments.length,1);assert.equal(f.state.attachments[0].originalName,'file.txt');
 f.context.pendingAttachments=f.state.attachments;
 await f.drop(['D:\\folder\\file.txt']);assert.equal(f.state.attachments.length,1);
 await f.drop(Array.from({length:20},(_,i)=>`D:/file-${i}.txt`));
 assert.equal(f.state.attachments.length,1);assert.match(f.state.notice,/20/);
});

test('existing discussion uploads still import into its owner, not into the project library',async()=>{
 const f=attachmentFixture();f.context.researchOpening=null;f.context.conversationId='c';
 f.context.conversationIdRef.current='c';f.context.researchConversationIdRef.current='c';f.context.attachmentDraftScopeRef.current='p:research:c';
 const work=f.attach();assert.equal(f.calls[0].method,'ChooseAndImportConversationDocuments');
 assert.deepEqual(f.calls[0].args,['p','c']);
 f.finish({attachments:[{id:'ready',status:'ready'}],errors:[]});await work;
 assert.equal(f.state.attachments[0].id,'ready');
});

test('opening send uses workspace reasoning instead of the old chat override',async()=>{
  const f=fixture();f.context.selectedConversation={reasoningLevel:'max'};
  f.context.workspaceReasoningLevel='low';f.context.researchOpening=async()=>{f.context.conversationIdRef.current='opening';return {conversationId:'opening',taskId:'task'}};
  const work=f.send();await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(f.calls[0].args[0].reasoningLevel,'low');f.resolve();await work;
});

test('automatic candidate follow waits for pending files and in-flight imports',async()=>{
  assert.match(source,/conversationBusy=\{busy \|\| sending \|\| importingAttachments \|\| pendingAttachments.length > 0\}/);
  const start=source.indexOf('  // A normal chat may prepare the next immutable planner.');
  const end=source.indexOf('\n  useEffect(() => {',source.indexOf('  useEffect(() => {',start)+1);
  for(const blocked of [true,false]) {
    let refresh;const selected=[],calls=[];
    const context={runDetail:{run:{id:'old',researchTaskId:'task',workflowPurpose:'research_starter',status:'completed',conversationId:'old-c',inputs:{}}},project:{id:'p'},conversationBusy:blocked,mutationRef:{current:''},activeRunIdRef:{current:'old'},activeWorkflowIdRef:{current:'old-w'},runRequestRef:{current:0},setDetail:()=>{},setRunDetail:()=>{},setStarterPlan:()=>{},setPlanningDiscussionVersion:()=>{},shareSnapshot:(_a,b)=>b,normalizeWorkflowDetail:v=>v,normalizeWorkflowRunDetail:v=>v,selectConversation:async id=>selected.push(id),loadProjectRuns:async()=>{},backend:async(_f,m)=>{calls.push(m);if(m==='GetCurrentResearchTaskRun')return {run:{id:'new',workflowId:'new-w',conversationId:'new-c'}};if(m==='GetLatestRunSnapshot')return null;return {workflow:{id:'new-w'}}},useEffect:fn=>fn(),window:{addEventListener:()=>{},removeEventListener:()=>{},clearInterval:()=>{},setInterval:fn=>{refresh=fn;return 1}},document:{hidden:false}};
    vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(start,end),{}).outputText,context);
    refresh();await new Promise(resolve=>setTimeout(resolve,0));
    assert.equal(selected.length,blocked?0:1);if(blocked)assert.equal(calls.length,0);
  }
});

test('pending one-question composer returns free text to the same request, never starts another run',async()=>{
 const f=fixture();f.context.pendingQuestionKey='c:q';f.context.userInputRequest={runId:'run',callId:'q',questions:[{id:'goal',required:true}]};f.context.userInputAnswers={};f.context.questionAnswered=()=>false;
 let submitted;f.context.userInputIndexesRef={current:new Map()};f.context.updateUserInputAnswers=()=>{};f.context.submitUserInput=async answers=>{submitted=answers;return true};f.context.focusUserInputCard=()=>{};
 await f.send();assert.equal(f.calls.length,0);assert.equal(submitted.goal.text,'Follow-up question');assert.equal(f.state.input,'');
});
test('multi-question composer fills the visible inline question draft without a new run',async()=>{
 const f=fixture();f.context.pendingQuestionKey='c:q';f.context.userInputRequest={questions:[{id:'a',required:true},{id:'b',required:true}]};f.context.userInputAnswers={};f.context.questionAnswered=()=>false;
 let draft,focused;f.context.userInputIndexesRef={current:new Map([['c:q',1]])};f.context.updateUserInputAnswers=answers=>draft=answers;f.context.focusUserInputCard=()=>focused=true;
 await f.send();assert.equal(f.calls.length,0);assert.equal(focused,true);assert.equal(draft.b.text,'Follow-up question');assert.equal(f.state.input,'');
});

test('Enter while a question cancellation is pending neither submits nor changes its draft',async()=>{
 const f=fixture();f.context.pendingQuestionKey='c:q';f.context.userInputRequest={runId:'run',questions:[{id:'scope'}]};f.context.cancellationRef.current.add('run');
 let focused=false;f.context.focusUserInputCard=()=>focused=true;f.context.updateUserInputAnswers=()=>assert.fail('cancelled question draft changed');
 f.context.submitUserInput=()=>assert.fail('cancelled question resumed');
 await f.send();assert.equal(focused,true);assert.equal(f.calls.length,0);assert.equal(f.state.input,'Follow-up question');
});
