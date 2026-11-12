// Actual React App and production CSS with delayed Wails Facade fixtures.
// Not a native WebView or real-model test. No formal data or production patching.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');
const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
const out=path.resolve(process.env.SCIAIDE_QUESTION_STOP_UI_OUTPUT||'.tmp/question-stop-ui');
const previous=fs.readFileSync('scripts/test-research-conversation-entry.cjs','utf8');
const start=previous.indexOf('const mock = `');
const baseline=Function('return '+previous.slice(start+'const mock = '.length,previous.indexOf('`;',start)+1))();
const css=fs.readdirSync('frontend/dist/assets').filter(f=>f.endsWith('.css')).map(f=>fs.readFileSync(path.join('frontend/dist/assets',f),'utf8')).join('\n');

(async()=>{
 fs.mkdirSync(out,{recursive:true});
 const bundle=await build({stdin:{contents:`import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<App/>);`,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
 const browser=await chromium.launch({channel:'msedge',headless:true});
 const errors=[],results=[];
 async function fixture(){
  const page=await browser.newPage({viewport:{width:1280,height:900}});page.on('pageerror',e=>errors.push(e.message));
  await page.setContent('<div id="root"></div>');await page.addStyleTag({content:css});await page.addScriptTag({content:baseline});
  await page.evaluate(()=>{
   const initial=window.go.wails, convs=['A','B'].map(id=>({id:'c'+id,projectId:'p',title:'停止夹具 '+id,modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium',permissionMode:'plan'}));
   window.stopCalls=[];window.stopPending={};window.stopReadsFail=false;window.stopListeners=new Set();
   window.runtime.EventsOnMultiple=(name,cb)=>{if(name==='sciaide:run-event')window.stopListeners.add(cb);return ()=>window.stopListeners.delete(cb)};
   window.stopSnapshots=Object.fromEntries(convs.map(c=>[c.id,{sequence:1,run:{id:'r'+c.id,conversationId:c.id,assistantMessageId:'a'+c.id,status:'waiting_user_input',modelProfileId:'model1',modelId:'fixture-model',createdAt:'2026-10-30T00:00:00Z'},workflowAI:false,messages:[{id:'u'+c.id,runId:'r'+c.id,role:'user',status:'complete',internal:false,parts:[{id:'up'+c.id,type:'text',text:'需要确认写作研究范围'}]},{id:'a'+c.id,runId:'r'+c.id,role:'assistant',status:'streaming',internal:false,parts:[]}],toolCalls:[],runSteps:[],pendingApprovals:[],userInput:{runId:'r'+c.id,callId:'q'+c.id,questions:[{id:'scope',question:'先关注哪种写作结果？',required:true,options:[{id:'quality',label:'文章质量'},{id:'revision',label:'修订过程'}]}]}}]));
   window.finishStop=(id,method)=>{const s=window.stopSnapshots[id];s.sequence++;s.run.status=method==='CancelRun'?'cancelled':'running';s.userInput=null;window.stopPending[method+id]?.(method==='CancelRun'?undefined:structuredClone(s));};
   window.emitStop=(id)=>{const s=window.stopSnapshots[id];s.sequence++;s.run.status='cancelled';s.userInput=null;for(const cb of window.stopListeners)cb({aggregateId:s.run.id,sequence:s.sequence,type:'run.cancelled',payload:{runId:s.run.id,status:'cancelled',errorCode:'RUN_CANCELLED'}});};
   window.go.wails=new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
    if(f==='ConversationFacade'&&m==='ListConversations')return convs;
    if(f==='ConversationFacade'&&m==='GetConversation')return convs.find(c=>c.id===args[0]);
    if(f==='ConversationFacade'&&m==='ListMessages')return window.stopSnapshots[args[0]]?.messages||[];
    if(f==='WorkflowFacade'&&m==='GetRunByConversation')return {found:false};
    if(f==='ChatFacade'){
     if(m==='ListConversationActivity')return convs.map(c=>({conversationId:c.id,status:window.stopSnapshots[c.id].run.status}));
     if(['GetLatestRunSnapshot','PollLatestRunSnapshot','GetRunSnapshot','PollRunSnapshot'].includes(m)){
      if(window.stopReadsFail)throw Error('Controlled snapshot outage');
      const id=args[0].startsWith('r')?args[0].slice(1):args[0],s=window.stopSnapshots[id]||null;
      // IPC serializes every response; never hand React a mutable host object.
      const value=s?structuredClone(s):null;
      return m.startsWith('Poll')?{revision:String(value?.sequence||0),unchanged:false,value}:value;
     }
     if(m==='CancelRun'||m==='RespondToUserInput'){
      const runId=m==='CancelRun'?args[0]:args[0].runId,id=runId.slice(1);window.stopCalls.push({method:m,runId,...(m==='RespondToUserInput'?{response:args[0]}:{})});
      return new Promise(resolve=>{window.stopPending[m+id]=resolve});
     }
     if(m==='StartChat'||m==='SteerChat')throw Error('Unexpected new Chat Run');
    }
    return initial[f][m](...args);
   }})});
  });
  await page.addScriptTag({content:bundle.outputFiles[0].text});await page.getByRole('button',{name:'停止夹具 A',exact:true}).click();await page.locator('.user-input-card').waitFor();return page;
 }
 const card=p=>p.locator('.user-input-card'),stop=p=>p.getByRole('button',{name:'停止讨论',exact:true});
 const go=(p,id)=>p.getByRole('button',{name:'停止夹具 '+id,exact:true}).click();
 const calls=p=>p.evaluate(()=>window.stopCalls);
 const rendered=async p=>{
  let timeout;try{await Promise.race([p.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))),new Promise((_,reject)=>{timeout=setTimeout(()=>reject(Error('Controlled App render did not settle')),5000)})]);}finally{clearTimeout(timeout)}
 };
 try{
  const p=await fixture();assert.equal(await stop(p).isEnabled(),true);
  // Same-frame DOM clicks exercise the real mounted handlers, not copied logic.
  await card(p).getByRole('radio',{name:'文章质量',exact:true}).check();
  await p.evaluate(()=>{const buttons=[...document.querySelectorAll('button')];buttons.find(b=>b.ariaLabel==='停止讨论').click();buttons.find(b=>b.textContent.trim()==='确定').click();buttons.find(b=>b.textContent.trim()==='跳过并继续').click();});
  await p.getByRole('button',{name:'正在停止',exact:true}).waitFor();
  assert.deepEqual((await calls(p)).map(c=>c.method),['CancelRun']);assert.equal(await card(p).getByRole('button',{name:'跳过并继续',exact:true}).isDisabled(),true);
  await go(p,'B');await card(p).waitFor();assert.equal(await stop(p).isEnabled(),true);await stop(p).click();
  await go(p,'A');assert.equal(await p.getByRole('button',{name:'正在停止',exact:true}).isDisabled(),true);
  await go(p,'B');await p.evaluate(()=>window.finishStop('cA','CancelRun'));
  await rendered(p);
  assert.equal(await p.getByRole('button',{name:'正在停止',exact:true}).isDisabled(),true);assert.equal(await card(p).count(),1);
  await p.evaluate(()=>window.finishStop('cB','CancelRun'));await card(p).waitFor({state:'detached'});assert.deepEqual((await calls(p)).map(c=>c.runId),['rcA','rcB']);
  await p.screenshot({path:path.join(out,'independent-cancellations.png')});results.push('Stop wins same-frame Confirm/Skip; A/B navigation keeps independent Run locks');

  const q=await fixture();await card(q).getByRole('radio',{name:'文章质量',exact:true}).check();await card(q).getByRole('button',{name:'确定',exact:true}).click();
  assert.equal(await stop(q).isDisabled(),true);await go(q,'B');assert.equal(await stop(q).isEnabled(),true);await stop(q).click();
  await q.evaluate(()=>window.finishStop('cA','RespondToUserInput'));await rendered(q);assert.equal(await q.getByRole('button',{name:'正在停止',exact:true}).isDisabled(),true);
  await q.evaluate(()=>window.finishStop('cB','CancelRun'));await card(q).waitFor({state:'detached'});assert.deepEqual((await calls(q)).map(c=>c.method),['RespondToUserInput','CancelRun']);results.push('Answer in A does not block Stop in B or clear its in-flight cancellation');

  const r=await fixture();await stop(r).click();await r.evaluate(()=>{window.stopReadsFail=true;window.emitStop('cA');});await card(r).waitFor({state:'detached'});
  assert.equal(await r.getByRole('button',{name:'正在停止',exact:true}).count(),0);assert.deepEqual((await calls(r)).map(c=>c.method),['CancelRun']);
  await r.screenshot({path:path.join(out,'terminal-event-clears-card.png')});results.push('Terminal event without full Run clears card while snapshot refresh fails');

  const s=await fixture();await card(s).getByRole('button',{name:'跳过并继续',exact:true}).click();const skipped=await calls(s);assert.equal(skipped.length,1);assert.equal(skipped[0].method,'RespondToUserInput');assert.equal(skipped[0].response.dismiss,true);assert.deepEqual(skipped[0].response.answers,{});results.push('Skip remains explicit dismiss/resume, never cancellation');
  assert.deepEqual(errors,[]);fs.writeFileSync(path.join(out,'result.json'),JSON.stringify({source:'full mounted React App, production CSS, controlled delayed Facades; not native or real model',results,errors},null,2));console.log('PASS:',results.join('; '));
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
