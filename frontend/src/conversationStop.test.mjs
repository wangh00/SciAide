import assert from 'node:assert/strict';
import test from 'node:test';
import fs from 'node:fs';
import vm from 'node:vm';
import {createRequire} from 'node:module';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import ts from 'typescript';

const require=createRequire(import.meta.url), exports={};
const source=fs.readFileSync(new URL('./ConversationStopButton.tsx',import.meta.url),'utf8');
vm.runInNewContext(ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.CommonJS,jsx:ts.JsxEmit.ReactJSX}}).outputText,{exports,require});
const app=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
const ast=ts.createSourceFile('App.tsx',app,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
function declaration(name){let found;function visit(node){if(ts.isFunctionDeclaration(node)&&node.name?.text===name)found=node;else ts.forEachChild(node,visit);}visit(ast);assert.ok(found,name);return found.getText(ast);}
let snapshotDeclaration,eventCallback;
function find(node){
 if(ts.isVariableDeclaration(node)&&node.name.getText(ast)==='applySnapshot')snapshotDeclaration=node.parent.parent.getText(ast);
 if(ts.isCallExpression(node)&&node.expression.getText(ast)==='eventsOn<Envelope>'&&node.arguments[0]?.text==='sciaide:run-event')eventCallback=node.arguments[1].getText(ast);
 if(ts.isCallExpression(node)&&node.expression.getText(ast)==='eventsOn'&&node.arguments[0]?.text==='sciaide:run-event')eventCallback=node.arguments[1].getText(ast);
 ts.forEachChild(node,find);
}
find(ast);assert.ok(snapshotDeclaration);assert.ok(eventCallback);
const handlers=ts.transpileModule(declaration('releaseCancellation')+'\n'+snapshotDeclaration+'\n'+declaration('stopConversationRun')+'\n'+declaration('submitUserInput')+'\nglobalThis.stop=stopConversationRun;globalThis.answer=submitUserInput;globalThis.snapshot=applySnapshot;globalThis.onEvent='+eventCallback+';',{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
const defaults={busy:true,workflowAI:false,workflowLocked:false,runId:'r',runConversationId:'c',conversationId:'c',sending:false,answering:false,cancellingRunId:'',waitingForAnswer:true,onStop:()=>{},icon:null};
const button=props=>exports.ConversationStopButton({...defaults,...props});

test('pending question keeps a separately labelled Stop control, not a submit/skip button',()=>{
 let stops=0;const node=button({onStop:()=>stops++});
 assert.equal(node.type,'button');assert.equal(node.props.type,'button');assert.equal(node.props.disabled,false);
 assert.equal(node.props['aria-label'],'停止讨论');assert.match(node.props.title,/不提交或跳过问题/);
 node.props.onClick();assert.equal(stops,1);assert.match(renderToStaticMarkup(node),/aria-label="停止讨论"/);
 assert.equal(button({waitingForAnswer:false}).props['aria-label'],'停止生成');
 assert.match(app,/pendingQuestionKey && conversationStopControl/);
 assert.match(app,/busy=\{userInputSubmitting\} disabled=\{cancellingRunId === activeRun\?\.id\}/);
});

test('Workflow owner remains separate and in-flight answers/cancels cannot be double submitted',()=>{
 for(const props of [{busy:false},{workflowAI:true},{workflowLocked:true}])assert.equal(button(props),null);
 for(const props of [{sending:true},{answering:true},{runId:undefined},{runConversationId:'other'},{cancellingRunId:'r'}])assert.equal(button(props).props.disabled,true);
 assert.equal(button({cancellingRunId:'r'}).props['aria-label'],'正在停止');
});

function fixture(){
 const calls=[],pending=[],state={notice:'',messages:[]},host=new Map();let failReads=false;
 const makeRun=id=>({id,conversationId:'c'+id,status:'waiting_user_input',createdAt:'2026-10-30T00:00:00Z'});
 const snap=id=>({run:{...host.get(id)},sequence:host.get(id).status==='waiting_user_input'?1:2,toolCalls:[],runSteps:[],pendingApprovals:[],messages:[],workflowAI:false,userInput:host.get(id).status==='waiting_user_input'?{runId:id,callId:'q'+id}:null});
 host.set('r',makeRun('r'));host.set('b',makeRun('b'));
 const context={busy:true,sendingRef:{current:false},userInputSubmitRef:{current:new Map()},submittingQuestions:new Map(),activeRunIsWorkflowAI:false,researchConversationLocked:false,conversationIdRef:{current:'cr'},cancellationRef:{current:new Set()},cancellingRuns:new Set(),userInputDraftsRef:{current:new Map()},userInputIndexesRef:{current:new Map()},setUserInputError:v=>state.error=v,setNotice:v=>state.notice=v,errorText:String,runSequenceRef:{current:new Map()},retrySequenceRef:{current:new Map()},appliedSnapshotRef:{current:null},snapshotRunRef:{current:null},useCallback:f=>f,normalizeRunSnapshot:s=>s,shareSnapshot:(old,value)=>value,runtimeActivityForRun:()=>null,mergeSnapshotMessages:(old,more)=>old,applyRetryEvent:v=>v,setRuntimeActivity:()=>{},setRetryStatus:()=>{},setRetryByRun:()=>{},setToolCalls:()=>{},setRunSteps:()=>{},setPendingApprovals:()=>{},setActiveRunIsWorkflowAI:v=>context.activeRunIsWorkflowAI=v,setMessages:f=>state.messages=f(state.messages),discardContentDeltas:()=>{},loadMessages:async()=>{},loadProfiles:async()=>{},
  backend:(facade,method,...args)=>{calls.push({facade,method,args});if(method==='GetRunSnapshot')return failReads?Promise.reject(Error('read unavailable')):Promise.resolve(snap(args[0]));return new Promise((resolve,reject)=>pending.push({method,args,resolve,reject}));}};
 context.setCancellingRuns=f=>context.cancellingRuns=f(context.cancellingRuns);
 context.setSubmittingQuestions=f=>context.submittingQuestions=f(context.submittingQuestions);
 context.setUserInputRequest=f=>context.userInputRequest=typeof f==='function'?f(context.userInputRequest):f;
 context.setActiveRun=f=>{context.activeRun=typeof f==='function'?f(context.activeRun):f;context.activeRunRef.current=context.activeRun;};
 context.setBusy=v=>context.busy=v;
 function select(id){context.activeRun={...host.get(id)};context.activeRunRef={current:context.activeRun};context.snapshotRunRef.current=context.activeRun;context.conversationIdRef.current=context.activeRun.conversationId;context.conversationId=context.activeRun.conversationId;context.pendingQuestionKey=context.activeRun.conversationId+':q'+id;context.userInputRequest={runId:id,callId:'q'+id};context.busy=true;}
 select('r');vm.createContext(context);vm.runInContext(handlers,context);
 function resolve(index=0){const p=pending[index];const id=p.method==='CancelRun'?p.args[0]:p.args[0].runId;host.get(id).status=p.method==='CancelRun'?'cancelled':'running';p.resolve(p.method==='CancelRun'?undefined:snap(id));}
 return {context,state,calls,pending,select,stop:()=>context.stop(),answer:(dismiss=false)=>context.answer({},dismiss),resolve,reject:(index=0)=>pending[index].reject(Error('Cancellation unavailable')),event:e=>context.onEvent(e),snapshot:s=>context.snapshot(s),snap,failReads:()=>failReads=true};
}
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const mutations=f=>f.calls.filter(c=>c.method!=='GetRunSnapshot');

test('Stop wins the same-frame race with Confirm/Skip and never sends an answer',async()=>{
 const f=fixture(),stopped=f.stop();assert.equal(f.context.cancellationRef.current.has('r'),true);
 assert.equal(await f.answer(),false);assert.equal(await f.answer(true),false);await f.stop();assert.deepEqual(mutations(f).map(c=>c.method),['CancelRun']);
 f.resolve();await stopped;await tick();assert.equal(f.context.busy,false);assert.equal(f.context.activeRun.status,'cancelled');assert.equal(f.context.userInputRequest,null);
});

test('Confirm wins the same-frame race; Stop does not cancel a half-submitted response',async()=>{
 const f=fixture(),answer=f.answer();assert.equal(f.context.userInputSubmitRef.current.get('cr:qr'),'r');await f.stop();
 assert.deepEqual(mutations(f).map(c=>c.method),['RespondToUserInput']);assert.equal(f.calls[0].args[0].dismiss,false);
 f.resolve();assert.equal(await answer,true);assert.equal(f.context.userInputSubmitRef.current.size,0);assert.equal(f.context.submittingQuestions.size,0);
});

test('Skip retains its existing response/resume contract, unlike Stop',async()=>{
 const f=fixture(),skipped=f.answer(true);assert.equal(f.calls[0].method,'RespondToUserInput');assert.equal(f.calls[0].args[0].dismiss,true);f.resolve();await skipped;
});

test('handler rejects stale conversations/Workflow owners and recovers from cancellation errors',async()=>{
 for(const change of [{activeRunIsWorkflowAI:true},{researchConversationLocked:true},{busy:false},{conversationIdRef:{current:'other'}}]){const f=fixture();Object.assign(f.context,change);await f.stop();assert.equal(f.calls.length,0);}
 const f=fixture(),stopped=f.stop();f.reject();await stopped;assert.match(f.state.notice,/Cancellation unavailable/);assert.equal(f.context.cancellationRef.current.size,0);assert.equal(f.context.cancellingRuns.size,0);assert.equal(f.context.busy,true);
});

test('A Stop -> B -> A keeps A locked; independent B cancellation is not erased by A completion',async()=>{
 const f=fixture(),a=f.stop();f.select('b');const b=f.stop();assert.equal(mutations(f).length,2);
 f.select('r');assert.equal(await f.answer(),false);await f.stop();assert.equal(mutations(f).length,2);
 f.select('b');f.resolve(0);await a;await tick();assert.equal(f.context.activeRun.id,'b');assert.equal(f.context.userInputRequest.runId,'b');assert.equal(f.context.cancellationRef.current.has('b'),true);
 f.resolve(1);await b;await tick();assert.equal(f.context.cancellationRef.current.size,0);
});

test('A answer does not block B Stop, and A response cannot clear B answer state',async()=>{
 const f=fixture(),a=f.answer();f.select('b');const b=f.stop();assert.deepEqual(mutations(f).map(c=>c.method),['RespondToUserInput','CancelRun']);
 f.resolve(0);await a;assert.equal(f.context.cancellationRef.current.has('b'),true);f.resolve(1);await b;await tick();
 const g=fixture(),ga=g.answer();g.select('b');const gb=g.answer();assert.equal(g.context.userInputSubmitRef.current.size,2);g.resolve(0);await ga;assert.equal(g.context.userInputSubmitRef.current.get('cb:qb'),'b');g.resolve(1);await gb;
});

test('terminal event without a Run clears only its question, even if refresh fails or window polling is absent',async()=>{
 const f=fixture();f.failReads();f.context.cancellationRef.current.add('r');f.context.cancellingRuns.add('r');
 f.event({type:'run.cancelled',aggregateId:'r',sequence:2,payload:{runId:'r',status:'cancelled',errorCode:'RUN_CANCELLED'}});
 assert.equal(f.context.userInputRequest,null);assert.equal(f.context.activeRun.status,'cancelled');assert.equal(f.context.busy,false);assert.equal(f.context.cancellationRef.current.size,0);
 // A stale snapshot with the same event sequence cannot resurrect a terminal Run.
 const stale=f.snap('r');stale.sequence=2;f.snapshot(stale);assert.equal(f.context.userInputRequest,null);assert.equal(f.context.activeRun.status,'cancelled');await tick();
 const g=fixture();g.context.cancellationRef.current.add('r');g.select('b');g.event({type:'run.cancelled',aggregateId:'r',sequence:2,payload:{runId:'r',status:'cancelled'}});
 assert.equal(g.context.userInputRequest.runId,'b');assert.equal(g.context.activeRun.id,'b');assert.equal(g.context.busy,true);assert.equal(g.context.cancellationRef.current.size,0);
 // Conversation selection can change before React synchronizes activeRunRef.
 const h=fixture();h.context.conversationIdRef.current='cb';h.event({type:'run.cancelled',aggregateId:'r',sequence:2,payload:{runId:'r',status:'cancelled'}});
 assert.equal(h.context.userInputRequest.runId,'r');assert.equal(h.context.busy,true);assert.equal(h.calls.length,0);
});
