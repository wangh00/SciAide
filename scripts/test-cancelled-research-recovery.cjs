// Actual React App fed by temporary SQLite/bootstrap facade projections.
// No native EXE, user database or commercial model is opened by this script.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve(process.env.SCIAIDE_CANCELLED_UI_OUTPUT || '.tmp/cancelled-recovery-ui-20261011');
const fixturePath = process.env.SCIAIDE_CANCELLED_UI_FIXTURE || path.join(output, 'facade-after.json');
const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'));
const interrupted = process.env.SCIAIDE_RECOVERY_UI_STATUS === 'interrupted';
const terminalStatus = interrupted ? 'interrupted' : 'cancelled';
const recoveryEvent = interrupted ? 'workflow.interrupted' : 'workflow.cancel_requested';
assert.equal(fixture.detail.run.status, terminalStatus, 'fixture must retain its actual persisted lifecycle outcome');
const css = process.env.SCIAIDE_PRODUCTION_CSS === '1'
  ? fs.readdirSync('frontend/dist/assets').filter(f => f.endsWith('.css')).map(f => fs.readFileSync(path.join('frontend/dist/assets', f), 'utf8')).join('\n')
  : ['styles.css', 'referenceMaterials.css', 'modal.css'].map(f => fs.readFileSync(`frontend/src/${f}`, 'utf8').replace('@import "./researchCard.css";', fs.readFileSync('frontend/src/researchCard.css', 'utf8'))).join('\n');
const mock = `
window.calls=[];
window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
  window.calls.push({f,m,args});
  const s=window.fixture, p=s.detail.run.projectId;
  if(m==='ListProjects') return [{id:p,name:'取消阶段恢复测试',workspacePath:'D:/Fixture',workspaceKind:'external'}];
  if(m==='ListModelProfiles') return [{id:'fixture-profile',name:'Fixture',models:[{id:'fixture-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,reasoningLevels:[],enabled:true,isDefault:true}],secretConfigured:true,enabled:true,isDefault:true,modelId:'fixture-model',customHeaders:{},apiProtocol:'openai_responses',baseUrl:'http://fixture.invalid'}];
  if(f==='WorkflowFacade') {
    if(m==='List') return [s.plan.workflow];
    if(m==='ListProjectRuns') return [s.detail.run];
    if(m==='Get') return s.plan;
    if(m==='GetRun'||m==='GetCurrentResearchTaskRun') return s.detail;
    if(m==='GetRunByConversation') return {found:true,run:s.detail};
    if(m==='GetPendingResearchAdoption') return null;
    if(m==='PollRun') return {revision:String(s.revision || 1),unchanged:false,value:s.detail};
    if(m==='Retry') return await new Promise((resolve,reject)=>{
      window.finishRetry=()=>{
        const step=s.detail.steps.find(step=>step.id===args[0].stepId);
        if(!step) throw Error('Wrong recovery step');
        s.detail.run.status='running'; s.detail.run.cancelRequested=false; s.detail.run.errorMessage='';
        step.status='running'; step.attempt+=1;
        s.timeline.entries.forEach(entry=>entry.active=false);
        s.revision=(s.revision || 1)+1; resolve(s.detail);
      };
      window.failRetry=()=>reject(Error('Fixture: temporarily unavailable'));
    });
    if(m==='Cancel') { window.applyCancelledState(); return window.fixture.detail; }
  }
  if(m==='GetConversation') return s.conversation;
  if(m==='ListConversations') return [s.conversation];
  if(m==='ListMessages') return s.timeline.entries.flatMap(entry=>entry.message?[entry.message]:[]);
  if(m==='ResearchTimeline') return s.timeline;
  if(m==='PollResearchTimeline') return {revision:String(s.revision || 1),unchanged:false,value:s.timeline};
  if(m==='GetLatestRunSnapshot') return null;
  if(m==='PollLatestRunSnapshot') return {revision:'1',unchanged:false,value:null};
  if(m==='GetNetworkConfig') return {global:{mode:'direct',url:''},modules:{},revision:0};
  if(m.startsWith('List')) return [];
  if(['Start','StartChat','StartResearch','Decide'].includes(m)) throw Error('Unexpected execution: '+m);
  return {};
}})})};
window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:()=>{},OnFileDropOff:()=>{}};
`;

