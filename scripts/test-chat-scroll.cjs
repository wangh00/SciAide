// Actual App / React / production styles; isolated facades, never a live task.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve(process.env.SCIAIDE_SCROLL_OUTPUT || '.tmp/chat-scroll');

function syntheticFixture() {
  const date = '2026-10-14T08:00:00Z';
  const citations = Array.from({length:45}, (_, i) => ({id:`source-${i}`, documentId:`document-${i}`,
    reference:`[K-FIXTURE${i}]`, sourceName:`study-${i}-metadata.md`, locator:'lines:1-25', mimeType:'text/markdown',
    quote:`Study ${i}\nEvidence disclosure: metadata, not the publication full text.\n${'A source excerpt for scrolling. '.repeat(35)}`}));
  const screening = {summary:'这些材料能够支持有限综述，需要核对引用及证据边界。'.repeat(5),
    recommendedReferences:citations.slice(0,8).map(c=>c.reference),
    citationAssessments:citations.map((c,i)=>({reference:c.reference,decision:i<8?'support':'exclude',sourceLevel:'metadata',reason:'核对材料来源、研究对象及结局。'.repeat(3)})),
    coverage:{strength:'limited',sufficientForClaimedScope:false,gaps:['部分材料只有摘要','缺少全文与完整组间估计'],independentStudyEstimate:6}};
  const nodes = [{id:'evidence_search',name:'核读本地材料',kind:'tool'}, {id:'evidence_screening',name:'综合入选文献证据',kind:'ai_analysis'},
    {id:'evidence_extraction',name:'提取可信证据',kind:'citation_selection',prompt:'请选择报告使用的可信本地证据。'}];
  const compilation = {schemaVersion:1,compilerVersion:'fixture',nodes,order:nodes.map(n=>n.id),edges:[],inputs:[],outputs:[],diagnostics:[]};
  const run = {id:'run',researchTaskId:'task',conversationId:'conversation',projectId:'project',workflowId:'workflow',workflowVersionId:'version',
    workflowName:'证据滚动回归',workflowPurpose:'user_plan',status:'waiting_human_confirmation',permissionMode:'full_access',inputs:{research_goal:'核验有限文献证据'},outputs:{},
    compilation,currentStep:2,createdAt:date,updatedAt:date};
  const steps = nodes.map((n,i)=>({id:'step-'+n.id,workflowRunId:run.id,nodeId:n.id,nodeKind:n.kind,ordinal:i,attempt:1,
    status:i===2?'waiting_human_confirmation':'completed',input:i===2?{candidates:citations,screening}:{},output:{},updatedAt:date}));
  const event = {id:'checkpoint',sequence:151,type:'workflow.human_confirmation_requested',payload:{stepId:steps[2].id},createdAt:date};
  const rows = Array.from({length:150},(_,i)=>({id:`message:history-${i}`,sequence:String(i+1),runId:run.id,conversationId:run.conversationId,kind:'message',createdAt:date,active:false,snapshot:{},
    message:{id:`history-${i}`,conversationId:run.conversationId,role:'assistant',status:'complete',parts:[{type:'text',text:`历史研究记录 ${i}\n\n${'材料解释与讨论。'.repeat(90)}`}],createdAt:date,citations:[]}}));
  rows.push({id:'event:checkpoint',sequence:'151',runId:run.id,conversationId:run.conversationId,kind:'event',eventType:event.type,createdAt:date,active:true,step:steps[2],snapshot:{event:event.payload}});
  return {detail:{run,steps,events:[event],pendingApprovals:[],aiExecutions:[],aiActivities:[],toolActivities:[],artifactCount:0},
    plan:{workflow:{id:'workflow',projectId:'project',purpose:'user_plan',name:run.workflowName,currentVersionId:'version',version:1,createdAt:date},
      versions:[{id:'version',workflowId:'workflow',version:1,definition:{schemaVersion:1,name:run.workflowName,nodes,edges:[],inputs:[],outputs:[]},compilation}]},
    conversation:{id:run.conversationId,projectId:run.projectId,title:run.workflowName,modelProfileId:'profile',modelId:'fixture-model',permissionMode:'full_access',reasoningLevel:'medium'},
    timeline:{taskId:'task',latestRunId:run.id,conversationId:run.conversationId,entries:rows,hasMore:false},messages:[],latestSnapshot:null};
}

