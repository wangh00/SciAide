import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import ts from 'typescript';

const source=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
const start=source.indexOf('  async function send(event: FormEvent)');
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
