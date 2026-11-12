// Actual React App with controlled Wails responses, never a user DB or model.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve(process.env.SCIAIDE_ROUTE_DATA_UI_OUTPUT || '.tmp/research-route-data-ui');
const css = process.env.SCIAIDE_PRODUCTION_CSS === '1'
  ? fs.readdirSync('frontend/dist/assets').filter(f => f.endsWith('.css')).map(f => fs.readFileSync(path.join('frontend/dist/assets', f), 'utf8')).join('\n')
  : ['styles.css', 'referenceMaterials.css', 'modal.css'].map(f => fs.readFileSync(`frontend/src/${f}`, 'utf8').replace('@import "./researchCard.css";', fs.readFileSync('frontend/src/researchCard.css', 'utf8'))).join('\n');

const mock = `
window.calls=[];
window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
  window.calls.push({f,m,args});
  const s=window.fixture;
  if(m==='ListProjects') return [{id:'p',name:'已上传数据复用测试',workspacePath:'D:/Fixture',workspaceKind:'external'}];
  if(m==='ListModelProfiles') return [{id:'model',name:'Fixture',models:[{id:'fixture-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,reasoningLevels:[],enabled:true,isDefault:true}],secretConfigured:true,enabled:true,isDefault:true,modelId:'fixture-model',customHeaders:{},apiProtocol:'openai_chat_completions',baseUrl:'http://fixture.invalid'}];
  if(f==='WorkflowFacade') {
    if(m==='List') return [s.planner.workflow,s.plan.workflow];
    if(m==='ListProjectRuns') return [s.detail.run];
    if(m==='Get') return args[1]===s.plan.workflow.id?s.plan:s.planner;
    if(m==='GetRun'||m==='GetCurrentResearchTaskRun') return s.detail;
    if(m==='GetRunByConversation') return {found:true,run:s.detail};
    if(m==='GetPendingResearchAdoption') return s.adopted?s.adoption:null;
    if(m==='GetResearchStarterPlan') return s.detail.run.outputs.plan;
    if(m==='AdoptResearchRoute') {
      s.adopted=true;
      s.detail.events.push({id:'adopted-event',type:'research.route_adopted',payload:{routeId:'tabular',availableNow:false}});
      return s.adoption;
    }
    if(m==='GetResearchRouteDataCandidates') {
      if(s.discoverDelayed) return await new Promise(resolve=>window.finishDiscovery=()=>resolve(s.candidates));
      if(s.discoverFails) throw Error('Fixture: candidate lookup unavailable');
      return s.candidates;
    }
    if(m==='StageResearchRouteData') {
      if(s.stageDelayed) return await new Promise(resolve=>window.finishStage=()=>resolve(s.files[args[0].attachmentId]));
      if(s.stageFails) throw Error('Fixture: stored file unavailable');
      return s.files[args[0].attachmentId];
    }
    if(m==='ChooseInputFileForResearchRoute') return {relativePath:'research-inputs/manual.csv',name:'manual.csv',sizeBytes:100,sha256:'c'.repeat(64)};
    if(m==='StartAdoptedResearchRoute') {
      s.started=structuredClone(args[0]); s.adopted=false;
      return {...s.detail,run:{...s.detail.run,id:'executing',workflowId:s.plan.workflow.id,workflowVersionId:'v',workflowPurpose:'user_plan',status:'completed',inputs:args[0].inputs,outputs:{},compilation:s.plan.versions[0].compilation},events:[]};
    }
    if(m==='PollRun') return {revision:'1',unchanged:false,value:s.detail};
  }
  if(f==='PythonFacade') {
    if(m==='GetProjectEnvironment') return {state:'ready'};
    if(m==='DetectInterpreters') return {interpreters:[]};
  }
  if(m==='GetConversation') return s.conversation;
  if(m==='ListConversations') return [s.conversation];
  if(m==='ResearchTimeline'||m==='PollResearchTimeline') {
    const value={...s.timeline,entries:s.adopted?[...s.timeline.entries,{id:'event:adopted-event',sequence:'3',kind:'event',eventType:'research.route_adopted',runId:'planner-run',conversationId:'chat',active:true,createdAt:'2026-10-11T00:00:01Z',snapshot:{event:{routeId:'tabular',availableNow:false}}}]:s.timeline.entries};
    return m==='ResearchTimeline'?value:{revision:s.adopted?'2':'1',unchanged:false,value};
  }
  if(m==='GetLatestRunSnapshot') return null;
  if(m==='PollLatestRunSnapshot') return {revision:'1',unchanged:false,value:null};
  if(m==='GetNetworkConfig') return {global:{mode:'direct',url:''},modules:{},revision:0};
  if(m.startsWith('List')) return [];
  if(['Start','StartChat','ChooseInputFile','StartResearch'].includes(m)) throw Error('Unexpected execution or file selection: '+m);
  return {};
}})})};
window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{}};
`;