// Reads emulate conditional polling. Mutation methods are rejected except for
// the explicit fixture-only confirmation check at the very end of each page.
const mock = `
window.calls=[];window.allowFixtureDecision=false;window.scrollWrites=[];window.runSubscribers=new Set();
const revision=value=>{let h=2166136261;const text=JSON.stringify(value);for(let i=0;i<text.length;i++)h=Math.imul(h^text.charCodeAt(i),16777619);return (h>>>0).toString(16)};
const conditional=(value,prior)=>{const next=revision(value);return next===prior?{revision:next,unchanged:true}:{revision:next,unchanged:false,value}};
const timeline=query=>{const s=window.fixture,all=s.timeline.entries;let rows=all.filter(v=>(!query.after||BigInt(v.sequence)>BigInt(query.after))&&(!query.before||BigInt(v.sequence)<BigInt(query.before)));const n=query.limit||50,hasMore=rows.length>n;return {...s.timeline,entries:query.after?rows.slice(0,n):rows.slice(-n),hasMore,nextBefore:rows[0]?.sequence}};
window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
  window.calls.push({f,m,args});const s=window.fixture,p=s.detail.run.projectId;let result={};
  if(m==='Decide'&&window.allowFixtureDecision)return await new Promise(resolve=>window.finishFixtureDecision=resolve);
  if(['Start','StartChat','StartResearch','Decide','Retry','ConfirmResearchRevision','RespondToUserInput','CancelRun'].includes(m))throw Error('Unexpected execution: '+m);
  if(m==='ListProjects')result=[{id:p,name:'滚动回归夹具',workspacePath:'D:/Fixture',workspaceKind:'external'}];
  else if(m==='ListModelProfiles')result=[{id:s.conversation.modelProfileId,name:'Fixture',models:[{id:s.conversation.modelId,contextWindowTokens:128000,autoCompactTokenLimit:100000,reasoningLevels:[],enabled:true,isDefault:true}],secretConfigured:true,enabled:true,isDefault:true,modelId:s.conversation.modelId,customHeaders:{},apiProtocol:'openai_responses',baseUrl:'http://fixture.invalid'}];
  else if(f==='WorkflowFacade'&&m==='List')result=[s.plan.workflow];
  else if(m==='ListProjectRuns')result=[s.detail.run];
  else if(f==='WorkflowFacade'&&m==='Get')result=s.plan;
  else if(['GetRun','GetCurrentResearchTaskRun'].includes(m))result=s.detail;
  else if(m==='GetRunByConversation')result={found:true,run:s.detail};
  else if(m==='GetPendingResearchAdoption')result=null;
  else if(m==='PollRun')result=conditional(s.detail,args.at(-1));
  else if(m==='GetConversation')result=s.conversation;
  else if(m==='ListConversations')result=[s.conversation];
  else if(m==='ListMessages')result=s.messages||[];
  else if(m==='ResearchTimeline')result=timeline(args[0]);
  else if(m==='PollResearchTimeline')result=conditional(timeline(args[0]),args.at(-1));
  else if(['GetLatestRunSnapshot','GetRunSnapshot'].includes(m))result=s.latestSnapshot||null;
  else if(['PollLatestRunSnapshot','PollRunSnapshot'].includes(m))result=conditional(s.latestSnapshot||null,args.at(-1));
  else if(m==='GetNetworkConfig')result={global:{mode:'direct',url:''},modules:{},revision:0};
  else if(m.startsWith('List'))result=[];
  return structuredClone(result);
}})})};
window.runtime={EventsOnMultiple:(event,callback)=>{if(event==='sciaide:run-event')window.runSubscribers.add(callback);return ()=>window.runSubscribers.delete(callback)},OnFileDrop:()=>{},OnFileDropOff:()=>{}};
const scrollTo=Element.prototype.scrollTo;Element.prototype.scrollTo=function(...args){if(this.matches('.chat'))window.scrollWrites.push({top:this.scrollTop,target:args[0],t:performance.now()});return scrollTo.apply(this,args)};
window.appendFixture=()=>{const s=window.fixture,id='append-'+(s.timeline.entries.length+1),sequence=String(BigInt(s.timeline.entries.at(-1).sequence)+1n);s.timeline.entries.push({id:'message:'+id,sequence,runId:s.detail.run.id,conversationId:s.conversation.id,kind:'message',active:false,snapshot:{},createdAt:new Date().toISOString(),message:{id,conversationId:s.conversation.id,role:'assistant',status:'complete',citations:[],parts:[{type:'text',text:'新增材料说明。'.repeat(80)}],createdAt:new Date().toISOString()}});window.dispatchEvent(new CustomEvent('research-message-saved',{detail:{projectId:s.detail.run.projectId,taskId:s.detail.run.researchTaskId}}));return 'message:'+id};
`;

