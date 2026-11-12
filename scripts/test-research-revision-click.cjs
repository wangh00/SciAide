// Actual App/React with isolated facade responses. No formal DB or model calls.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve(process.env.SCIAIDE_REVISION_CLICK_OUTPUT || '.tmp/revision-single-action-20261013/ui');
const basePath = process.env.SCIAIDE_REVISION_CLICK_FIXTURE || '.tmp/cancelled-recovery-ui-20261011/facade-after.json';
const base = JSON.parse(fs.readFileSync(basePath, 'utf8'));
const nodes = [
  {id:'sources', name:'核读已有材料', kind:'tool'},
  {id:'method_selection', name:'确定分析方法', kind:'agent_stage'},
  {id:'method_implementation', name:'实现分析方法', kind:'agent_stage'},
  {id:'python_environment', name:'准备分析依赖', kind:'tool', sideEffect:true},
  {id:'python_analysis', name:'执行可复现分析', kind:'python', sideEffect:true},
  {id:'result_interpretation', name:'解释结果与局限', kind:'agent_stage'},
  {id:'report_drafting', name:'形成交付稿', kind:'agent_stage'},
  {id:'independent_review', name:'独立二次审查', kind:'agent_stage'},
  {id:'delivery_gate', name:'核验交付条件', kind:'tool', tool:{qualifiedName:'builtin.research.workflow.review.gate'}},
  {id:'publish', name:'登记交付产物', kind:'tool', sideEffect:true},
];
const fixture = structuredClone(base);
fixture.detail.run = {...fixture.detail.run, status:'failed', currentStep:8, workflowName:'返修交互测试', errorCode:'RESEARCH_REVIEW_NOT_APPROVED', errorMessage:'报告与计算依据需要修正', outputs:{}, compilation:{...fixture.detail.run.compilation, nodes, order:nodes.map(n=>n.id), edges:[], outputs:[]}};
fixture.detail.steps = nodes.map((node, ordinal)=>({id:`step-${node.id}`, workflowRunId:fixture.detail.run.id, nodeId:node.id, nodeKind:node.kind, ordinal, status:ordinal===8?'failed':ordinal===9?'queued':'completed', attempt:1, input:{}, output:ordinal===7?{analysis:{approved:false, requiredCorrections:['核对实现与报告的一致性'], summary:'实际计算依据需要修订'}}:{}, errorCode:ordinal===8?'RESEARCH_REVIEW_NOT_APPROVED':'', errorMessage:ordinal===8?'核对实现与报告的一致性':'', updatedAt:'2026-10-13T01:00:00Z'}));
fixture.detail.aiExecutions=[]; fixture.detail.aiActivities=[]; fixture.detail.toolActivities=[];
fixture.detail.reviewRevisionTargets = [
  {nodeId:'method_implementation', label:'实现分析方法', repeatsSideEffects:true},
  {nodeId:'report_drafting', label:'形成交付稿', repeatsSideEffects:false},
];
fixture.detail.reviewRevisionRecommendation = {status:'recommended', nodeId:'method_implementation', label:'实现分析方法', summary:'核对并修正实际计算依据。', reasons:['仅修改文字不能修正计算。'], affectedStages:nodes.slice(2).map(n=>n.name), repeatsSideEffects:true, reviewOutputSha256:'current-review'};
fixture.detail.events=[{id:'review-failed',sequence:1,type:'workflow.failed',payload:{stepId:fixture.detail.steps[8].id},createdAt:'2026-10-13T01:00:00Z'}];
fixture.plan.workflow.name=fixture.detail.run.workflowName;
fixture.timeline.entries=[{id:'event:review-failed',sequence:'1',runId:fixture.detail.run.id,conversationId:fixture.detail.run.conversationId,kind:'event',eventType:'workflow.failed',active:true,step:fixture.detail.steps[8],createdAt:'2026-10-13T01:00:00Z',snapshot:{status:'failed',errorCode:'RESEARCH_REVIEW_NOT_APPROVED',errorMessage:'核对实现与报告的一致性',step:fixture.detail.steps[8],event:{stepId:fixture.detail.steps[8].id},outputs:{}}}];
const styles = process.env.SCIAIDE_PRODUCTION_CSS==='1'
  ? fs.readdirSync('frontend/dist/assets').filter(f=>f.endsWith('.css')).map(f=>fs.readFileSync(path.join('frontend/dist/assets',f),'utf8')).join('\n')
  : ['styles.css','referenceMaterials.css','modal.css'].map(f=>fs.readFileSync(`frontend/src/${f}`,'utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n');

const mock = `
window.calls=[];
window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
  window.calls.push({f,m,args}); const s=window.fixture, p=s.detail.run.projectId;
  if(m==='ListProjects') return [{id:p,name:'返修交互夹具',workspacePath:'D:/Fixture',workspaceKind:'external'}];
  if(m==='ListModelProfiles') return [{id:'fixture-profile',name:'Fixture',models:[{id:'fixture-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,reasoningLevels:[],enabled:true,isDefault:true}],secretConfigured:true,enabled:true,isDefault:true,modelId:'fixture-model',customHeaders:{},apiProtocol:'openai_responses',baseUrl:'http://fixture.invalid'}];
  if(f==='WorkflowFacade') {
    if(m==='List') return [s.plan.workflow];
    if(m==='ListProjectRuns') return [s.detail.run];
    if(m==='Get') return s.plan;
    if(m==='GetRun'||m==='GetCurrentResearchTaskRun') return s.detail;
    if(m==='GetRunByConversation') return {found:true,run:s.detail};
    if(m==='GetPendingResearchAdoption') return null;
    if(m==='PollRun') return {revision:String(s.revision||1),unchanged:false,value:s.detail};
    if(m==='Retry') return await new Promise((resolve,reject)=>{
      window.failRetry=()=>reject(Error('Fixture: temporarily unavailable'));
      window.finishRetry=()=>{
        const target=s.detail.steps.find(v=>v.nodeId===args[0].revisionNodeId);
        s.detail.steps.forEach(v=>{if(v.ordinal>=target.ordinal && v.ordinal<=8){v.status='queued';v.attempt++;}});
        target.status='running';s.detail.run.status='running';s.detail.run.currentStep=target.ordinal;
        s.timeline.entries.forEach(v=>v.active=false);s.revision=(s.revision||1)+1;resolve(s.detail);
      };
    });
  }
  if(m==='GetConversation') return s.conversation;
  if(m==='ListConversations') return [s.conversation];
  if(m==='ListMessages') return [];
  if(m==='ResearchTimeline') return s.timeline;
  if(m==='PollResearchTimeline') return {revision:String(s.revision||1),unchanged:false,value:s.timeline};
  if(m==='GetLatestRunSnapshot') return null;
  if(m==='PollLatestRunSnapshot') return {revision:'1',unchanged:false,value:null};
  if(m==='GetNetworkConfig') return {global:{mode:'direct',url:''},modules:{},revision:0};
  if(m.startsWith('List')) return [];
  if(['Start','StartChat','StartResearch','Decide','ConfirmResearchRevision'].includes(m)) throw Error('Unexpected execution: '+m);
  return {};
}})})};
window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:()=>{},OnFileDropOff:()=>{}};
`;

(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const bundle=await build({stdin:{contents:"import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<App/>);",loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const browser=await chromium.launch({channel:'msedge',headless:true});
  const errors=[];let lastPage;
  const rpc=page=>page.evaluate(()=>window.calls.filter(c=>c.m==='Retry'));
  const card=page=>page.locator('.research-interaction-card.error');
  async function open(value=structuredClone(fixture)) {
    const page=lastPage=await browser.newPage({viewport:{width:1280,height:1300}});
    page.on('pageerror',e=>errors.push(e.message));
    await page.route('**/*',route=>route.abort());
    await page.setContent('<div id="root"></div>');await page.addStyleTag({content:styles});
    await page.addScriptTag({content:mock});await page.evaluate(value=>window.fixture=value,value);
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    await page.locator('.workflow-project-tasks article > button').first().click();
    await card(page).locator('.research-revision-advice').waitFor();return page;
  }
  try {
    const page=await open();const button=card(page).getByRole('button',{name:'按建议修改',exact:true});
    assert.equal(await button.isEnabled(),true);assert.equal(await card(page).getByRole('checkbox').count(),0);
    assert.doesNotMatch(await card(page).innerText(),/承担额外/);
    assert.deepEqual(await card(page).locator('.research-revision-stage-list li').allTextContents(),nodes.slice(2).map(n=>n.name));
    assert.match(await card(page).innerText(),/本次会重新计算或执行相关操作/);assert.equal((await rpc(page)).length,0);
    for(const width of [1280,960]) {
      await page.setViewportSize({width,height:1700});await card(page).scrollIntoViewIfNeeded();
      assert.equal(await card(page).evaluate(el=>el.scrollWidth>el.clientWidth),false);
      await card(page).screenshot({path:path.join(output,`recommended-${width}.png`)});
    }
    const prior=await page.evaluate(()=>window.fixture.detail.steps.slice(0,2));
    await button.evaluate(el=>{el.click();el.click();});
    await card(page).getByRole('button',{name:'正在准备修改…',exact:true}).waitFor();
    assert.equal((await rpc(page)).length,1);let command=(await rpc(page))[0].args[0];
    assert.equal(command.confirmSideEffect,true);assert.equal(command.useRecommendation,true);assert.equal(command.expectedReviewSha256,'current-review');
    await page.evaluate(()=>window.failRetry());await button.waitFor();assert.equal(await button.isEnabled(),true);
    // Failure keeps the same scope and allows a new explicit click, not auto retry.
    assert.equal((await rpc(page)).length,1);await button.click();await page.evaluate(()=>window.finishRetry());
    await page.waitForFunction(()=>!document.querySelector('.research-retry-actions'));
    assert.deepEqual(await page.evaluate(()=>window.fixture.detail.steps.slice(0,2)),prior);await page.close();

    const manual=await open();await card(manual).getByRole('button',{name:'调整修改范围',exact:true}).click();
    const manualButton=card(manual).getByRole('button',{name:'按所选范围修改',exact:true});
    assert.equal(await manualButton.isDisabled(),true);
    await card(manual).getByRole('combobox',{name:'返修起点'}).selectOption('report_drafting');
    assert.deepEqual(await card(manual).locator('.research-revision-stage-list li').allTextContents(),nodes.slice(6).map(n=>n.name));
    assert.doesNotMatch(await card(manual).innerText(),/本次会重新计算/);assert.equal((await rpc(manual)).length,0);
    await card(manual).screenshot({path:path.join(output,'manual-report.png')});await manualButton.click();
    command=(await rpc(manual))[0].args[0];assert.equal(command.confirmSideEffect,false);assert.equal(command.revisionNodeId,'report_drafting');assert.equal(command.useRecommendation,false);await manual.close();

    const selection=structuredClone(fixture);
    selection.detail.reviewRevisionTargets=[{nodeId:'method_selection',label:'选择已有引用',repeatsSideEffects:true}];
    selection.detail.reviewRevisionRecommendation={...fixture.detail.reviewRevisionRecommendation,nodeId:'method_selection',label:'选择已有引用'};
    selection.detail.evidenceReselection={nodeId:'method_selection',availableCount:64,recommendedCount:29,selectedRecommendedCount:15,missingRecommendedCount:14,previouslyUnofferedCount:0,reviewOutputSha256:'current-review'};
    const citations=await open(selection);assert.equal(await card(citations).getByRole('checkbox').count(),0);
    assert.match(await card(citations).innerText(),/此按钮只打开引用补选/);assert.match(await card(citations).innerText(),/确认引用后才继续/);
    assert.equal((await rpc(citations)).length,0);await card(citations).getByRole('button',{name:'打开引用补选',exact:true}).click();
    assert.equal((await rpc(citations))[0].args[0].confirmSideEffect,true);await citations.close();

    const missing=structuredClone(fixture);missing.detail.reviewRevisionRecommendation={status:'needs_input',summary:'缺少必要数据',requiredInputs:['确认样本身份'],repeatsSideEffects:false};
    const blocked=await open(missing);assert.equal(await card(blocked).locator('.research-retry-actions').count(),0);assert.equal((await rpc(blocked)).length,0);await blocked.close();
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(output,'result.json'),JSON.stringify({source:'actual App with isolated offline facade fixture',productionCSS:process.env.SCIAIDE_PRODUCTION_CSS==='1',checks:['one click / zero checkbox','exact suggested and manual suffix','1280/960 no overflow','click consent reaches Retry','double click coalesced','failure stays explicit','upstream retained','citation checkpoint not adopted automatically','missing input not executable'],errors},null,2));
    console.log('PASS: real App one-click revision, scope, consent forwarding, busy/error/citation boundaries; no formal DB or model requests.');
  } catch(error) {
    if(lastPage&&!lastPage.isClosed()) {fs.writeFileSync(path.join(output,'failed-page.txt'),await lastPage.locator('body').innerText());fs.writeFileSync(path.join(output,'failed-calls.json'),JSON.stringify(await lastPage.evaluate(()=>window.calls),null,2));await lastPage.screenshot({path:path.join(output,'failed-page.png')});}
    throw error;
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