function fixture(options={}) {
  const inputs=[{name:'research_goal',type:'string',required:true},{name:'route_context',type:'object'},
    {name:'input_paths',type:'array',fileKind:'tabular',required:true,minItems:1,maxItems:16},
    ...(options.twoPorts?[{name:'mapping',type:'array',fileKind:'tabular',required:false,maxItems:16,...(options.optionalDefault?{default:['research-inputs/default.csv']}: {})}]:[])];
  const nodes=[{id:'python_analysis',name:'计算',kind:'python'}];
  const compilation={nodes,edges:[],order:['python_analysis'],outputs:[]};
  const definition={schemaVersion:1,name:'葡萄酒数据分析',inputs,nodes,edges:[],outputs:[]};
  const plan={workflow:{id:'formal-w',projectId:'p',name:definition.name,currentVersionId:'v',version:1},versions:[{id:'v',workflowId:'formal-w',version:1,definition,compilation}]};
  const planner={workflow:{id:'planner-w',projectId:'p',name:'路线规划',currentVersionId:'pv',version:1},versions:[{id:'pv',workflowId:'planner-w',definition:{schemaVersion:1,name:'路线规划',inputs:[],nodes:[],edges:[],outputs:[]}}]};
  const route={routeId:'tabular',title:'分析已上传的葡萄酒数据',reason:'先确认采用的数据，再执行分析。',availableNow:false,validation:'blocked',blockers:['请选择一份 CSV、TSV 或 XLSX 数据。'],requiredResources:[],deliverables:['计算结果'],stageIds:['python_analysis'],reviewCheckpoints:[],layers:[]};
  const starterPlan={normalizedQuestion:'分析已上传的葡萄酒数据',researchType:'data_analysis',availableResources:[],missingInformation:[],selectedSkills:[],routes:[route],recommendedRouteId:'tabular',recommendationReason:'复用已上传的文件',confidence:'medium',limitations:[]};
  const run={id:'planner-run',projectId:'p',workflowId:'planner-w',workflowVersionId:'pv',workflowName:'路线规划',workflowPurpose:'research_starter',researchTaskId:'task',conversationId:'chat',status:'completed',permissionMode:'full_access',inputs:{starter_context:{researchIdea:'帮我分析一下葡萄酒数据'}},outputs:{plan:starterPlan},compilation:{nodes:[],edges:[],order:[],outputs:[]},createdAt:'2026-10-11T00:00:00Z',updatedAt:'2026-10-11T00:00:01Z'};
  const detail={run,steps:[],events:[{id:'completed',type:'workflow.completed',payload:{},createdAt:run.updatedAt}],aiExecutions:[],pendingApprovals:[],artifactCount:0};
  const conversation={id:'chat',projectId:'p',title:'葡萄酒数据分析',permissionMode:'full_access',modelProfileId:'model',modelId:'fixture-model',reasoningLevel:'medium'};
  const message={id:'human',conversationId:'chat',role:'user',status:'complete',internal:false,parts:[{type:'text',text:'帮我分析一下葡萄酒数据'},{type:'media',payload:{attachmentId:'wine',originalName:'wine-red-initial-1000.csv',mimeType:'text/csv',format:'csv',sizeBytes:1000,unitCount:1,truncated:false}}],createdAt:run.createdAt};
  const timeline={taskId:'task',latestRunId:run.id,conversationId:'chat',hasMore:false,entries:[{id:'message:human',sequence:'1',kind:'message',runId:run.id,conversationId:'chat',message,snapshot:{},createdAt:run.createdAt},{id:'event:completed',sequence:'2',kind:'event',eventType:'workflow.completed',runId:run.id,conversationId:'chat',active:true,snapshot:{workflowPurpose:'research_starter',outputs:{plan:starterPlan}},createdAt:run.updatedAt}]};
  const candidates=options.empty?[]:[{attachmentId:'wine',name:'wine-red-initial-1000.csv',sizeBytes:1000,sha256:'a'.repeat(64),portNames:options.twoPorts?['input_paths','mapping']:['input_paths']},...(options.multi?[{attachmentId:'other',name:'wine-complete.csv',sizeBytes:1500,sha256:'b'.repeat(64),portNames:['input_paths']}]:[])];
  const files=Object.fromEntries(candidates.map(c=>[c.attachmentId,{...c,relativePath:'research-inputs/'+c.attachmentId+'-'+c.sha256+'.csv'}]));
  const adoption={starterRunId:run.id,routeId:'tabular',researchTaskId:'task',researchIdea:'帮我分析一下葡萄酒数据',workflow:{workflow:plan.workflow,version:plan.versions[0]},initialInputs:{research_goal:'帮我分析一下葡萄酒数据',route_context:{routeId:'tabular'},input_paths:[],...(options.twoPorts?{mapping:[]}: {})}};
  return {plan,planner,detail,conversation,timeline,candidates,files,adoption,adopted:false,...options};
}

