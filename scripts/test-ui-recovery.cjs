// Actual App reload/race fixture. No formal database, task mutation or model.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve(process.env.SCIAIDE_RECOVERY_OUTPUT || '.tmp/ui-recovery');
const viewKey = 'sciaide-ui-recovery-v1';
const mock = `
window.calls=[];
const projects=[{id:'p',name:'恢复测试项目',workspacePath:'D:/Fixture',workspaceKind:'external'},{id:'q',name:'另一个项目',workspacePath:'D:/Other',workspaceKind:'external'}];
const c={id:'c',projectId:'p',title:'原科研任务',permissionMode:'full_access',modelProfileId:'model',modelId:'fixture-model',reasoningLevel:'medium'};
const other={...c,id:'other',title:'普通对话'};
const nodes=[{id:'report',name:'研究报告',kind:'agent_stage'}],compilation={nodes,edges:[],outputs:[]};
const report={markdown:'# 恢复前已保存报告\\n\\n此内容来自原任务，不重新计算。',claimSummary:[],methodSummary:'已有结果',limitations:[],confidence:'low'};
const detail={run:{id:'r',workflowId:'w',workflowVersionId:'v',projectId:'p',researchTaskId:'t',workflowName:'原科研任务',workflowPurpose:'user_plan',conversationId:'c',status:'completed',createdAt:'2026-10-13T00:00:00Z',inputs:{},outputs:{report_draft:report,citations:[],delivery_gate:{approved:true}},compilation},steps:[],events:[{id:'done',type:'workflow.completed',payload:{},createdAt:'2026-10-13T00:00:00Z'}],pendingApprovals:[],aiExecutions:[],artifactCount:0,registeredDeliverables:['report_draft'],deliveryAssessment:{status:'reviewed',label:'研究报告',report:{confidencePolicy:'claim_based'},checks:[],limitations:[]}};
const owner={workflow:{id:'w',projectId:'p',name:'原科研任务',currentVersionId:'v',version:1},versions:[{id:'v',workflowId:'w',definition:{schemaVersion:1,name:'原科研任务',nodes,edges:[],inputs:[],outputs:[]},compilation}]};
const timeline={taskId:'t',latestRunId:'r',conversationId:'c',hasMore:false,entries:[{id:'event:done',sequence:'1',kind:'event',eventType:'workflow.completed',runId:'r',conversationId:'c',active:true,snapshot:{event:{},outputs:detail.run.outputs}}]};
const wait=ms=>new Promise(r=>setTimeout(r,ms));
window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
 window.calls.push({f,m,args});
 if(['Start','StartChat','StartResearch','Retry','Decide','ConfirmResearchRevision','RespondToUserInput','CancelRun','CreateConversation','CreateResearchTask'].includes(m))throw Error('Unexpected execution: '+m);
 if(m==='ListProjects')return projects;
 if(m==='ListModelProfiles')return[{id:'model',name:'Fixture',models:[{id:'fixture-model',contextWindowTokens:128000,reasoningLevels:[],enabled:true,isDefault:true}],secretConfigured:true,enabled:true,isDefault:true,modelId:'fixture-model',customHeaders:{},apiProtocol:'openai_chat_completions',baseUrl:'http://fixture.invalid'}];
 if(m==='ListConversations'){await wait(window.scenario.listDelay||0);return args[0]==='p'?[other,c]:[{...other,id:'q-c',projectId:'q',title:'新项目对话'}]}
 if(m==='GetConversation'){await wait(window.scenario.getDelay||0);return args[0]==='c'?c:other}
 if(m==='GetRunByConversation'){await wait(window.scenario.lookupDelay||0);return args[0]==='c'?{found:true,run:detail}:{found:false}}
 if(m==='ListProjectRuns')return args[0]==='p'?[detail.run]:[];
 if(m==='List'&&f==='WorkflowFacade')return args[0]==='p'?[owner.workflow]:[];
 if(m==='Get'&&f==='WorkflowFacade')return owner;
 if(['GetRun','GetCurrentResearchTaskRun'].includes(m))return detail;
 if(m==='PollRun')return{revision:'1',unchanged:false,value:detail};
 if(m==='ResearchTimeline')return timeline;
 if(m==='PollResearchTimeline')return{revision:'1',unchanged:false,value:timeline};
 if(m==='ListMessages')return[];
 if(m==='GetLatestRunSnapshot')return null;
 if(m==='PollLatestRunSnapshot')return{revision:'1',unchanged:false,value:null};
 if(m==='GetPendingResearchAdoption')return null;
 if(m==='GetNetworkConfig')return{global:{mode:'direct',url:''},modules:{},revision:0};
 if(m.startsWith('List'))return[];
 return{};
}})})};
window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:()=>{},OnFileDropOff:()=>{}};
`;