(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const captured = process.env.SCIAIDE_SCROLL_FIXTURE;
  const fixture = captured ? JSON.parse(fs.readFileSync(captured,'utf8')) : syntheticFixture();
  const css = process.env.SCIAIDE_PRODUCTION_CSS==='1'
    ? fs.readdirSync('frontend/dist/assets').filter(n=>n.endsWith('.css')).map(n=>fs.readFileSync('frontend/dist/assets/'+n,'utf8')).join('\n')
    : ['styles.css','referenceMaterials.css','modal.css'].map(n=>fs.readFileSync('frontend/src/'+n,'utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n');
  const bundle = await build({stdin:{contents:"import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<React.StrictMode><App/></React.StrictMode>);",loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const productionJS=process.env.SCIAIDE_PRODUCTION_JS==='1';
  const code=productionJS ? fs.readdirSync('frontend/dist/assets').filter(n=>n.endsWith('.js')).map(n=>fs.readFileSync('frontend/dist/assets/'+n,'utf8')).join('\n') : bundle.outputFiles[0].text;
  const browser = await chromium.launch({channel:'msedge',headless:true});
  const errors=[],results=[];let page;
  try {
    for (const size of [{width:1280,height:900},{width:960,height:720}]) {
      page=await browser.newPage({viewport:size});page.on('pageerror',e=>errors.push(e.message));
      await page.route('**/*',route=>route.abort());await page.setContent('<!doctype html><div id="root"></div>');
      await page.addStyleTag({content:css});await page.evaluate(v=>window.fixture=v,structuredClone(fixture));await page.addScriptTag({content:mock});await page.addScriptTag({content:code,type:productionJS?'module':undefined});
      await page.getByRole('button',{name:'科研模式',exact:true}).click();await page.locator('.workflow-project-tasks article > button').first().click();
      const card=page.locator('.research-interaction-card.decision'),chat=page.locator('.chat'),list=card.locator('.workflow-selection-list');
      await card.waitFor();await page.waitForTimeout(1800);
      const metrics=()=>chat.evaluate(c=>({top:c.scrollTop,height:c.clientHeight,total:c.scrollHeight,bottom:c.scrollHeight-c.clientHeight-c.scrollTop,writes:window.scrollWrites.length}));
      const bottom=await metrics();assert.ok(bottom.bottom<=2,'initial card does not follow');
      assert.ok(await card.evaluate(e=>e.getBoundingClientRect().height>document.querySelector('.chat').clientHeight),'fixture card must exceed the viewport');
      await card.locator('button.accept').hover();await page.evaluate(()=>window.scrollWrites=[]);
      await page.mouse.wheel(0,-40);await page.waitForTimeout(1400);const first=await metrics();
      assert.ok(first.top<bottom.top-25,'one small upward wheel was pulled back');assert.equal(first.writes,0,'automatic follow fought upward reading');
      // Once explicitly reading, repeated native scrolls can reach the card heading.
      const rect=await chat.boundingBox();await page.mouse.move(rect.x+7,rect.y+rect.height/2);
      for(let i=0;i<12;i++){await page.mouse.wheel(0,-80);await page.waitForTimeout(40)}await page.waitForTimeout(300);
      const position=await card.evaluate(e=>({top:e.getBoundingClientRect().top,chatTop:document.querySelector('.chat').getBoundingClientRect().top}));
      assert.ok(position.top>=position.chatTop-2,'card heading remains inaccessible');assert.equal((await metrics()).writes,0);
      await page.screenshot({path:path.join(output,`card-heading-${size.width}.png`)});
      const readPosition=await metrics();const appended=await page.evaluate(()=>window.appendFixture());
      await page.locator(`[data-timeline-id="${appended}"]`).waitFor();await page.waitForTimeout(500);
      assert.equal((await metrics()).top,readPosition.top,'append lost manual reading position');assert.equal((await metrics()).writes,0);

      // Native scroll to the bottom resumes follow. No artificial flag is set.
      await chat.evaluate(c=>c.scrollTop=c.scrollHeight);await page.waitForTimeout(300);await page.evaluate(()=>window.scrollWrites=[]);
      const appendedAtBottom=await page.evaluate(()=>window.appendFixture());await page.locator(`[data-timeline-id="${appendedAtBottom}"]`).waitFor();await page.waitForTimeout(300);
      assert.ok((await metrics()).bottom<=2,'return to bottom did not resume following');assert.ok((await metrics()).writes>0);

      // The nested evidence list scrolls on its own, never locks the outer feed.
      await card.evaluate(e=>{const c=document.querySelector('.chat'),l=e.querySelector('.workflow-selection-list');l.scrollTop=300;c.scrollTop+=l.getBoundingClientRect().top-c.getBoundingClientRect().top-50});await page.waitForTimeout(250);
      const listRect=await list.boundingBox();await page.mouse.move(listRect.x+listRect.width/2,Math.max(listRect.y,rect.y)+40);
      const listBefore=await list.evaluate(l=>l.scrollTop);await page.evaluate(()=>window.scrollWrites=[]);await page.mouse.wheel(0,-40);await page.waitForTimeout(400);
      assert.ok(await list.evaluate(l=>l.scrollTop)<listBefore,'nested list did not scroll');assert.equal((await metrics()).writes,0);
      // Full original excerpt can be read, and expansion doesn't yank the feed.
      await list.evaluate(l=>l.scrollTop=0);const excerpt=list.locator('details').first();await excerpt.locator('summary').click();await page.waitForTimeout(250);
      assert.equal(await excerpt.evaluate(e=>e.open),true);assert.equal((await metrics()).writes,0);
      await excerpt.locator('summary').click();

      // Older bodies keep exactly the same outer box when released.
      const older=page.getByRole('button',{name:'加载更早记录',exact:true});
      if(await older.count()){await chat.evaluate(c=>c.scrollTop=0);await older.click();await page.waitForTimeout(500)}
      await chat.evaluate(c=>c.scrollTop=0);await page.waitForTimeout(250);
      const firstRowId=await page.locator('[data-timeline-id]:has(.message)').first().getAttribute('data-timeline-id');
      const firstRow=page.locator(`[data-timeline-id="${firstRowId}"]`);const rowId=firstRowId;
      const box=()=>firstRow.evaluate(e=>({height:e.getBoundingClientRect().height,margin:getComputedStyle(e).marginBottom,mounted:e.dataset.timelineMounted}));
      const mounted=await box();assert.equal(mounted.mounted,'true');assert.ok(mounted.height>0);assert.equal(mounted.margin,'18px');
      await chat.evaluate(c=>c.scrollTop=c.scrollHeight);await page.waitForTimeout(400);const released=await box();
      assert.equal(released.mounted,'false','historical body not released');assert.ok(Math.abs(mounted.height-released.height)<1);assert.equal(released.margin,mounted.margin,'placeholder loses row gap');

      // Move the footer into view explicitly. Hover alone is not a mutation.
      await card.locator('button.accept').scrollIntoViewIfNeeded();await page.waitForTimeout(300);await page.evaluate(()=>window.scrollWrites=[]);
      await card.locator('button.accept').hover();const footer=await card.locator('button.accept').boundingBox();
      let maxFooterDelta=0;for(let i=0;i<8;i++){await page.waitForTimeout(100);const r=await card.locator('button.accept').boundingBox();maxFooterDelta=Math.max(maxFooterDelta,Math.abs(r.y-footer.y))}
      assert.ok(maxFooterDelta<1,'hovering footer moves its hit target');assert.equal((await metrics()).writes,0);
      assert.equal(await card.locator('button.accept').isDisabled(),true);
      const limited=card.locator('.workflow-limited-evidence input');await limited.click();
      await page.waitForFunction(()=>document.querySelector('.workflow-limited-evidence input')?.checked && !document.querySelector('.research-interaction-card.decision button.accept')?.disabled);
      await page.waitForTimeout(300);assert.equal(await limited.isChecked(),true);assert.equal(await card.locator('button.accept').isEnabled(),true);
      await card.locator('button.accept').hover();await page.waitForTimeout(300);await page.screenshot({path:path.join(output,`card-footer-${size.width}.png`)});
      const forbidden=['Start','StartChat','StartResearch','Decide','Retry','ConfirmResearchRevision','RespondToUserInput','CancelRun'];
      assert.deepEqual(await page.evaluate(names=>window.calls.filter(c=>names.includes(c.m)),forbidden),[],'reading or selection advanced work');
      // Check the original confirmation RPC, in this offline fixture only.
      const selected=await card.locator('.workflow-selection-list input:checked').count();await page.evaluate(()=>window.allowFixtureDecision=true);await card.locator('button.accept').click();
      const call=await page.evaluate(()=>window.calls.find(c=>c.m==='Decide'));assert.ok(call);assert.equal(call.args[0].approved,true);assert.equal(call.args[0].acceptLimitedEvidence,true);
      assert.equal(call.args[0].context.length,selected);assert.equal(call.args[0].stepId,fixture.detail.steps.find(s=>s.nodeKind==='citation_selection').id);
      results.push({size,firstUpPixels:bottom.top-first.top,manualAppendPositionRetained:true,nestedListScrolled:true,placeholder:{rowId,mounted,released},maxFooterDelta,selected,confirmationRPC:'fixture only'});
      await page.close();page=null;
    }
    // The same controller serves free chat: real React delta events grow the
    // current answer, rather than a hand-written substitute for the renderer.
    page=await browser.newPage({viewport:{width:1280,height:900}});page.on('pageerror',e=>errors.push(e.message));
    const chatFixture=syntheticFixture();chatFixture.messages=Array.from({length:20},(_,i)=>({id:'chat-'+i,conversationId:chatFixture.conversation.id,role:'assistant',status:'complete',citations:[],createdAt:'2026-10-14T08:00:00Z',parts:[{type:'text',text:'科研讨论内容。'.repeat(90)}]}));
    chatFixture.latestSnapshot={run:{id:'stream-run',conversationId:chatFixture.conversation.id,assistantMessageId:'stream-answer',status:'running',createdAt:'2026-10-14T08:01:00Z',permissionMode:'full_access',requestedReasoningLevel:'medium',modelProfileId:'profile',modelId:'fixture-model'},sequence:0,messages:[...chatFixture.messages,{id:'stream-answer',conversationId:chatFixture.conversation.id,runId:'stream-run',role:'assistant',status:'streaming',parts:[],citations:[]}],toolCalls:[],runSteps:[],pendingApprovals:[]};
    await page.route('**/*',route=>route.abort());await page.setContent('<!doctype html><div id="root"></div>');await page.addStyleTag({content:css});
    await page.evaluate(v=>window.fixture=v,chatFixture);await page.addScriptTag({content:mock});await page.addScriptTag({content:code,type:productionJS?'module':undefined});
    await page.locator('.chat .message').first().waitFor();await page.waitForTimeout(300);
    await page.evaluate(()=>{
      window.streamSequence=0;window.emitStream=delta=>{
        const s=window.fixture.latestSnapshot,answer=s.messages.at(-1);answer.parts=[{type:'text',text:(answer.parts[0]?.text||'')+delta}];s.sequence=++window.streamSequence;
        const event={aggregateId:'stream-run',sequence:s.sequence,type:'content.delta',payload:{messageId:'stream-answer',delta}};for(const cb of window.runSubscribers)cb(event);
      };
    });
    await page.waitForFunction(()=>document.querySelectorAll('.chat .message').length>=21);
    const chat=page.locator('.chat');await chat.evaluate(c=>c.scrollTop=c.scrollHeight);await page.waitForTimeout(100);
    await page.evaluate(()=>{window.scrollWrites=[];window.emitStream('新答案。'.repeat(200))});await page.waitForTimeout(200);
    assert.ok(await chat.evaluate(c=>c.scrollHeight-c.clientHeight-c.scrollTop)<=2,'free chat streaming lost bottom follow');
    const chatRect=await chat.boundingBox();await page.mouse.move(chatRect.x+15,chatRect.y+chatRect.height-50);
    await page.mouse.wheel(0,-40);await page.waitForTimeout(100);const readingTop=await chat.evaluate(c=>c.scrollTop);
    await page.evaluate(()=>{window.scrollWrites=[];window.emitStream('后续答案。'.repeat(150))});await page.waitForTimeout(300);
    assert.equal(await chat.evaluate(c=>c.scrollTop),readingTop,'free chat streaming interrupted reading');assert.equal(await page.evaluate(()=>window.scrollWrites.length),0);
    await chat.evaluate(c=>c.scrollTop=c.scrollHeight);await page.waitForTimeout(100);await page.evaluate(()=>window.emitStream('继续回答。'.repeat(120)));await page.waitForTimeout(250);
    assert.ok(await chat.evaluate(c=>c.scrollHeight-c.clientHeight-c.scrollTop)<=2,'free chat did not resume at bottom');
    results.push({mode:'free chat',streamingFollowsAtBottom:true,streamingRetainsReadingPosition:true,resumesAtBottom:true});await page.close();page=null;
    assert.deepEqual(errors,[]);
    const result={source:'actual React.StrictMode App with isolated facade fixtures in Edge; all network blocked',captured:!!captured,productionCSS:process.env.SCIAIDE_PRODUCTION_CSS==='1',productionJS,results,errors};
    fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(result,null,2));console.log('PASS',JSON.stringify(result));
  } catch(e) {
    if(page){fs.writeFileSync(path.join(output,'failed-page.txt'),await page.locator('body').innerText());fs.writeFileSync(path.join(output,'failed-calls.json'),JSON.stringify(await page.evaluate(()=>window.calls),null,2));await page.screenshot({path:path.join(output,'failed-page.png')});}
    throw e;
  } finally {await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