(async () => {
  fs.mkdirSync(output, {recursive:true});
  const bundle = await build({stdin:{contents:"import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<App/>);",loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const browser = await chromium.launch({channel:'msedge',headless:true});
  const errors=[];
  let currentPage;
  const calls = (page, method) => page.evaluate(m=>window.calls.filter(c=>c.m===m),method);
  const recovery = page => page.getByRole('button',{name:'恢复当前阶段',exact:true});
  const baseEntry = fixture.timeline.entries.find(e=>e.eventType===recoveryEvent);
  assert.ok(baseEntry,'fixture must contain its persisted recovery event');
  async function open(value=structuredClone(fixture)) {
    const page = currentPage = await browser.newPage({viewport:{width:1280,height:900}});
    page.on('pageerror',error=>errors.push(error.message));
    await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:css}); await page.addScriptTag({content:mock});
    await page.evaluate(value=>window.fixture=value,value);
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    await page.locator('.workflow-project-tasks article > button').first().click();
    await page.locator('.research-route-panel').waitFor();
    await page.locator('.research-task-timeline [data-timeline-id="'+baseEntry.id+'"]').waitFor({state:'attached'});
    return page;
  }
  async function assertNoExecution(page) {
    for(const m of ['Retry','Start','StartChat','StartResearch','Decide']) assert.equal((await calls(page,m)).length,0,m+' must require a human action');
  }
  try {
    const page = await open();
    await recovery(page).waitFor({timeout:4000});
    await recovery(page).waitFor({state:'visible'});
    assert.equal(await recovery(page).isDisabled(),false);
    assert.equal(await recovery(page).count(),1,'one recovery card, not duplicate controls');
    assert.equal(await page.locator('.research-task-timeline .research-retry-actions').count(),1,'recovery belongs in the feed');
    assert.match(await page.locator('.research-interaction-card.error').innerText(),/保留已完成阶段、原始资料和历史记录/);
    await assertNoExecution(page);
    for(const width of [1280,960]) {
      await page.setViewportSize({width,height:900});
      await page.waitForFunction(()=>[...document.querySelectorAll('.research-task-timeline')].every(root=>{
        for(let el=root;el;el=el.parentElement) if(Number(getComputedStyle(el).opacity)<0.99) return false;
        return root.getAnimations({subtree:true}).filter(animation=>animation.playState==='running' && animation.effect?.getComputedTiming().iterations!==Infinity).length===0;
      }));
      await page.screenshot({path:path.join(output,`${terminalStatus}-${width}.png`)});
      const bounds=await recovery(page).evaluate(button=>{
        const b=button.getBoundingClientRect(),c=button.closest('.research-card').getBoundingClientRect();
        return {overflow:document.documentElement.scrollWidth>innerWidth,inside:b.left>=c.left&&b.right<=c.right};
      });
      assert.equal(bounds.overflow,false); assert.equal(bounds.inside,true);
    }
    // Leave and reopen the same task: state reads must not request a model.
    await page.getByRole('button',{name:'自由对话',exact:true}).click();
    assert.equal(await recovery(page).count(),0,'ordinary chat does not own workflow recovery controls');
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    // The App may already restore the previously selected conversation.
    // Explicitly return to the list before opening it again.
    await page.getByRole('button',{name:'返回科研任务列表',exact:true}).click();
    await page.locator('.workflow-project-tasks article > button').first().click();
    await recovery(page).waitFor(); await assertNoExecution(page);
    const upstream=await page.evaluate(()=>structuredClone(window.fixture.detail.steps[0]));
    await recovery(page).click();
    await page.getByRole('button',{name:'正在恢复…',exact:true}).waitFor();
    assert.equal(await page.getByRole('button',{name:'正在恢复…',exact:true}).isDisabled(),true);
    assert.equal((await calls(page,'Retry')).length,1);
    const command=(await calls(page,'Retry'))[0].args[0];
    assert.equal(command.stepId,fixture.detail.steps[1].id);
    assert.equal(command.runId,fixture.detail.run.id); assert.equal(command.projectId,fixture.detail.run.projectId);
    assert.equal(command.confirmSideEffect,false);
    await page.evaluate(()=>window.finishRetry());
    await page.waitForFunction(()=>!document.querySelector('.research-retry-actions'));
    assert.deepEqual(await page.evaluate(()=>window.fixture.detail.steps[0]),upstream);
    assert.equal((await calls(page,'Retry')).length,1);
    await page.close();

    // A failed Retry RPC leaves the action available, no hidden auto-retry.
    const failed=await open(); await recovery(failed).click();
    await failed.getByRole('button',{name:'正在恢复…',exact:true}).waitFor();
    await failed.evaluate(()=>window.failRetry());
    await recovery(failed).waitFor(); assert.equal(await recovery(failed).isDisabled(),false);
    assert.equal((await calls(failed,'Retry')).length,1); await failed.close();

    // Same-run old cancellations, wrong attempts, non-current stages and
    // cancelled tools cannot inherit the newest AI recovery action.
    for(const scenario of ['old-run','old-attempt','wrong-step','cancelled-tool','already-running']) {
      const value=structuredClone(fixture),entry=value.timeline.entries.find(e=>e.id===baseEntry.id);
      entry.active=true; entry.step=structuredClone(value.detail.steps[1]);
      if(scenario==='old-run') entry.runId='historical-run';
      if(scenario==='old-attempt') entry.step.attempt-=1;
      if(scenario==='wrong-step') entry.step=structuredClone(value.detail.steps[0]);
      if(scenario==='cancelled-tool') {value.detail.steps[1].nodeKind='tool';value.detail.run.compilation.nodes.find(n=>n.id===value.detail.steps[1].nodeId).kind='tool';entry.step.nodeKind='tool';}
      if(scenario==='already-running') {value.detail.run.status='running';value.detail.steps[1].status='running';}
      const negative=await open(value);
      assert.equal(await recovery(negative).count(),0,scenario); await assertNoExecution(negative); await negative.close();
    }
    const duplicate=structuredClone(fixture),entry=duplicate.timeline.entries.find(e=>e.id===baseEntry.id);
    duplicate.timeline.entries.splice(duplicate.timeline.entries.indexOf(entry),0,{...structuredClone(entry),id:'event:older-cancel',sequence:String(BigInt(entry.sequence)-1n),active:true});
    const unique=await open(duplicate); await recovery(unique).waitFor(); assert.equal(await recovery(unique).count(),1); await unique.close();

    // Cancel from the running view, not only initial loading of terminal state.
    const running=structuredClone(fixture);
    running.detail.run.status='running'; running.detail.run.errorMessage='';
    running.detail.steps[1].status='running';
    running.timeline.entries=running.timeline.entries.filter(e=>e.id!==baseEntry.id);
    // Keep a harmless historical marker so open() can wait for the same DOM row.
    running.timeline.entries.push({...structuredClone(baseEntry),active:false,eventType:'workflow.created'});
    if(!interrupted) {
    const cancelling=await open(running);
    await cancelling.evaluate(cancelled=>{
      window.applyCancelledState=()=>{window.fixture=structuredClone(cancelled);window.fixture.revision=2;};
    },fixture);
    await cancelling.locator('.research-route-panel').getByRole('button',{name:'取消',exact:true}).click();
    await recovery(cancelling).waitFor(); assert.equal((await calls(cancelling,'Cancel')).length,1); await assertNoExecution(cancelling); await cancelling.close();
    }

    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(output,'browser-verification.json'),JSON.stringify({passed:true,fixturePath,scope:'Real temporary SQLite facade load; controlled Retry RPC in browser; real HTTP/bootstrap Retry separately tested',scenarios:[terminalStatus+'-load','reopen','explicit-exact-step-retry','busy-single-action','retry-rpc-failure','history-and-attempt-isolation','no-tool-replay','no-normal-chat-control',...(!interrupted?['in-place-cancel']:[]),'layout-1280-960'],pageErrors:errors},null,2));
    console.log(`PASS: ${terminalStatus} research recovery is visible, explicit, uniquely scoped and preserved on reopening`);
  } catch(error) {
    if(currentPage && !currentPage.isClosed()) {
      fs.writeFileSync(path.join(output,'browser-failure.json'),JSON.stringify({message:error.message,...(await currentPage.evaluate(()=>({text:document.body.innerText,calls:window.calls}))),errors},null,2));
      await currentPage.screenshot({path:path.join(output,'browser-failure.png')});
    }
    throw error;
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