(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const bundle=await build({stdin:{contents:`import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<App/>);`,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const browser=await chromium.launch({channel:'msedge',headless:true});
  const errors=[];
  async function open(options={}) {
    const page=await browser.newPage({viewport:{width:1280,height:900}});
    page.on('pageerror',error=>{errors.push(error.message);console.error(error.message);});
    await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:css}); await page.addScriptTag({content:mock});
    await page.evaluate(value=>window.fixture=value,fixture(options));
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    await page.locator('.workflow-project-tasks article > button').first().click();
    try {
      if(!options.adopted) await page.getByRole('button',{name:'采用并补充数据',exact:true}).waitFor();
      else await page.getByText('已选路线 · 确认数据',{exact:true}).waitFor();
    } catch(error) {
      fs.writeFileSync(path.join(output,'open-failure.json'),JSON.stringify({...(await page.evaluate(()=>({text:document.body.innerText,calls:window.calls}))),errors},null,2));
      await page.screenshot({path:path.join(output,'open-failure.png')});throw error;
    }
    return page;
  }
  const calls=(page,method)=>page.evaluate(m=>window.calls.filter(c=>c.m===m),method);
  const checkbox=page=>page.getByRole('region',{name:'本任务已上传的数据'}).getByRole('checkbox');
  const start=page=>page.getByRole('button',{name:'开始执行所选路线',exact:true});
  const adopted=async page=>{
    await page.getByRole('button',{name:'采用并补充数据',exact:true}).click();
    await page.getByText('已选路线 · 确认数据',{exact:true}).waitFor();
  };
  try {
    const single=await open();
    assert.equal((await calls(single,'GetResearchRouteDataCandidates')).length,0);
    assert.equal((await calls(single,'StageResearchRouteData')).length,0);
    await adopted(single);
    await checkbox(single).waitFor();
    await single.waitForFunction(()=>document.querySelector('.research-route-uploaded-data input')?.checked);
    assert.equal((await calls(single,'AdoptResearchRoute')).length,1);
    assert.equal((await calls(single,'StartAdoptedResearchRoute')).length,0);
    assert.equal((await calls(single,'ChooseInputFileForResearchRoute')).length,0);
    assert.equal((await calls(single,'StageResearchRouteData')).length,1);
    assert.match(await single.locator('.research-start-panel').innerText(),/wine-red-initial-1000.csv[\s\S]*来自本任务对话/);
    assert.equal(await single.locator('.research-task-timeline .research-start-panel').count(),1,'confirmation remains in feed');
    for(const width of [1280,960]) {
      await single.setViewportSize({width,height:900});
      await single.waitForTimeout(120);
      const bounds=await single.evaluate(()=>{
        const card=document.querySelector('.research-start-panel').getBoundingClientRect(),row=document.querySelector('.research-route-uploaded-data label').getBoundingClientRect();
        return {overflow:document.documentElement.scrollWidth>innerWidth,card:{left:card.left,right:card.right},row:{left:row.left,right:row.right}};
      });
      assert.equal(bounds.overflow,false);assert.ok(bounds.row.left>=bounds.card.left && bounds.row.right<=bounds.card.right);
      await single.screenshot({path:path.join(output,`single-${width}.png`)});
    }
    await checkbox(single).click();
    await single.waitForFunction(()=>!document.querySelector('.research-route-uploaded-data input')?.checked, null, {timeout:3000});
    assert.equal(await checkbox(single).isChecked(),false);assert.equal(await start(single).isDisabled(),true);
    await single.waitForTimeout(1100);
    assert.equal(await checkbox(single).isChecked(),false,'unchecking must not trigger autofill again');
    await checkbox(single).click();
    await single.waitForFunction(()=>document.querySelector('.research-route-uploaded-data input')?.checked);
    await single.waitForFunction(()=>!document.querySelector('.research-launch-row > button')?.disabled);
    assert.equal((await calls(single,'StageResearchRouteData')).length,1,'reuse staged snapshot');
    await start(single).click();
    assert.equal((await calls(single,'StartAdoptedResearchRoute')).length,1);
    assert.deepEqual(await single.evaluate(()=>window.fixture.started.inputs.input_paths),['research-inputs/wine-'+'a'.repeat(64)+'.csv']);
    await single.close();

    const multi=await open({multi:true});await adopted(multi);await checkbox(multi).first().waitFor();
    assert.equal((await calls(multi,'StageResearchRouteData')).length,0);
    assert.equal(await checkbox(multi).first().isChecked(),false);assert.equal(await checkbox(multi).nth(1).isChecked(),false);
    await checkbox(multi).nth(1).click();await multi.waitForFunction(()=>document.querySelectorAll('.research-route-uploaded-data input')[1]?.checked);
    await multi.waitForFunction(()=>!document.querySelector('.research-launch-row > button')?.disabled);
    assert.equal(await checkbox(multi).first().isChecked(),false);
    await multi.screenshot({path:path.join(output,'multiple-files.png')});
    await start(multi).click();
    assert.deepEqual(await multi.evaluate(()=>window.fixture.started.inputs.input_paths),['research-inputs/other-'+'b'.repeat(64)+'.csv']);
    await multi.close();

    const restored=await open({adopted:true});
    await restored.waitForFunction(()=>document.querySelector('.research-route-uploaded-data input')?.checked);
    assert.equal((await calls(restored,'AdoptResearchRoute')).length,0,'existing pending route unchanged');
    assert.equal((await calls(restored,'StartAdoptedResearchRoute')).length,0);await restored.close();

    const failed=await open({adopted:true,stageFails:true});
    await failed.getByText(/无法带入已上传数据/).waitFor();
    await failed.getByRole('button',{name:'添加数据文件',exact:true}).click();
    await failed.getByText('manual.csv',{exact:true}).waitFor();await start(failed).click();
    assert.deepEqual(await failed.evaluate(()=>window.fixture.started.inputs.input_paths),['research-inputs/manual.csv']);await failed.close();

    const roles=await open({adopted:true,twoPorts:true});await checkbox(roles).first().waitFor();
    assert.equal(await checkbox(roles).count(),2);assert.equal((await calls(roles,'StageResearchRouteData')).length,0,'do not guess file role');
    assert.equal(await start(roles).isDisabled(),true);
    await checkbox(roles).first().click();
    await roles.waitForFunction(()=>!document.querySelector('.research-launch-row > button')?.disabled);
    assert.equal(await checkbox(roles).nth(1).isChecked(),false);
    await roles.screenshot({path:path.join(output,'optional-file-not-required.png')});
    await start(roles).click();
    assert.equal(await roles.evaluate(()=>Object.hasOwn(window.fixture.started.inputs,'mapping')),false,'omit unselected optional file');
    await roles.close();

    const both=await open({adopted:true,twoPorts:true});await checkbox(both).first().waitFor();
    await checkbox(both).first().click();await both.waitForFunction(()=>!document.querySelector('.research-launch-row > button')?.disabled);
    await checkbox(both).nth(1).click();await both.waitForFunction(()=>document.querySelectorAll('.research-route-uploaded-data input')[1]?.checked);
    await both.waitForFunction(()=>!document.querySelector('.research-launch-row > button')?.disabled);
    await start(both).click();
    assert.equal((await both.evaluate(()=>window.fixture.started.inputs.mapping)).length,1);
    await both.close();

    const cleared=await open({adopted:true,twoPorts:true,optionalDefault:true});await checkbox(cleared).first().waitFor();
    await checkbox(cleared).first().click();await cleared.waitForFunction(()=>!document.querySelector('.research-launch-row > button')?.disabled);
    await start(cleared).click();
    assert.deepEqual(await cleared.evaluate(()=>window.fixture.started.inputs.mapping),[],'explicit clear suppresses nonempty default');
    await cleared.close();

    for(const options of [{empty:true},{discoverFails:true}]) {
      const noData=await open({adopted:true,...options});
      await noData.getByRole('button',{name:'添加数据文件',exact:true}).waitFor();
      await noData.waitForFunction(()=>!document.querySelector('.research-start-panel .workflow-multi-input > button')?.disabled);
      assert.equal((await calls(noData,'StageResearchRouteData')).length,0);
      assert.equal(await checkbox(noData).count(),0);
      assert.equal(await start(noData).isDisabled(),true);
      if(options.discoverFails) await noData.getByText(/无法带入已上传数据/).waitFor();
      await noData.close();
    }

    const delayed=await open({adopted:true,stageDelayed:true});
    await delayed.waitForFunction(()=>typeof window.finishStage==='function');
    assert.equal(await start(delayed).isDisabled(),true);
    await delayed.getByRole('button',{name:'返回研究方案',exact:true}).click();
    await delayed.getByRole('heading',{name:'你想研究什么？',exact:true}).waitFor();
    await delayed.evaluate(()=>window.finishStage());await delayed.waitForTimeout(100);
    assert.equal(await delayed.locator('.research-start-panel').count(),0);
    assert.equal((await calls(delayed,'StartAdoptedResearchRoute')).length,0);await delayed.close();
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(output,'verification.json'),JSON.stringify({singleAutofill:true,explicitRouteAndStart:true,noDuplicatePicker:true,removableSelection:true,multipleNoMerge:true,pendingRestoration:true,nativePickerFallback:true,emptyOrLookupFailureNonblocking:true,multipleRolesNoGuess:true,optionalFileOmitted:true,selectedOptionalFileRetained:true,clearedOptionalDefaultNotRestored:true,staleResponseIgnored:true,widths:[1280,960],errors},null,2));
    console.log('PASS: actual App route adoption → existing-upload confirmation → explicit start; single/multiple/removal/restoration/fallback/roles/stale UI checked');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1});
