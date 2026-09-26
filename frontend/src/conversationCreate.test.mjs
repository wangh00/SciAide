import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import ts from 'typescript';

const source=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
const start=source.indexOf('  async function createConversation()');
const end=source.indexOf('\n  const [cancellingRunId',start);
function fixture(){
  let resolve,reject;const pending=new Promise((yes,no)=>{resolve=yes;reject=no});
  const state={creating:false,list:[],selected:'',notice:''},calls=[];
  const context={projectId:'p',profileId:'profile',modelId:'model',workspaceReasoningLevel:'high',creatingConversationRef:{current:false},selectedProjectIdRef:{current:'p'},setCreating:x=>state.creating=x,setNotice:x=>state.notice=x,setConversations:f=>state.list=f(state.list),setConversationId:x=>state.selected=x,errorText:String,composerInputRef:{current:null},window:{requestAnimationFrame:f=>f()},backend:(...args)=>{calls.push(args);return pending}};
  vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.create=createConversation;',{}).outputText,context);
  return {context,state,calls,resolve,reject,create:()=>context.create()};
}
test('new conversation bypasses title dialog and coalesces repeated clicks',async()=>{
  const f=fixture(),work=f.create();await f.create();assert.equal(f.calls.length,1);
  assert.equal(f.calls[0][2].title,'');assert.equal(f.calls[0][2].reasoningLevel,'high');
  f.resolve({id:'new',title:'新会话'});await work;assert.equal(f.state.selected,'new');assert.equal(f.state.list.length,1);assert.equal(f.state.creating,false);
});
test('late creation never switches another project and failure allows retry',async()=>{
  const f=fixture(),work=f.create();f.context.selectedProjectIdRef.current='other';f.resolve({id:'new'});await work;assert.equal(f.state.selected,'');assert.equal(f.state.list.length,0);
  const failed=fixture(),attempt=failed.create();failed.reject(Error('create failed'));await attempt;assert.match(failed.state.notice,/create failed/);assert.equal(failed.context.creatingConversationRef.current,false);assert.equal(failed.state.creating,false);
});
test('chat creation does not require a model configuration',async()=>{
  const f=fixture();f.context.profileId='';f.context.modelId='';const work=f.create();assert.equal('modelProfileId' in f.calls[0][2],false);f.resolve({id:'new'});await work;
});
