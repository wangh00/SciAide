// Actual React UI + controlled facade fixtures. No real model or user database.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const repairOutput = path.resolve(process.env.SCIAIDE_REPAIR_UI_OUTPUT || '.tmp/research-selection-ui-20261007');
const guideOutput = path.resolve(process.env.SCIAIDE_GUIDE_UI_OUTPUT || '.tmp/research-guide');

(async () => {
  const bundle = await build({stdin: {contents: `import React from 'react'; import {createRoot} from 'react-dom/client'; import App from './App'; createRoot(document.getElementById('root')).render(<App/>);`,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const mock = `if(!crypto.randomUUID) crypto.randomUUID=()=> 'fixture-client-message'; window.calls=[]; window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
    window.calls.push({f,m,args});
    if(window.detail) {
      if(m==='ListProjectRuns') return [window.detail.run];
      if(m==='Get') return window.owner;
      if(m==='List' && f==='WorkflowFacade') return [window.owner.workflow];
      if(m==='GetRun') return window.detail;
      if(m==='PollRun') return {revision:'1',unchanged:false,value:window.detail};
      if(m==='GetConversation') return window.conversation;
      if(m==='ListConversations') return [window.conversation];
      if(m==='ResearchTimeline') return window.timeline;
      if(m==='PollResearchTimeline') return {revision:'1',unchanged:false,value:window.timeline};
      if(m==='GetLatestRunSnapshot') return null;
      if(m==='PollLatestRunSnapshot') return {revision:'1',unchanged:false,value:null};
      if(m==='ListResearchRevisionProposals') return window.proposals || [];
      if(m==='ChooseAndImportConversationDocuments') return {attachments:[{id:'repair-attachment',originalName:'supplement.txt',mimeType:'text/plain',format:'text',sizeBytes:30,status:'ready'}],errors:[]};
      if(m==='StartChat') throw Error('Fixture captured repair discussion; no model called');
      if(m==='Decide') return window.detail;
      if(m==='Retry') {
        const selection=window.detail.steps.find(step=>step.nodeId==='evidence_extraction');
        selection.attempt++; selection.status='waiting_human_confirmation'; selection.input=window.selectionInput;
        window.detail.run.status='waiting_human_confirmation'; window.detail.run.currentStep=selection.ordinal;
        window.detail.events.push({id:'selection-event',type:'workflow.human_confirmation_requested',payload:{stepId:selection.id}});
        window.timeline.entries=[{id:'event:selection-event',sequence:'2',kind:'event',eventType:'workflow.human_confirmation_requested',runId:'r',conversationId:'c',active:true,step:selection,snapshot:{event:{stepId:selection.id}}}];
        return window.detail;
      }
    }
    if(m==='ListProjects') return [{id:'p',name:'科研入口测试',workspacePath:'D:/Fixture',workspaceKind:'external'}];
    if(m==='ListModelProfiles') return [{id:'model1',name:'Fixture',models:[{id:'fixture-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,reasoningLevels:[],enabled:true,isDefault:true}],secretConfigured:true,enabled:true,isDefault:true,modelId:'fixture-model',customHeaders:{},apiProtocol:'openai_chat_completions',baseUrl:'http://fixture.invalid'}];
    if(m==='ListProjects'||m.startsWith('List')) return [];
    if(m==='StartResearch') throw Error('Fixture captured planning request; no model called');
    if(m==='GetLatestRunSnapshot') return null;
    if(m==='GetNetworkConfig') return {global:{mode:'direct',url:''},modules:{},revision:0};
    return {};
  }})})};window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:()=>{},OnFileDropOff:()=>{}};`;
  const browser = await chromium.launch({channel:'msedge',headless:true});
  try {
    const page = await browser.newPage({viewport:{width:1280,height:900}});
    const errors = []; page.on('pageerror', error => {errors.push(error.message);console.error(error.message);});
    await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:['styles.css','referenceMaterials.css','modal.css'].map(file=>fs.readFileSync(`frontend/src/${file}`,'utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n')});
    await page.addScriptTag({content:mock});
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    const question = page.getByRole('textbox',{name:'研究问题',exact:true});
    await question.waitFor();
    await page.getByRole('button',{name:/^文献比较/}).click();
    assert.match(await question.inputValue(),/带原文定位的比较表/);
    assert.equal(await page.evaluate(()=>window.calls.filter(call=>call.m==='StartResearch').length),0);
    await question.fill('我自己的研究目标');
    await page.getByRole('button',{name:/^数据分析/}).click();
    assert.ok((await question.inputValue()).startsWith('我自己的研究目标\n\n'));
    const combined = await question.inputValue();
    await page.getByRole('button',{name:/^数据分析/}).click();
    assert.equal(await question.inputValue(),combined);
    // The normal length limit must not silently erase user text.
    await question.fill('问'.repeat(7950));
    await page.getByRole('button',{name:/^数据分析/}).click();
    assert.equal(await question.inputValue(),'问'.repeat(7950));
    await page.getByText('当前问题较长，无法追加完整示例；原内容保持不变，请自行编辑。',{exact:true}).waitFor();
    await question.fill('请分析我的表格，只做描述统计。');
    await page.getByRole('button',{name:'生成研究路线',exact:true}).click();
    await page.getByText('Fixture captured planning request; no model called',{exact:false}).waitFor();
    const commands = await page.evaluate(()=>window.calls.filter(call=>call.m==='StartResearch'));
    assert.equal(commands.length,1);
    assert.equal(commands[0].args[0].researchIdea,'请分析我的表格，只做描述统计。');
    assert.deepEqual(commands[0].args[0].referenceAttachmentIds,[]);
    fs.mkdirSync(guideOutput,{recursive:true});
    for (const width of [1280,960]) {
      await page.setViewportSize({width,height:900});
      await page.getByRole('region',{name:'科研对话示例'}).scrollIntoViewIfNeeded();
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth > innerWidth),false);
      assert.equal(await page.locator('.workflow-plan-home').evaluate(element=>element.scrollWidth>element.clientWidth),false);
      await page.screenshot({path:path.join(guideOutput,`entry-${width}.png`)});
    }
    // Open a report-free, reviewed analysis through the original task list.
    const analysisPage = await browser.newPage({viewport:{width:1280,height:900}});
    analysisPage.on('pageerror',error=>errors.push(error.message));
    await analysisPage.setContent('<div id="root"></div>');
    await analysisPage.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
    await analysisPage.addScriptTag({content:mock});
    await analysisPage.evaluate(() => {
      const nodes = [{id:'data_preflight',name:'预检',kind:'tool'},{id:'python_analysis',name:'计算',kind:'python'},{id:'result_interpretation',name:'解释',kind:'agent_stage'}];
      const definition = {schemaVersion:1,name:'已完成表格分析',inputs:[],nodes,edges:[],outputs:[]};
      window.owner = {workflow:{id:'w',projectId:'p',name:'已完成表格分析',currentVersionId:'v',version:1},versions:[{id:'v',workflowId:'w',definition,compilation:{nodes,edges:[],outputs:[]}}]};
      window.conversation = {id:'c',projectId:'p',title:'数据分析任务',permissionMode:'full_access',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium'};
      window.detail = {run:{id:'r',workflowId:'w',workflowVersionId:'v',projectId:'p',researchTaskId:'t',workflowName:'已完成表格分析',workflowPurpose:'user_plan',conversationId:'c',status:'completed',createdAt:'2026-09-30T01:00:00Z',inputs:{},outputs:{delivery_gate:{approved:true},interpretation:{summary:'合计为 3，来自本次计算。',computedFacts:['合计=3'],inferences:['仅为演示'],alternativeExplanations:[],methodChecks:[],limitations:['演示数据不是科研样本'],confidence:'low'}},compilation:{nodes,edges:[],outputs:[]}},steps:[],events:[{id:'e',type:'workflow.completed',payload:{},createdAt:'2026-09-30T01:01:00Z'}],pendingApprovals:[],aiExecutions:[],artifactCount:0,registeredDeliverables:[],deliveryAssessment:{status:'reviewed'}};
      window.timeline = {taskId:'t',latestRunId:'r',conversationId:'c',hasMore:false,entries:[{id:'event:e',sequence:'1',kind:'event',eventType:'workflow.completed',runId:'r',conversationId:'c',createdAt:'2026-09-30T01:01:00Z',active:true,snapshot:{event:{},outputs:window.detail.run.outputs}}]};
    });
    await analysisPage.addScriptTag({content:bundle.outputFiles[0].text});
    await analysisPage.getByRole('button',{name:'科研模式',exact:true}).click();
    await analysisPage.locator('.workflow-project-tasks article > button').first().click();
    await analysisPage.getByRole('region',{name:'本次数据分析结果'}).waitFor();
    await analysisPage.getByRole('region',{name:'在原对话继续研究'}).waitFor();
    await analysisPage.getByRole('button',{name:'更新数据',exact:true}).click();
    assert.match(await analysisPage.locator('.composer textarea').inputValue(),/完整文件集合/);
    assert.equal(await analysisPage.evaluate(()=>window.calls.filter(call=>['StartChat','ConfirmResearchRevision','Start'].includes(call.m)).length),0);
    await analysisPage.getByRole('button',{name:'比较另一种方法',exact:true}).click();
    assert.match(await analysisPage.locator('.composer textarea').inputValue(),/完整文件集合[\s\S]*当前实际采用/);
    assert.equal(await analysisPage.getByRole('button',{name:'补充文献',exact:true}).count(),0);
    await analysisPage.screenshot({path:path.join(guideOutput,'analysis-followup.png')});
    // Actual App repair entry on a verified rejected task. All identities and
    // facade results are synthetic; no formal task/database/model is used.
    const repairPage = await browser.newPage({viewport:{width:1280,height:900}});
    repairPage.on('pageerror',error=>errors.push(error.message));
    await repairPage.setContent('<div id="root"></div>');
    await repairPage.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
    await repairPage.addScriptTag({content:mock});
    await repairPage.evaluate(() => {
      const nodes = [{id:'evidence_screening',name:'综合文献证据',kind:'agent_stage'},{id:'evidence_extraction',name:'确认采用引用',kind:'citation_selection'},{id:'independent_review',name:'独立二次审查',kind:'agent_stage'},{id:'delivery_gate',name:'核验交付条件',kind:'tool',tool:{qualifiedName:'builtin.research.workflow.review.gate'}}];
      const edges=[{fromNode:'independent_review',fromPort:'analysis',toNode:'delivery_gate',toPort:'review'}];
      const definition={schemaVersion:1,name:'审查拒绝返修夹具',inputs:[],nodes,edges,outputs:[]};
      window.owner={workflow:{id:'w',projectId:'p',name:definition.name,currentVersionId:'v',version:1},versions:[{id:'v',workflowId:'w',definition,compilation:{nodes,edges:[],outputs:[]}}]};
      window.conversation={id:'c',projectId:'p',title:'审查拒绝任务',permissionMode:'full_access',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium'};
      const candidates=Array.from({length:64},(_,i)=>({id:`source-${i}`,reference:`[K-FIXTURE${i}]`,documentId:`doc-${i%2}`,sourceName:`source-${i%2}.pdf`,mimeType:'application/pdf',quote:`Fixture source passage ${i}`,locator:`page ${i+1}`}));
      const screening={summary:'已接受的证据综合',recommendedReferences:candidates.slice(0,29).map(ref=>ref.reference),citationAssessments:[],coverage:{strength:'adequate',sufficientForClaimedScope:true,gaps:[]}};
      const correction='比较表第（4）行虽然已经写入两段原文句子，但它们仍未进入本轮确认引用。请采用已接受综合中的两段定义性摘录，保留短摘录和页码，更新比较表并核对原始五个问题；不能把无确认引用的核心格当作完成。';
      const reviewOutput={approved:false,requiredCorrections:[correction],methodIssues:[correction],reviewFindings:[{id:'issue-2-1',status:'open',origin:'existing',summary:correction,location:'比较表第（4）行',reason:'两段仍未采用',changeReason:'上轮缺口仍未解决'}]};
      window.selectionInput={candidates,screening,previousSelectedReferences:candidates.slice(0,15).map(ref=>ref.reference),citationRepairContext:{reviewOutputSha256:'review-hash',requiredCorrections:[correction],mentionedReferences:[candidates[18].reference,candidates[19].reference],repeatedIssueCount:1}};
      const gate={id:'gate-step',nodeId:'delivery_gate',ordinal:3,attempt:1,nodeKind:'tool',status:'failed',input:{},output:{},errorCode:'RESEARCH_REVIEW_NOT_APPROVED',errorMessage:'尚有核心比较缺口'};
      window.detail={run:{id:'r',workflowId:'w',workflowVersionId:'v',projectId:'p',researchTaskId:'t',workflowName:definition.name,workflowPurpose:'user_plan',conversationId:'c',status:'failed',currentStep:3,createdAt:'2026-10-05T01:00:00Z',inputs:{},outputs:{},compilation:{nodes,edges,outputs:[]}},steps:[{id:'screen-step',nodeId:'evidence_screening',ordinal:0,attempt:9,nodeKind:'agent_stage',status:'completed',input:{},output:{}},{id:'selection-step',nodeId:'evidence_extraction',ordinal:1,attempt:1,nodeKind:'citation_selection',status:'completed',input:{candidates:candidates.slice(0,15),screening},output:{citations:candidates.slice(0,15)}},{id:'review-step',nodeId:'independent_review',ordinal:2,attempt:4,nodeKind:'agent_stage',status:'completed',input:{},output:{analysis:reviewOutput}},gate],events:[{id:'failure-event',type:'workflow.failed',payload:{stepId:gate.id}}],pendingApprovals:[],aiExecutions:[{workflowStepId:'review-step',attempt:4,status:'completed',output:reviewOutput}],artifactCount:0,registeredDeliverables:[],discussionRevisionAvailable:true,evidenceReselection:{nodeId:'evidence_extraction',availableCount:64,recommendedCount:29,selectedRecommendedCount:15,missingRecommendedCount:14,previouslyUnofferedCount:14,reviewOutputSha256:'review-hash'},reviewRevisionTargets:[{nodeId:'evidence_extraction',label:'确认采用引用',repeatsSideEffects:false}],reviewRevisionRecommendation:{status:'needs_input',summary:'先确认已有证据',requiredInputs:['核查原目标中的全部核心比较'],reviewOutputSha256:'review-hash',repeatsSideEffects:false}};
      window.timeline={taskId:'t',latestRunId:'r',conversationId:'c',hasMore:false,entries:[{id:'event:failure-event',sequence:'1',kind:'event',eventType:'workflow.failed',runId:'r',conversationId:'c',active:true,step:gate,snapshot:{event:{stepId:gate.id}}}]};
      window.initialRepairFixture=structuredClone({owner:window.owner,conversation:window.conversation,detail:window.detail,timeline:window.timeline,selectionInput:window.selectionInput});
    });
    await repairPage.addScriptTag({content:bundle.outputFiles[0].text});
    await repairPage.getByRole('button',{name:'科研模式',exact:true}).click();
    await repairPage.locator('.workflow-project-tasks article > button').first().click();
    const direct=repairPage.getByRole('button',{name:'打开引用补选',exact:true});
    await direct.waitFor();
    for(const name of ['让 AI 整理返修方案','复制补充要求','重新选择已有证据','先讨论完整返修方案']) assert.equal(await repairPage.getByRole('button',{name,exact:true}).count(),0);
    await repairPage.getByText(/14 条没有出现在旧选择列表/).waitFor();
    const composer=repairPage.locator('.composer textarea');
    await composer.fill('保留我的草稿');
    await repairPage.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    await repairPage.locator('.composer-wrap').getByText('supplement.txt').waitFor();
    fs.mkdirSync(repairOutput,{recursive:true});
    await repairPage.getByText(/有 1 项既有问题尚未解决/).waitFor();
    await repairPage.screenshot({path:path.join(repairOutput,'direct-action.png')});
    await direct.click();
    assert.equal(await composer.inputValue(),'保留我的草稿');
    await repairPage.locator('.composer-wrap').getByText('supplement.txt').waitFor();
    assert.equal(await repairPage.evaluate(()=>window.calls.filter(call=>['StartChat','ConfirmResearchRevision','Start'].includes(call.m)).length),0);
    await repairPage.getByText('已选 15 / 64',{exact:true}).waitFor();
    assert.equal(await repairPage.locator('.workflow-selection-list input:checked').count(),15);
    await repairPage.getByText(/引用选择尚未变化/).waitFor();
    const confirm=repairPage.getByRole('button',{name:/^(仍按原|确认所选) \d+ 条引用/});
    assert.match(await confirm.textContent(),/^仍按原 15 条引用/);
    assert.equal(await repairPage.locator('.citation-repair-toolbar').getByRole('button',{name:'补入审查提及引用（2）',exact:true}).count(),1);
    await repairPage.getByRole('checkbox',{name:/以有限证据继续/}).click();
    await confirm.waitFor({state:'visible'});
    assert.equal(await confirm.isEnabled(),true);
    // Warn about unchanged choices, but do not impose a new scientific gate.
    await confirm.click();
    const unchangedDecision=await repairPage.evaluate(()=>window.calls.filter(call=>call.m==='Decide'));
    assert.equal(unchangedDecision.length,1);assert.equal(unchangedDecision[0].args[0].context.length,15);
    assert.equal(await repairPage.locator('.workflow-selection-list > label').first().getByText('[K-FIXTURE18]',{exact:false}).count(),1);
    for(const width of [1280,960]) {
      await repairPage.setViewportSize({width,height:900});
      await repairPage.getByRole('region',{name:'本轮引用补选'}).scrollIntoViewIfNeeded();
      assert.equal(await repairPage.locator('.research-interaction-card.decision').evaluate(el=>el.scrollWidth>el.clientWidth),false);
      assert.equal(await repairPage.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await repairPage.screenshot({path:path.join(repairOutput,`selection-unchanged-${width}.png`)});
      await repairPage.locator('.citation-repair-toolbar').scrollIntoViewIfNeeded();
      assert.equal(await repairPage.locator('.citation-repair-toolbar').evaluate(el=>el.scrollWidth>el.clientWidth),false);
      await repairPage.screenshot({path:path.join(repairOutput,`selection-toolbar-${width}.png`)});
    }
    await repairPage.getByRole('button',{name:'补入审查提及引用（2）',exact:true}).click();
    await repairPage.getByText('已选 17 / 64',{exact:true}).waitFor();
    await repairPage.getByText(/新增 2 条、移除 0 条/).waitFor();
    assert.match(await confirm.textContent(),/^确认所选 17 条引用/);
    assert.equal(await repairPage.getByRole('button',{name:'审查提及引用已选入',exact:true}).isDisabled(),true);
    assert.equal(await repairPage.evaluate(()=>window.calls.filter(call=>call.m==='Decide').length),1);
    // Keep a user's non-recommended choice when supplementing recommendations.
    const customChoice=repairPage.locator('.workflow-selection-list > label').filter({hasText:'[K-FIXTURE50]'});
    await customChoice.scrollIntoViewIfNeeded();
    await repairPage.screenshot({path:path.join(repairOutput,'custom-choice-before.png')});
    await customChoice.getByRole('checkbox').click();
    await repairPage.getByText('已选 18 / 64',{exact:true}).waitFor();
    assert.equal(await customChoice.getByRole('checkbox').isChecked(),true);
    await repairPage.getByRole('button',{name:'补入未选推荐（12）',exact:true}).click();
    await repairPage.getByText('已选 30 / 64',{exact:true}).waitFor();
    assert.equal(await repairPage.locator('.workflow-selection-list > label').filter({hasText:'[K-FIXTURE50]'}).getByRole('checkbox').isChecked(),true);
    await confirm.click();
    const supplemented=await repairPage.evaluate(()=>window.calls.filter(call=>call.m==='Decide').at(-1).args[0].context);
    assert.equal(supplemented.length,30);
    assert.ok(supplemented.some(ref=>ref.reference==='[K-FIXTURE50]'));
    assert.deepEqual(supplemented,await repairPage.evaluate(()=>window.selectionInput.candidates.filter(ref=>Number(ref.id.split('-')[1])<29||ref.id==='source-50')));
    await repairPage.getByRole('button',{name:'替换为 AI 推荐',exact:true}).click();
    await repairPage.getByText('已选 29 / 64',{exact:true}).waitFor();
    const retries=await repairPage.evaluate(()=>window.calls.filter(call=>call.m==='Retry'));
    assert.equal(retries.length,1);assert.equal(retries[0].args[0].revisionNodeId,'evidence_extraction');assert.equal(retries[0].args[0].expectedReviewSha256,'review-hash');
    assert.equal(await repairPage.evaluate(()=>window.detail.steps[0].attempt),9);
    assert.equal(await repairPage.evaluate(()=>window.calls.filter(call=>['ConfirmResearchRevision','StartChat','Start'].includes(call.m)).length),0);
    await repairPage.screenshot({path:path.join(repairOutput,'reselection-29.png')});
    const currentRepairPage=await browser.newPage({viewport:{width:1280,height:900}});
    currentRepairPage.on('pageerror',error=>errors.push(error.message));
    await currentRepairPage.setContent('<div id="root"></div>');
    await currentRepairPage.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
    await currentRepairPage.addScriptTag({content:mock});
    const fixture=await repairPage.evaluate(()=>window.initialRepairFixture);
    await currentRepairPage.evaluate(value=>{
      Object.assign(window,value);
      window.detail.evidenceReselection.previouslyUnofferedCount=0;
      window.detail.steps[1].input.candidates=window.selectionInput.candidates;
      window.detail.reviewRevisionRecommendation={status:'recommended',nodeId:'evidence_extraction',label:'确认采用引用',summary:'采用已接受的原文摘录，再更新比较表和报告。',reasons:['同一引用缺口仍未解决，先调整采用集合。'],affectedStages:['确认采用引用','综合方法','形成报告','独立审查','交付核验'],reviewOutputSha256:'review-hash',repeatsSideEffects:false};
    },fixture);
    await currentRepairPage.addScriptTag({content:bundle.outputFiles[0].text});
    await currentRepairPage.getByRole('button',{name:'科研模式',exact:true}).click();
    await currentRepairPage.locator('.workflow-project-tasks article > button').first().click();
    const currentDirect=currentRepairPage.getByRole('button',{name:'打开引用补选',exact:true});
    await currentDirect.waitFor();
    assert.equal(await currentRepairPage.getByRole('button',{name:'按建议修改',exact:true}).count(),0);
    await currentRepairPage.getByText(/有 1 项既有问题尚未解决/).scrollIntoViewIfNeeded();
    await currentRepairPage.screenshot({path:path.join(repairOutput,'review-repeat-current.png')});
    await currentDirect.click();
    await currentRepairPage.getByText(/引用选择尚未变化/).waitFor();
    await currentRepairPage.getByRole('button',{name:'补入审查提及引用（2）',exact:true}).click();
    await currentRepairPage.getByText('已选 17 / 64',{exact:true}).waitFor();
    const currentRetry=await currentRepairPage.evaluate(()=>window.calls.filter(call=>call.m==='Retry'));
    assert.equal(currentRetry.length,1);assert.equal(currentRetry[0].args[0].useRecommendation,true);
    assert.equal(await currentRepairPage.evaluate(()=>window.calls.filter(call=>['Decide','StartChat'].includes(call.m)).length),0);
    assert.deepEqual(errors,[]);
    console.log('PASS: renamed citation-open action; count-adjacent supplement; explicit unchanged-count confirmation; actual App facade fixture; one direct Retry, no extra chat/proposal; repeat issue visible; original 15/64 unchanged warning does not block confirmation; explicit 2-passage/remaining-recommendation supplements preserve custom choice and exact snapshots; 1280/960 no overflow; screening stays attempt 9. No model/native EXE/formal DB.');
  } finally { await browser.close(); }
})().catch(error => {console.error(error);process.exit(1);});