(async()=>{
 fs.mkdirSync(output,{recursive:true});
 const bundle=await build({stdin:{contents:"import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';import {UIErrorBoundary} from './UIErrorBoundary';createRoot(document.getElementById('root')).render(<React.StrictMode><UIErrorBoundary scope='application'><App/></UIErrorBoundary></React.StrictMode>);",loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
 const css=fs.readdirSync('frontend/dist/assets').filter(n=>n.endsWith('.css')).map(n=>fs.readFileSync('frontend/dist/assets/'+n,'utf8')).join('\n');
 const browser=await chromium.launch({channel:'msedge',headless:true});
 const checks=[];
 const page=await browser.newPage({viewport:{width:1280,height:900}});
 try{
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  let scenario={};
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url());
   if(url.origin!=='http://fixture.local')return route.abort();
   if(url.pathname==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:'<!doctype html><meta charset="utf-8"><div id="root"></div><script src="/fixture.js"></script><script src="/app.js"></script>'});
   if(url.pathname==='/app.js')return route.fulfill({contentType:'text/javascript; charset=utf-8',body:bundle.outputFiles[0].text});
   if(url.pathname==='/fixture.js')return route.fulfill({contentType:'text/javascript; charset=utf-8',body:'window.scenario='+JSON.stringify(scenario)+';'+mock});
   return route.abort();
  });
  await page.goto('http://fixture.local/');
  await page.waitForTimeout(600);
  const reload=async(view,config={})=>{
   scenario=config;await page.evaluate(({key,view})=>sessionStorage.setItem(key,JSON.stringify(view)),{key:viewKey,view});
   await page.reload();await page.addStyleTag({content:css});
  };
  const view={projectId:'p',conversationId:'c',mode:'research',draft:'尚未发送的补充要求',attachments:[{id:'draft:file',originalName:'未发送数据.csv',draftPath:'D:/Fixture/data.csv'}]};
  await reload(view,{lookupDelay:250,listDelay:800});
  await page.getByRole('region',{name:'本轮研究报告'}).waitFor({timeout:15000});
  await page.waitForTimeout(1200);
  let saved=await page.evaluate(key=>JSON.parse(sessionStorage.getItem(key)),viewKey);
  assert.equal(saved.conversationId,'c','late conversation list selected a different task');
  assert.equal(saved.draft,view.draft,'restored draft lost');
  assert.deepEqual(saved.attachments,view.attachments,'restored attachment references lost');
  assert.equal(await page.getByText('未发送数据.csv',{exact:true}).count(),1,'restored attachment not shown');
  checks.push('actual App reload retains original research task, draft and attachment references');
  await page.reload();await page.addStyleTag({content:css});
  await page.getByRole('region',{name:'本轮研究报告'}).waitFor();await page.waitForTimeout(1200);
  saved=await page.evaluate(key=>JSON.parse(sessionStorage.getItem(key)),viewKey);assert.deepEqual(saved,view);
  checks.push('second reload reads original task again without restarting it');
  await reload(view,{lookupDelay:2200,listDelay:100});
  await page.waitForFunction(()=>window.calls.some(c=>c.m==='GetRunByConversation'&&c.args[0]==='c'));
  await page.locator('#project').selectOption('q');
  await page.waitForTimeout(2800);
  saved=await page.evaluate(key=>JSON.parse(sessionStorage.getItem(key)),viewKey);
  assert.equal(saved.projectId,'q','late restore changed project');assert.notEqual(saved.conversationId,'c','late restore changed conversation');
  assert.equal(await page.getByText('恢复前已保存报告',{exact:true}).count(),0,'late lookup leaked old task into new project');
  checks.push('switching project ignores late restore and old conversation list');
  await reload(view,{getDelay:900,lookupDelay:50,listDelay:50});
  await page.getByRole('button',{name:'普通对话',exact:true}).click();
  await page.waitForTimeout(1800);
  saved=await page.evaluate(key=>JSON.parse(sessionStorage.getItem(key)),viewKey);
  assert.equal(saved.conversationId,'other','late recovery overwrote selected conversation');
  assert.equal(saved.mode,'chat');
  assert.deepEqual(saved.attachments,[],'old attachment references entered another conversation');
  checks.push('selecting another conversation during recovery wins over late stored navigation');
  await reload({...view,conversationId:'other',mode:'chat',attachments:[]},{lookupDelay:100,listDelay:350});
  await page.waitForTimeout(1000);
  saved=await page.evaluate(key=>JSON.parse(sessionStorage.getItem(key)),viewKey);assert.equal(saved.mode,'chat');assert.equal(saved.conversationId,'other');assert.equal(saved.draft,view.draft);
  checks.push('ordinary chat also restores without creating research work');
  assert.deepEqual(errors,[]);
  const result={source:'actual App/React.StrictMode, isolated Edge and read-only mock facades, no native WebView/database/model',checks,errors};
  fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(result,null,2));console.log('PASS',JSON.stringify(result));
 }catch(error){fs.writeFileSync(path.join(output,'failure.txt'),String(error.stack));fs.writeFileSync(path.join(output,'failed-page.txt'),await page.locator('body').innerText());fs.writeFileSync(path.join(output,'failed-state.json'),JSON.stringify(await page.evaluate(key=>({saved:sessionStorage.getItem(key),calls:window.calls}),viewKey),null,2));throw error}
 finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
