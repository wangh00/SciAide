// Actual React App + offline facades. No real model, native EXE or user DB.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve('.tmp/runtime-activity-fix');

(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const bundle = await build({stdin:{contents:"import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<App/>);",loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const css = fs.readdirSync('frontend/dist/assets').filter(f=>f.endsWith('.css')).map(f=>fs.readFileSync(path.join('frontend/dist/assets',f),'utf8')).join('\n');
  const browser = await chromium.launch({channel:'msedge',headless:true});
  const errors=[];
  try {
    const page = await browser.newPage({viewport:{width:1280,height:900}});
    page.on('pageerror',e=>errors.push(e.message));
    await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:css});
    await page.evaluate(()=>{
      window.calls=[];window.listeners=[];
      window.runtime={EventsOn:(_name,fn)=>{window.listeners.push(fn);return ()=>{}},EventsOnMultiple:(_name,fn)=>{window.listeners.push(fn);return ()=>{}},OnFileDrop:()=>{},OnFileDropOff:()=>{}};
      const now=new Date().toISOString();
      window.conversation={id:'c',projectId:'p',title:'科研补查讨论',permissionMode:'full_access',modelProfileId:'profile',modelId:'fixture-model',reasoningLevel:'max'};
      const compilation={nodes:[],edges:[],outputs:[],order:[]};
      window.detail={run:{id:'workflow',projectId:'p',workflowId:'w',workflowVersionId:'v',researchTaskId:'task',conversationId:'c',workflowName:'睡眠与学习研究',workflowPurpose:'user_plan',status:'failed',inputs:{},outputs:{},compilation,createdAt:now},steps:[],events:[],pendingApprovals:[],aiExecutions:[],artifactCount:0};
      const messages=[{id:'user',runId:'chat',role:'user',status:'complete',parts:[{type:'text',text:'先核对当前信息缺口，先不要执行返修。'}],createdAt:now},{id:'answer',runId:'chat',role:'assistant',status:'streaming',parts:[{type:'text',text:'关于证据 **[K-32DD581CCB82]**：摘录没有样本量，不应称“小样本”。本轮核读的摘录 [K-6E75257CD221]。'}],citations:[{ordinal:0,reference:'[K-6E75257CD221]',sourceName:'research-metadata.md',locator:'lines:1-20',quote:'All correlations returned non-significant results.',quoteSha256:'a'.repeat(64),evidenceLevel:'metadata_abstract'}],createdAt:now}];
      window.snapshot={sequence:1,retrySequence:0,run:{id:'chat',conversationId:'c',assistantMessageId:'answer',status:'running',modelTurns:4,modelProfileId:'profile',modelId:'fixture-model',createdAt:now,startedAt:now,requestedReasoningLevel:'max',apiProtocol:'openai_chat_completions',contextWindowTokens:200000,autoCompactTokenLimit:160000,inputTokens:1000},activity:{runId:'chat',phase:'compaction',turnIndex:4,startedAt:new Date(Date.now()-67000).toISOString()},messages,toolCalls:[],runSteps:[],pendingApprovals:[],workflowAI:false};
      window.timeline={taskId:'task',latestRunId:'workflow',conversationId:'c',hasMore:false,entries:messages.map((message,i)=>({id:'message:'+message.id,sequence:String(i+1),kind:'message',message,runId:'workflow',conversationId:'c',createdAt:now,snapshot:{}}))};
      window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
        window.calls.push({f,m,args});
        if(m==='ListProjects')return [{id:'p',name:'离线科研验证',workspacePath:'D:/Fixture',workspaceKind:'external'}];
        if(m==='ListModelProfiles')return [{id:'profile',name:'Fixture',enabled:true,isDefault:true,secretConfigured:true,apiProtocol:'openai_chat_completions',baseUrl:'http://fixture.invalid',modelId:'fixture-model',customHeaders:{},models:[{id:'fixture-model',contextWindowTokens:200000,autoCompactTokenLimit:160000,reasoningLevels:['max'],enabled:true,isDefault:true}]}];
        if(m==='ListProjectRuns')return [window.detail.run];
        if(m==='GetRun'||m==='GetCurrentResearchTaskRun')return window.detail;
        if(m==='PollRun')return {revision:'1',unchanged:false,value:window.detail};
        if(m==='Get')return {workflow:{id:'w',projectId:'p',name:'睡眠与学习研究',currentVersionId:'v'},versions:[{id:'v',definition:{schemaVersion:1,name:'睡眠与学习研究',nodes:[],edges:[],inputs:[],outputs:[]},compilation}]};
        if(m==='GetConversation')return window.conversation;
        if(m==='ListConversations')return [window.conversation];
        if(m==='ListMessages')return window.snapshot.messages;
        if(m==='GetLatestRunSnapshot'||m==='GetRunSnapshot')return window.snapshot;
        if(m==='PollLatestRunSnapshot')return {revision:String(window.snapshot.sequence),unchanged:false,value:window.snapshot};
        if(m==='ResearchTimeline')return window.timeline;
        if(m==='PollResearchTimeline')return {revision:'1',unchanged:false,value:window.timeline};
        if(m==='GetNetworkConfig')return {global:{mode:'direct',url:''},modules:{},revision:0};
        if(m.startsWith('List'))return [];
        if(['StartChat','Retry','Decide','ConfirmResearchRevision'].includes(m))throw Error('Unexpected task execution in display-only test');
        return {};
      }})})};
    });
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    await page.locator('.workflow-project-tasks article > button').first().click();
    await page.locator('.run-process-toggle').filter({hasText:'正在压缩会话上下文'}).waitFor();
    const marker=page.locator('.citation-unverified');
    assert.equal(await marker.textContent(),'来源标记 · 未绑定');
    assert.match(await marker.getAttribute('title'),/K-32DD581CCB82/);
    assert.equal(await page.locator('.citation-marker').count(),1);
    await page.locator('.citation-marker').click();
    await page.getByRole('region',{name:'引用证据'}).waitFor();
    assert.match(await page.locator('.citation-detail').textContent(),/All correlations/);
    await page.getByRole('button',{name:'关闭引用详情',exact:true}).click();
    await page.screenshot({path:path.join(output,'compaction-and-citations.png')});
    const phase=async(name,turn)=>page.evaluate(({name,turn})=>{
      window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+1,run:{...window.snapshot.run,modelTurns:turn},activity:{runId:'chat',phase:name,turnIndex:turn,startedAt:new Date().toISOString()}};
      const event={aggregateId:'chat',sequence:window.snapshot.sequence,type:'run.activity',payload:{activity:window.snapshot.activity}};
      window.listeners.forEach(fn=>fn(event));
    },{name,turn});
    await phase('model_wait',5);
    await page.locator('.run-process-toggle').filter({hasText:'第5轮 · 等待模型响应'}).waitFor();
    await phase('model_processing',5);
    await page.locator('.run-process-toggle').filter({hasText:'模型已响应'}).waitFor();
    // Older/out-of-order events cannot put the bar back into compaction.
    await page.evaluate(()=>window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:1,type:'run.activity',payload:{activity:{runId:'chat',turnIndex:4,phase:'compaction',startedAt:new Date().toISOString()}}})));
    assert.match(await page.locator('.run-process-toggle').textContent(),/模型已响应/);
    assert.doesNotMatch(await page.locator('.run-process-toggle').textContent(),/正在压缩/);
	await page.evaluate(()=>{
	  window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+1,retrySequence:window.snapshot.sequence+1,retry:{phase:'stream',reason:'stream_stalled',attempt:2,maxAttempts:5,delayMillis:6000,message:'模型流长时间没有有效进展，正在重连 2/5'}};
	  window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:window.snapshot.sequence,type:'run.retrying',payload:{messageId:'answer',retry:window.snapshot.retry}}));
	});
	await page.locator('.run-process-toggle').filter({hasText:'模型流无有效进展，正在重连 2/5'}).waitFor();
	await page.evaluate(()=>{
	  window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+1,retrySequence:window.snapshot.sequence+1,retry:{...window.snapshot.retry,state:'receiving',delayMillis:0,message:'已重新连接，等待本次模型结果完成'}};
	  window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:window.snapshot.sequence,type:'run.retrying',payload:{messageId:'answer',retry:window.snapshot.retry}}));
	});
	await page.locator('.run-process-toggle').filter({hasText:'重试 2/5 · 已重新连接，等待本次模型结果完成'}).waitFor();
	await page.getByRole('button',{name:'自由对话',exact:true}).click();
	await page.getByRole('button',{name:'科研模式',exact:true}).click();
	await page.locator('.workflow-project-tasks article > button').first().click();
	await page.locator('.run-process-toggle').filter({hasText:'重试 2/5 · 已重新连接'}).waitFor();
	await page.evaluate(()=>window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:1,type:'run.retry.recovered',payload:{}})));
	assert.match(await page.locator('.run-process-toggle').textContent(),/重试 2\/5/);
	await page.screenshot({path:path.join(output,'retry-reconnected-restored.png')});
	await page.evaluate(()=>{
	  const oldRetry=window.snapshot.retry, oldRetrySequence=window.snapshot.retrySequence;
	  window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+1,retrySequence:window.snapshot.sequence+1,retry:undefined};
	  window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:window.snapshot.sequence,type:'run.retry.recovered',payload:{}}));
	  // A stale retry field assembled with a newer overall sequence cannot
	  // resurrect the waiting status after the live recovery event arrives.
	  window.snapshot={...window.snapshot,retry:oldRetry,retrySequence:oldRetrySequence};
	});
    await page.waitForTimeout(1800);
    assert.doesNotMatch(await page.locator('.run-process-toggle').textContent(),/重试 2\/5|正在重连/);
    await page.evaluate(()=>{
      // Conversely, a newer unrelated snapshot watermark cannot swallow a
      // recovered event whose own status revision is newer than the retry.
      window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+2,retrySequence:window.snapshot.sequence+1};
      window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:window.snapshot.retrySequence,type:'run.retrying',payload:{retry:window.snapshot.retry}}));
    });
    await page.locator('.run-process-toggle').filter({hasText:'重试 2/5'}).waitFor();
    await page.waitForTimeout(1800); // apply overall watermark ahead of retry
    await page.evaluate(()=>{
      window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:window.snapshot.sequence,type:'run.retry.recovered',payload:{}}));
      window.snapshot={...window.snapshot,retry:undefined,retrySequence:window.snapshot.sequence};
    });
    await page.waitForTimeout(100);
    assert.doesNotMatch(await page.locator('.run-process-toggle').textContent(),/重试 2\/5/);
    // Clear transient events by switching away; recovery comes from snapshot.
    await phase('compaction',5);
    await page.getByRole('button',{name:'自由对话',exact:true}).click();
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    await page.locator('.workflow-project-tasks article > button').first().click();
    await page.locator('.run-process-toggle').filter({hasText:'第5轮 · 正在压缩'}).waitFor();
	await phase('implementation_wait',6);
	await page.locator('.run-process-toggle').filter({hasText:'已保存实现片段，正在生成下一段（尚未执行）'}).waitFor();
	await page.evaluate(()=>{
	  window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+1};
	  window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:window.snapshot.sequence,type:'run.implementation_progress',payload:{messageId:'answer',parts:2,chars:1000,complete:false}}));
	});
	assert.match(await page.locator('.run-process-toggle').textContent(),/已保存实现片段/);
	assert.equal(await page.getByRole('button',{name:'确认代码片段',exact:true}).count(),0);
	await phase('implementation_validation',7);
	await page.locator('.run-process-toggle').filter({hasText:'源码已组装，正在核验完整实现（尚未执行）'}).waitFor();
	await page.locator('.run-process-toggle').scrollIntoViewIfNeeded();
	await page.waitForTimeout(350); // settle the existing pane entrance animation before visual QA
	await page.screenshot({path:path.join(output,'implementation-validation.png')});
	// Reloaded durable status preserves the assembly/compute distinction.
	await page.getByRole('button',{name:'自由对话',exact:true}).click();
	await page.getByRole('button',{name:'科研模式',exact:true}).click();
	await page.locator('.workflow-project-tasks article > button').first().click();
	await page.locator('.run-process-toggle').filter({hasText:'源码已组装，正在核验完整实现（尚未执行）'}).waitFor();
    await page.setViewportSize({width:960,height:720});
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
    await page.screenshot({path:path.join(output,'restored-960.png')});
    await page.evaluate(()=>{
      window.snapshot={...window.snapshot,sequence:window.snapshot.sequence+1,retrySequence:window.snapshot.sequence+1,run:{...window.snapshot.run,status:'completed',completedAt:new Date().toISOString()},activity:undefined,retry:undefined,messages:window.snapshot.messages.map(m=>({...m,status:'complete'}))};
      window.listeners.forEach(fn=>fn({aggregateId:'chat',sequence:window.snapshot.sequence,type:'run.completed',payload:{run:window.snapshot.run}}));
    });
    await page.locator('.run-process-toggle').filter({hasText:'已处理'}).waitFor();
    assert.doesNotMatch(await page.locator('.run-process-toggle').textContent(),/正在压缩|等待模型|正在生成下一段|正在核验完整实现/);
    assert.equal(await page.evaluate(()=>window.calls.filter(c=>['StartChat','Retry','Decide','ConfirmResearchRevision'].includes(c.m)).length),0);
    assert.deepEqual(errors,[]);
    console.log('PASS: actual App/production CSS; durable snapshot restore; compact/wait/respond/source-part/assembly phases; saved source is not execution or extra confirmation; stale event ignored; terminal clears phase; unknown historical marker explicit; current citation opens exact quote; 1280/960 no overflow. Offline fixture, no real model or formal DB.');
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exit(1);});
