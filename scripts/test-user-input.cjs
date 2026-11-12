// Actual React App, production CSS and controlled Wails facade. No real model,
// native desktop automation or user database is involved.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');
const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
const out=path.resolve(process.env.SCIAIDE_USER_INPUT_UI_OUTPUT||'.tmp/user-input-inline-20261008/ui');
const css=fs.readdirSync('frontend/dist/assets').filter(f=>f.endsWith('.css')).map(f=>fs.readFileSync(path.join('frontend/dist/assets',f),'utf8')).join('\n');
const previous=fs.readFileSync('scripts/test-research-conversation-entry.cjs','utf8');
const mockLiteral=previous.slice(previous.indexOf('const mock = `')+'const mock = '.length,previous.indexOf('`;',previous.indexOf('const mock = `'))+1);
const baseline=Function('return '+mockLiteral)();

(async()=>{
 fs.mkdirSync(out,{recursive:true});
 const bundle=await build({stdin:{contents:`import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<App/>);`,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
 const browser=await chromium.launch({channel:'msedge',headless:true});
 const errors=[];
 try {
  const page=await browser.newPage({viewport:{width:1280,height:900}});page.on('pageerror',e=>errors.push(e.message));
  await page.setContent('<div id="root"></div>');await page.addStyleTag({content:css});await page.addScriptTag({content:baseline});
  await page.evaluate(()=>{
   const initial=window.go.wails;
   const conv={id:'question-c',projectId:'p',title:'问题对话',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium',permissionMode:'plan'};
   const request={runId:'question-r',callId:'question-call',questions:[{id:'goal',header:'产物',question:'你希望先获得哪种研究产物？',required:true,options:[{id:'report',label:'文献比较报告',description:'梳理现有研究的共同点与差异'},{id:'plan',label:'可执行研究方案',description:'明确研究设计及下一步材料'}]},{id:'scope',header:'范围',question:'你希望重点关注什么？',required:true,selectionMode:'multiple',options:[{id:'methods',label:'方法比较'},{id:'limitations',label:'研究局限'}]}]};
   window.snapshot={sequence:1,run:{id:request.runId,conversationId:conv.id,assistantMessageId:'a',status:'waiting_user_input',modelProfileId:'model1',modelId:'fixture-model',createdAt:'2026-10-08T01:00:00Z'},workflowAI:false,messages:[{id:'u',runId:request.runId,role:'user',status:'complete',internal:false,parts:[{id:'up',type:'text',text:'我想研究睡眠相关问题'}]},{id:'a',runId:request.runId,role:'assistant',status:'streaming',internal:false,parts:[]}],toolCalls:[],runSteps:[],pendingApprovals:[],userInput:request};
   window.questionCalls=[];window.responseFailure=true;
   window.go.wails=new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
    if(f==='ConversationFacade'&&m==='ListConversations')return [conv,{...conv,id:'other-c',title:'另一会话'}];
    if(f==='ConversationFacade'&&m==='ListMessages')return args[0]===conv.id?window.snapshot.messages:[];
    if(f==='WorkflowFacade'&&m==='GetRunByConversation')return {found:false};
    if(f==='ChatFacade') {
     if(m==='ListConversationActivity')return [{conversationId:conv.id,status:window.snapshot.run.status}];
     if(m==='GetLatestRunSnapshot')return args[0]===conv.id?window.snapshot:null;
     if(m==='GetRunSnapshot')return window.snapshot;
     if(m==='PollRunSnapshot'||m==='PollLatestRunSnapshot')return {revision:String(window.snapshot.sequence),unchanged:false,value:args[0]==='other-c'?null:window.snapshot};
     if(m==='RespondToUserInput') {
      window.questionCalls.push({f,m,args});
      if(window.responseFailure){window.responseFailure=false;throw Error('Fixture: 提交失败，请重试')}
      window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+1,run:{...window.snapshot.run,status:'completed'},userInput:undefined,messages:[...window.snapshot.messages.filter(m=>m.id!=='a'),{id:'human-answer',role:'user',status:'complete',internal:false,runId:window.snapshot.run.id,parts:[{id:'hp',type:'text',text:'我的回答：文献比较报告；先看成人；方法比较；研究局限'}]},{id:'a',role:'assistant',status:'complete',internal:false,runId:window.snapshot.run.id,parts:[{id:'ap',type:'text',text:'收到，继续同一轮对话。'}]}]};return window.snapshot;
     }
     if(m==='StartChat'||m==='SteerChat')throw Error('Unexpected new Chat Run while answering');
    }
    return initial[f][m](...args);
   }})});
  });
  await page.addScriptTag({content:bundle.outputFiles[0].text});
  await page.locator('#project').selectOption('p');
  const card=page.locator('.user-input-card');await card.waitFor();
  const active=card.locator('.user-input-panel.current');
  assert.equal(await page.getByRole('dialog').count(),0);
  assert.equal(await card.evaluate(el=>!!el.closest('.message-stack')&&!!el.closest('.chat')),true);
  assert.equal(await page.locator('.user-input-backdrop').count(),0);
  await active.getByRole('radio',{name:/文献比较报告/}).check();
  await active.getByRole('textbox',{name:'你的回答或补充'}).fill('先看成人');
  assert.equal(await page.evaluate(()=>window.questionCalls.length),0);
  // The desktop shell already has a 960px minimum; this change does not
  // silently redesign the unrelated project navigation for mobile screens.
  for(const [width,height] of [[1920,1080],[1280,900],[1024,768],[960,720],[960,600]]) {
   await page.setViewportSize({width,height});await page.waitForTimeout(200);
   await card.getByRole('button',{name:'确定',exact:true}).scrollIntoViewIfNeeded();
   const geometry=await card.boundingBox();assert.ok(geometry.x>=0&&geometry.x+geometry.width<=width+1,JSON.stringify(geometry));
   assert.equal(await card.evaluate(el=>el.scrollWidth>el.clientWidth),false);
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
   assert.equal(await card.getByRole('button',{name:'确定',exact:true}).isVisible(),true);
   await page.screenshot({path:path.join(out,`questions-${width}-${height}.png`)});
  }
  await page.setViewportSize({width:1280,height:900});
  await card.getByRole('button',{name:'确定',exact:true}).click();
  await card.locator('.sliding-forward').waitFor();
  const sliding=await card.evaluate(el=>({count:el.querySelectorAll('.user-input-panel').length,hidden:el.querySelector('.leaving').getAttribute('aria-hidden'),inert:el.querySelector('.leaving').hasAttribute('inert'),current:getComputedStyle(el.querySelector('.current')).animationName,outgoing:getComputedStyle(el.querySelector('.leaving')).animationName}));
  assert.equal(sliding.count,2);assert.equal(sliding.hidden,'true');assert.equal(sliding.inert,true);assert.equal(sliding.current,'user-question-enter-right');assert.equal(sliding.outgoing,'user-question-exit-left');
  await page.screenshot({path:path.join(out,'question-slide.png')});
  await card.locator('.leaving').waitFor({state:'detached'});
  assert.equal(await page.evaluate(()=>window.questionCalls.length),0);
  // The same waiting request restores its active page and drafts after navigation.
  await page.getByRole('button',{name:'另一会话',exact:true}).click();await card.waitFor({state:'hidden'});
  await page.getByRole('button',{name:'问题对话',exact:true}).click();await active.getByText('你希望重点关注什么？',{exact:true}).waitFor();
  await card.getByRole('button',{name:'上一题',exact:true}).click();await card.locator('.leaving').waitFor({state:'detached'});
  assert.equal(await active.getByRole('radio',{name:/文献比较报告/}).isChecked(),true);assert.equal(await active.getByRole('textbox',{name:'你的回答或补充'}).inputValue(),'先看成人');
  await card.getByRole('button',{name:'确定',exact:true}).click();await card.locator('.leaving').waitFor({state:'detached'});
  await active.getByRole('checkbox',{name:'方法比较',exact:true}).check();await active.getByRole('checkbox',{name:'研究局限',exact:true}).check();
  await page.getByRole('textbox',{name:'聊天消息',exact:true}).fill('范围只限成人');await page.getByRole('button',{name:'发送回答',exact:true}).click();
  assert.equal(await active.getByRole('textbox',{name:'你的回答或补充'}).inputValue(),'范围只限成人');assert.equal(await page.evaluate(()=>window.questionCalls.length),0);
  await card.getByRole('button',{name:'确定',exact:true}).click();await card.getByRole('alert').waitFor();
  assert.equal(await active.getByRole('checkbox',{name:'方法比较',exact:true}).isChecked(),true);
  await page.screenshot({path:path.join(out,'submission-retry.png')});
  await card.getByRole('button',{name:'确定',exact:true}).click();await card.waitFor({state:'hidden'});
  await page.getByText('收到，继续同一轮对话。',{exact:true}).waitFor();
  const calls=await page.evaluate(()=>window.questionCalls);assert.equal(calls.length,2);assert.deepEqual(calls[0].args,calls[1].args);
  const response=calls[1].args[0];assert.equal(response.runId,'question-r');assert.equal(response.callId,'question-call');assert.equal(response.answers.goal.text,'先看成人');assert.deepEqual(response.answers.scope.optionIds,['methods','limitations']);assert.equal(response.answers.scope.text,'范围只限成人');
  // A new Run on the same conversation restores its own pending question,
  // but cannot leak its card or draft when the user switches conversation.
  await page.evaluate(()=>{window.snapshot={...window.snapshot,sequence:3,run:{...window.snapshot.run,id:'next-discussion-run',status:'waiting_user_input',createdAt:'2026-10-08T01:30:00Z'},userInput:{runId:'next-discussion-run',callId:'next-question',questions:[{id:'next',question:'交付之后，还有哪个方向想继续？',required:true}]}}});
  await page.getByRole('button',{name:'另一会话',exact:true}).click();
  await page.getByRole('button',{name:'问题对话',exact:true}).click();
  await card.waitFor();assert.equal(await active.getByRole('textbox',{name:'你的回答或补充'}).inputValue(),'');
  await page.getByRole('button',{name:'另一会话',exact:true}).click();await page.waitForTimeout(1200);assert.equal(await card.count(),0);
  await page.getByRole('button',{name:'问题对话',exact:true}).click();await card.waitFor();assert.equal(await page.getByRole('dialog').count(),0);
  await page.getByRole('textbox',{name:'聊天消息',exact:true}).fill('请解释结果，不做返修');
  await page.getByRole('button',{name:'发送回答',exact:true}).click();await page.waitForTimeout(200);
  const free=await page.evaluate(()=>window.questionCalls.at(-1).args[0]);assert.equal(free.callId,'next-question');assert.equal(free.answers.next.text,'请解释结果，不做返修');
  await page.evaluate(()=>{window.snapshot={...window.snapshot,sequence:4,run:{...window.snapshot.run,id:'skip-run',status:'waiting_user_input',createdAt:'2026-10-08T02:00:00Z'},userInput:{runId:'skip-run',callId:'skip-question',questions:[{id:'optional-direction',question:'现在要选研究方向吗？',required:true,options:[{id:'recommended',label:'推荐方向'},{id:'alternative',label:'另一方向'}]}]}}});
  await page.getByRole('button',{name:'另一会话',exact:true}).click();await page.getByRole('button',{name:'问题对话',exact:true}).click();await card.waitFor();await card.getByRole('button',{name:'跳过并继续',exact:true}).click();await card.waitFor({state:'hidden'});
  const skipped=await page.evaluate(()=>window.questionCalls.at(-1).args[0]);assert.equal(skipped.runId,'skip-run');assert.equal(skipped.callId,'skip-question');assert.equal(skipped.dismiss,true);assert.deepEqual(skipped.answers,{});
  // Reproduce production handoff event ordering on the actual App. A retry
  // resets the old dangling intro before replaying the useful explanation.
  const handoff=await browser.newPage({viewport:{width:1280,height:900}});handoff.on('pageerror',e=>errors.push(e.message));
  await handoff.setContent('<div id="root"></div>');await handoff.addStyleTag({content:css});await handoff.addScriptTag({content:baseline});
  await handoff.evaluate(()=>{
    const initial=window.go.wails;window.handoffListeners=new Set();
    window.runtime.EventsOnMultiple=(name,cb)=>{if(name==='sciaide:run-event')window.handoffListeners.add(cb);return ()=>window.handoffListeners.delete(cb)};
    const conv={id:'handoff-c',projectId:'p',title:'未完成提问恢复',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium',permissionMode:'plan'};
    window.handoffBody='先区分睡眠质量与学习投入，问卷相关性不能证明因果。';
    window.handoffIntro=window.handoffBody+'\n\n为了帮你继续细化研究问题、问卷和分析方法，我需要先确认两个方面：';
    window.handoffSnapshot={sequence:1,run:{id:'handoff-r',conversationId:conv.id,assistantMessageId:'handoff-a',status:'running',createdAt:'2026-10-09T01:00:00Z'},workflowAI:false,messages:[{id:'handoff-u',runId:'handoff-r',role:'user',status:'complete',internal:false,parts:[{id:'hup',type:'text',text:'我想研究睡眠'}]},{id:'handoff-a',runId:'handoff-r',role:'assistant',status:'streaming',internal:false,parts:[{id:'hap',type:'text',text:window.handoffIntro}]}],toolCalls:[],runSteps:[],pendingApprovals:[]};
    window.go.wails=new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
      if(f==='ConversationFacade'&&m==='ListConversations')return [conv];
      if(f==='ConversationFacade'&&m==='ListMessages')return window.handoffSnapshot.messages;
      if(f==='WorkflowFacade'&&m==='GetRunByConversation')return {found:false};
      if(f==='ChatFacade'){
        if(m==='ListConversationActivity')return [{conversationId:conv.id,status:window.handoffSnapshot.run.status}];
        if(['GetLatestRunSnapshot','GetRunSnapshot'].includes(m))return window.handoffSnapshot;
        if(['PollRunSnapshot','PollLatestRunSnapshot'].includes(m))return {revision:String(window.handoffSnapshot.sequence),unchanged:false,value:window.handoffSnapshot};
      }
      return initial[f][m](...args);
    }})});
  });
  await handoff.addScriptTag({content:bundle.outputFiles[0].text});await handoff.locator('#project').selectOption('p');
  await handoff.locator('.message.assistant').filter({hasText:'我需要先确认两个方面'}).waitFor();
  await handoff.evaluate(()=>{
    const emit=(sequence,type,payload)=>window.handoffListeners.forEach(cb=>cb({aggregateId:'handoff-r',sequence,type,payload}));
    window.handoffSnapshot={...window.handoffSnapshot,sequence:4,messages:window.handoffSnapshot.messages.map(m=>m.id==='handoff-a'?{...m,parts:[{id:'hap',type:'text',text:window.handoffBody}]}:m)};
    emit(2,'run.retrying',{messageId:'handoff-a',retry:{phase:'question_handoff',attempt:1,maxAttempts:1,message:'正在补全本轮未展开的提问'}});
    emit(3,'content.started',{messageId:'handoff-a'});emit(4,'content.delta',{messageId:'handoff-a',delta:window.handoffBody});
  });
  await handoff.waitForFunction(()=>{
    const text=document.querySelector('.message.assistant')?.textContent||'';
    return text.includes(window.handoffBody)&&!text.includes('我需要先确认两个方面')&&text.split(window.handoffBody).length===2;
  }).catch(async error=>{await handoff.screenshot({path:path.join(out,'handoff-failure.png')});console.error('handoff state',await handoff.evaluate(()=>({text:document.querySelector('.message.assistant')?.textContent,listeners:window.handoffListeners.size,snapshot:window.handoffSnapshot,html:document.querySelector('.message.assistant')?.outerHTML})));throw error});
  await handoff.evaluate(()=>{
    window.handoffSnapshot={...window.handoffSnapshot,sequence:5,run:{...window.handoffSnapshot.run,status:'waiting_user_input'},userInput:{runId:'handoff-r',callId:'handoff-call',questions:[{id:'purpose',question:'希望最终产出什么？',required:true,options:[{id:'design',label:'研究设计'},{id:'review',label:'文献梳理'}]}]}};
    window.handoffListeners.forEach(cb=>cb({aggregateId:'handoff-r',sequence:5,type:'run.user_input_requested',payload:{}}));
  });
  await handoff.locator('.user-input-card').waitFor();assert.equal(await handoff.locator('.user-input-card').count(),1);assert.equal(await handoff.getByRole('dialog').count(),0);
  assert.ok((await handoff.locator('.message.assistant').textContent()).includes(await handoff.evaluate(()=>window.handoffBody)));
  await handoff.waitForTimeout(400);
  await handoff.screenshot({path:path.join(out,'unfinished-question-handoff.png')});
  // Legacy planner questions use the identical inline card, not a second choice /
  // confirmation chain. Free text is transmitted separately from option IDs.
  const legacy=await browser.newPage({viewport:{width:1280,height:900}});legacy.on('pageerror',e=>errors.push(e.message));
  await legacy.setContent('<div id="root"></div>');await legacy.addStyleTag({content:css});await legacy.addScriptTag({content:baseline});
  await legacy.evaluate(()=>{
    const plan={normalizedQuestion:'旧路线需要明确研究方向',researchType:'research_design',recommendationReason:'先明确产物与研究范围',confidence:'medium',missingInformation:[],limitations:[],availableResources:[],selectedSkills:[],routes:[],recommendedRouteId:'',clarification:{needsUserInput:true,intro:'这些选择只用于继续设计路线，不会执行研究。',questions:[{id:'legacy-goal',text:'你想先做哪一种工作？',impact:'决定候选路线的交付类型',required:true,selectionMode:'single',options:[{id:'review',label:'文献综述'},{id:'design',label:'研究设计'}]}]}};
    const run={id:'legacy-r',workflowId:'legacy-w',workflowName:'旧路线问题',projectId:'p',researchTaskId:'legacy-task',conversationId:'legacy-c',workflowPurpose:'research_starter',status:'completed',inputs:{starter_context:{researchIdea:'旧路线需要明确研究方向'}},outputs:{plan},compilation:{nodes:[],edges:[],order:[],outputs:[]},createdAt:'2026-10-08T01:00:00Z',updatedAt:'2026-10-08T01:00:00Z'};
    window.detail={run,steps:[],events:[{id:'legacy-completed',type:'workflow.completed',payload:{},createdAt:run.updatedAt}],pendingApprovals:[],aiExecutions:[],artifactCount:0};window.owner={workflow:{id:run.workflowId,projectId:'p',name:'旧路线问题',currentVersionId:'legacy-v',workflowPurpose:'research_starter'},versions:[]};window.conversation={id:run.conversationId,projectId:'p',title:'旧路线问题',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium'};
    window.timeline={taskId:'legacy-task',latestRunId:run.id,conversationId:run.conversationId,hasMore:false,entries:[{id:'event:legacy-completed',sequence:'1',kind:'event',eventType:'workflow.completed',runId:run.id,conversationId:run.conversationId,active:true,createdAt:run.updatedAt,snapshot:{event:{},outputs:run.outputs}}]};window.legacyReplies=[];window.legacyReplyFailure=true;
    const initial=window.go.wails;window.go.wails=new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
      if(f==='WorkflowFacade'&&m==='GetResearchStarterPlan')return plan;
      if(f==='WorkflowFacade'&&m==='GetPendingResearchAdoption')return null;
      if(f==='WorkflowFacade'&&m==='GetCurrentResearchTaskRun')return window.detail;
      if(f==='WorkflowFacade'&&m==='AnswerResearchClarification'){
        window.legacyReplies.push(args[0]);
        if(window.legacyReplyFailure){window.legacyReplyFailure=false;throw Error('Fixture: 旧问题提交失败')}
        window.detail={...window.detail,run:{...run,id:'legacy-next',inputs:{discussion_only:true},outputs:{}}};return window.detail;
      }
      return initial[f][m](...args);
    }})});
  });
  await legacy.addScriptTag({content:bundle.outputFiles[0].text});await legacy.getByRole('button',{name:'科研模式',exact:true}).click();await legacy.locator('.workflow-project-tasks article > button').first().click();
  const oldCard=legacy.locator('.user-input-card');await oldCard.waitFor();
  assert.equal(await legacy.getByRole('dialog').count(),0);assert.equal(await oldCard.evaluate(el=>!!el.closest('.research-task-timeline')&&!!el.closest('.chat')),true);
  await oldCard.getByText('决定候选路线的交付类型',{exact:true}).waitFor();assert.equal(await oldCard.getByRole('button',{name:'跳过并继续',exact:true}).count(),0);
  await oldCard.getByRole('textbox',{name:'你的回答或补充'}).fill('先比较方法，不选这两项');await oldCard.getByRole('button',{name:'确定',exact:true}).click();await oldCard.getByRole('alert').waitFor();
  assert.equal(await oldCard.getByRole('textbox',{name:'你的回答或补充'}).inputValue(),'先比较方法，不选这两项');await legacy.waitForTimeout(250);
  await oldCard.getByRole('button',{name:'确定',exact:true}).scrollIntoViewIfNeeded();await legacy.screenshot({path:path.join(out,'legacy-free-text-retry.png')});
  await oldCard.getByRole('button',{name:'确定',exact:true}).click();await oldCard.waitFor({state:'hidden'});
  const oldReplies=await legacy.evaluate(()=>window.legacyReplies);assert.equal(oldReplies.length,2);assert.deepEqual(oldReplies[0],oldReplies[1]);assert.deepEqual(oldReplies[1].answers,{'legacy-goal':[]});assert.deepEqual(oldReplies[1].freeTextAnswers,{'legacy-goal':'先比较方法，不选这两项'});
  assert.equal(await legacy.evaluate(()=>window.calls.filter(c=>['StartChat','AdoptResearchRoute','ReplanResearchStarter','ConfirmResearchRevision'].includes(c.m)).length),0);
  // Generic tool questions in a research discussion are the trailing element
  // of the existing timeline, not rendered twice or attached to the composer.
  const research=await browser.newPage({viewport:{width:1280,height:900}});research.on('pageerror',e=>errors.push(e.message));
  await research.setContent('<div id="root"></div>');await research.addStyleTag({content:css});await research.addScriptTag({content:baseline});
  await research.evaluate(()=>{
    const run={id:'discussion-workflow',workflowId:'discussion-w',workflowName:'睡眠研究讨论',projectId:'p',researchTaskId:'discussion-task',conversationId:'discussion-c',workflowPurpose:'research_starter',status:'completed',inputs:{discussion_only:true,starter_context:{researchIdea:'想研究大学生睡眠与学习状态'}},outputs:{},compilation:{nodes:[],edges:[],order:[],outputs:[]},createdAt:'2026-10-08T01:00:00Z',updatedAt:'2026-10-08T01:00:00Z'};
    window.detail={run,steps:[],events:[],pendingApprovals:[],aiExecutions:[],artifactCount:0};window.owner={workflow:{id:run.workflowId,projectId:'p',name:run.workflowName,currentVersionId:'discussion-v',workflowPurpose:'research_starter'},versions:[]};window.conversation={id:run.conversationId,projectId:'p',title:run.workflowName,modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium'};
    const messages=[{id:'discussion-user',runId:'discussion-chat',role:'user',status:'complete',internal:false,parts:[{id:'dup',type:'text',text:'想研究大学生睡眠与学习状态'}]},{id:'discussion-assistant',runId:'discussion-chat',role:'assistant',status:'streaming',internal:false,parts:[{id:'dap',type:'text',text:'先明确你希望如何开展研究，选好后我们继续讨论。'}]}];
    window.timeline={taskId:run.researchTaskId,latestRunId:run.id,conversationId:run.conversationId,hasMore:false,entries:messages.map((message,i)=>({id:'message:'+message.id,sequence:String(i+1),kind:'message',runId:'discussion-chat',conversationId:run.conversationId,createdAt:run.createdAt,message,snapshot:{}}))};
    window.researchSnapshot={sequence:1,run:{id:'discussion-chat',conversationId:run.conversationId,assistantMessageId:'discussion-assistant',status:'waiting_user_input',createdAt:run.createdAt},workflowAI:false,messages,toolCalls:[],runSteps:[],pendingApprovals:[],userInput:{runId:'discussion-chat',callId:'discussion-question',questions:[{id:'method',question:'你希望先按哪种方式设计？',required:true,options:[{id:'survey',label:'一次性匿名问卷',description:'招募同学填写问卷，先观察睡眠与学习状态的关联。'},{id:'diary',label:'连续日记追踪',description:'连续记录一到两周的睡眠与每日学习状态。'},{id:'review',label:'文献综述',description:'先整理已有研究，不需要招募参与者。'},{id:'data',label:'分析已有数据',description:'先确认你已有数据中的睡眠和学习指标。'}]},{id:'output',question:'希望最终产出什么？',required:true,options:[{id:'design',label:'研究设计草案'},{id:'comparison',label:'方法比较说明'}]}]}};window.researchReplies=[];
    const initial=window.go.wails;window.go.wails=new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
      if(f==='WorkflowFacade'&&m==='GetCurrentResearchTaskRun')return window.detail;
      if(f==='WorkflowFacade'&&m==='GetRunByConversation')return {found:false};
      if(f==='WorkflowFacade'&&m==='GetPendingResearchAdoption')return null;
      if(f==='ConversationFacade'&&m==='ListMessages')return window.researchSnapshot.messages;
      if(f==='ChatFacade'){
        if(m==='ListConversationActivity')return [{conversationId:'discussion-c',status:window.researchSnapshot.run.status}];
        if(m==='GetLatestRunSnapshot'||m==='GetRunSnapshot')return window.researchSnapshot;
        if(m==='PollLatestRunSnapshot'||m==='PollRunSnapshot'){window.researchPolls=(window.researchPolls||0)+1;return {revision:String(window.researchSnapshot.sequence),unchanged:false,value:window.researchSnapshot};}
        if(m==='RespondToUserInput'){
          window.researchReplies.push(args[0]);window.researchSnapshot={...window.researchSnapshot,sequence:2,run:{...window.researchSnapshot.run,status:'completed'},userInput:undefined,messages:[...messages.filter(m=>m.role==='user'),{id:'research-human-answer',runId:'discussion-chat',role:'user',status:'complete',internal:false,parts:[{id:'rhp',type:'text',text:'我的回答：一次性匿名问卷；研究设计草案'}]},{...messages[1],status:'complete',parts:[{id:'rap',type:'text',text:'继续为你讨论研究设计，不会自动采用或执行路线。'}]}]};window.timeline={...window.timeline,entries:window.researchSnapshot.messages.map((message,i)=>({id:'message:'+message.id,sequence:String(i+1),kind:'message',runId:'discussion-chat',conversationId:'discussion-c',createdAt:run.createdAt,message,snapshot:{}}))};return window.researchSnapshot;
        }
        if(m==='StartChat'||m==='SteerChat')throw Error('Unexpected new research Chat Run');
      }
      return initial[f][m](...args);
    }})});
  });
  await research.addScriptTag({content:bundle.outputFiles[0].text});await research.getByRole('button',{name:'科研模式',exact:true}).click();await research.locator('.workflow-project-tasks article > button').first().click();
  const researchCard=research.locator('.user-input-card');await researchCard.waitFor();assert.equal(await researchCard.count(),1);assert.equal(await research.getByRole('dialog').count(),0);
  assert.equal(await researchCard.evaluate(el=>el.parentElement.classList.contains('research-task-timeline')&&el.parentElement.lastElementChild===el&&!!el.closest('.chat')&&!el.closest('.composer-wrap')),true);
  await researchCard.getByRole('radio',{name:/一次性匿名问卷/}).check();await researchCard.getByRole('button',{name:'确定',exact:true}).scrollIntoViewIfNeeded();await research.screenshot({path:path.join(out,'research-inline-card.png')});
  const actionBounds=await researchCard.evaluate(el=>{const footer=el.querySelector('footer').getBoundingClientRect(),primary=el.querySelector('button.primary').getBoundingClientRect(),secondary=el.querySelector('.user-input-secondary-actions').getBoundingClientRect();return {footer:{right:footer.right,width:footer.width},primary:{right:primary.right,top:primary.top,bottom:primary.bottom,width:primary.width},secondary:{top:secondary.top,bottom:secondary.bottom}}});
  assert.ok(actionBounds.primary.width<actionBounds.footer.width/2);assert.ok(actionBounds.footer.right-actionBounds.primary.right<22);assert.ok(actionBounds.primary.top<actionBounds.secondary.bottom&&actionBounds.secondary.top<actionBounds.primary.bottom);
  await researchCard.getByRole('button',{name:'确定',exact:true}).click();await researchCard.locator('.leaving').waitFor({state:'detached'});assert.equal(await research.evaluate(()=>window.researchReplies.length),0);
  await researchCard.getByRole('radio',{name:'研究设计草案',exact:true}).check();await researchCard.getByRole('button',{name:'确定',exact:true}).click();await researchCard.waitFor({state:'hidden'});
  const researchResponses=await research.evaluate(()=>window.researchReplies);assert.equal(researchResponses.length,1);assert.equal(researchResponses[0].runId,'discussion-chat');assert.equal(researchResponses[0].callId,'discussion-question');assert.deepEqual(researchResponses[0].answers,{method:{optionIds:['survey']},output:{optionIds:['design']}});
  assert.equal(await research.evaluate(()=>window.calls.filter(c=>['StartChat','AdoptResearchRoute','ReplanResearchStarter','ConfirmResearchRevision'].includes(c.m)).length),0);
  await research.emulateMedia({reducedMotion:'reduce'});
  // A terminal Chat Run cannot become waiting again. Prove that stale polling
  // cannot restore its card, then use a genuinely new Run for the next question.
  const stalePoll=await research.evaluate(()=>{window.researchSnapshot={...window.researchSnapshot,sequence:3,run:{...window.researchSnapshot.run,status:'waiting_user_input'},userInput:{runId:'discussion-chat',callId:'stale-question',questions:[{id:'stale',question:'过期问题不可复活',required:true}]}};return window.researchPolls||0});
  await research.waitForFunction(n=>(window.researchPolls||0)>n,stalePoll);
  // The mock counter means the read started, not that its Promise and React
  // updates have settled. Fence two browser frames with a host-side deadline.
  let renderTimeout;try{await Promise.race([research.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))),new Promise((_,reject)=>{renderTimeout=setTimeout(()=>reject(Error('Stale-snapshot render did not settle')),5000)})]);}finally{clearTimeout(renderTimeout)}
  assert.equal(await researchCard.count(),0);
  await research.evaluate(()=>{window.researchSnapshot={...window.researchSnapshot,sequence:4,run:{...window.researchSnapshot.run,id:'discussion-long-chat',status:'waiting_user_input',createdAt:'2026-10-08T02:00:00Z'},userInput:{runId:'discussion-long-chat',callId:'long-question',questions:[{id:'long',question:'请确认范围。'.repeat(180),required:true,options:[{id:'bounded',label:'仅分析现有材料'}]}]}}});
  await researchCard.waitFor();await researchCard.getByRole('button',{name:'确定',exact:true}).scrollIntoViewIfNeeded();assert.equal(await researchCard.evaluate(el=>el.scrollWidth>el.clientWidth),false);
  assert.equal(await researchCard.evaluate(el=>getComputedStyle(el).animationName),'none');await researchCard.getByRole('radio',{name:'仅分析现有材料',exact:true}).check();
  await research.screenshot({path:path.join(out,'research-long-question.png')});
  assert.deepEqual(errors,[]);console.log('PASS: Actual App inline questions, 5 desktop sizes, directional slides, choice/free-text, active-page draft restore, retry, same-turn response, conversation isolation, composer answer, unfinished-question stream reset/body/card, legacy planner adapter, research trailing-card single-submit, lower-right Confirm, long content and reduced motion.');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
