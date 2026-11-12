// Actual React UI + controlled facade fixtures. No real model or user database.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const productionCSS = process.env.SCIAIDE_PRODUCTION_CSS==='1' ? fs.readdirSync('frontend/dist/assets').filter(file=>file.endsWith('.css')).map(file=>fs.readFileSync(path.join('frontend/dist/assets',file),'utf8')).join('\n') : '';
const repairOutput = path.resolve(process.env.SCIAIDE_REPAIR_UI_OUTPUT || '.tmp/research-selection-ui-20261007');
const guideOutput = path.resolve(process.env.SCIAIDE_GUIDE_UI_OUTPUT || '.tmp/research-guide');

(async () => {
  const bundle = await build({stdin: {contents: `import React from 'react'; import {createRoot} from 'react-dom/client'; import App from './App'; createRoot(document.getElementById('root')).render(<App/>);`,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const mock = `if(!crypto.randomUUID) crypto.randomUUID=()=> 'fixture-client-message'; window.calls=[]; window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
    window.calls.push({f,m,args});
    if(m==='ChooseConversationDocumentPaths') {
      if(window.choosePending) return await new Promise(resolve=>{window.finishChoose=resolve});
      return window.choosePaths ?? ['D:/fixture/unsent.txt'];
    }
    if(m==='ImportConversationDocumentPaths' && window.openingEnabled && !window.detail) {
      if(window.importFails) throw Error('Fixture: attachment import unavailable');
      return {attachments:args[2].filter(p=>p!==window.badPath).map((p,i)=>({id:'opening-file'+(p.endsWith('unsent.txt')?'':i),projectId:args[0],researchTaskId:'conversation:'+args[1],originalName:p.split('/').pop(),mimeType:'text/plain',format:'text',sizeBytes:20,unitCount:1,status:'ready'})),errors:window.badPath?[{path:window.badPath,message:'Fixture: unreadable file'}]:[]};
    }
    if(window.openingEnabled && !window.detail) {
      const run={id:'opening-run',updatedAt:'2026-10-08T01:00:00Z',workflowId:'opening-w',projectId:'p',researchTaskId:'opening-task',conversationId:'opening-c',workflowPurpose:'research_starter',status:'completed',inputs:{discussion_only:true,starter_context:{researchIdea:'请分析我的表格，只做描述统计。'}},outputs:{},compilation:{nodes:[],edges:[],order:[],outputs:[]},createdAt:'2026-10-08T01:00:00Z'};
      const detail={run,steps:[],events:[],pendingApprovals:[],aiExecutions:[],artifactCount:0};
      if(m==='GetCurrentResearchTaskRun' && window.prepared) return window.prepared;
      if(m==='GetResearchStarterPlan') return window.prepared.run.outputs.plan;
      if(m==='StartResearch') {window.openingCreated=true;return detail;}
      if(m==='GetRun') return detail;
      if(m==='GetCurrentResearchTaskRun') return detail;
      if(m==='PollRun') return {revision:'1',unchanged:false,value:detail};
      if(m==='Get') return {workflow:{id:'opening-w',projectId:'p',currentVersionId:'v'},versions:[]};
      if(m==='GetConversation') return {id:args[0]||'opening-c',projectId:'p',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium'};
      if(m==='ListProjectRuns') return window.openingCreated?[run]:[];
      if(m==='ChooseAndImportConversationDocuments') return {attachments:[{id:'opening-file',conversationId:'opening-c',originalName:'unsent.txt',mimeType:'text/plain',format:'text',sizeBytes:20,status:'ready'}],errors:[]};
      if(m==='StartChat') {
        if(!window.openingChatSuccess) throw Error('Fixture captured opening chat; no model called');
        const message={id:args[0].clientMessageId,runId:'opening-chat',internal:false,role:'user',status:'complete',parts:[{type:'text',text:args[0].text}],createdAt:'2026-10-08T01:01:00Z'};
        const answer={id:'opening-answer-'+window.calls.filter(c=>c.m==='StartChat').length,runId:'opening-chat',internal:false,role:'assistant',status:'complete',parts:[{type:'text',text:'先看看你希望交付什么，暂不执行研究。'}],createdAt:'2026-10-08T01:01:01Z'};
        window.openingMessages=[...(window.openingMessages||[]),message,answer];
        return {id:'opening-chat',conversationId:'opening-c',status:'completed',createdAt:'2026-10-08T01:01:00Z'};
      }
      if(m==='ListMessages') return window.openingMessages||[];
      if(m==='ResearchTimeline') return {taskId:'opening-task',latestRunId:run.id,conversationId:run.conversationId,hasMore:false,entries:[...(window.openingMessages||[]).map((message,i)=>({id:'message:'+message.id,sequence:String(i+1),kind:'message',message,conversationId:'opening-c',runId:run.id,createdAt:message.createdAt,snapshot:{}})),...(window.prepared?[{id:'event:prepared-completion',sequence:'50',kind:'event',runId:'prepared-run',conversationId:'prepared-c',eventType:'workflow.completed',active:true,createdAt:window.prepared.run.updatedAt,snapshot:{event:{}}}]:[])]};
      if(m==='PollResearchTimeline') return {revision:String(window.openingMessages?.length||0),unchanged:false,value:{taskId:'opening-task',latestRunId:run.id,conversationId:run.conversationId,hasMore:false,entries:[...(window.openingMessages||[]).map((message,i)=>({id:'message:'+message.id,sequence:String(i+1),kind:'message',message,conversationId:'opening-c',runId:run.id,createdAt:message.createdAt,snapshot:{}})),...(window.prepared?[{id:'event:prepared-completion',sequence:'50',kind:'event',runId:'prepared-run',conversationId:'prepared-c',eventType:'workflow.completed',active:true,createdAt:window.prepared.run.updatedAt,snapshot:{event:{}}}]:[])]}};
    }
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
      if(m==='GetLatestRunSnapshot') return window.repairChatSnapshot || null;
      if(m==='PollLatestRunSnapshot') return {revision:'1',unchanged:false,value:window.repairChatSnapshot || null};
      if(m==='ListResearchRevisionProposals') return window.proposals || [];
      if(m==='ChooseAndImportConversationDocuments') return {attachments:[{id:'repair-attachment',originalName:'supplement.txt',mimeType:'text/plain',format:'text',sizeBytes:30,status:'ready'}],errors:[]};
      if(m==='StartChat') {
        if(window.repairChatPending) await new Promise(resolve=>{window.finishRepairChat=resolve});
        if(!window.repairChatSuccess) throw Error('Fixture captured repair discussion; no model called');
        const run={id:'repair-chat',conversationId:args[0].conversationId,status:window.repairChatStatus||'completed',createdAt:'2026-10-08T01:05:00Z'};
        window.repairChatSnapshot={run,messages:[],toolCalls:[],pendingApprovals:[],events:[]};
        return run;
      }
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
    if(f==='WorkflowFacade' && m==='ListProjectRuns') return window.homeRuns || [];
    if(f==='WorkflowFacade' && m==='List') return window.homePlans || [];
    if(f==='WorkflowFacade' && m==='Templates') return window.homeTemplates || [];
    if(f==='WorkflowFacade' && m==='Get' && window.homePlans) {
      if(window.homeOpenFail) throw Error('Fixture: saved plan unavailable');
      const workflow=window.homePlans.find(plan=>plan.id===args[1]);
      return {workflow,versions:[{id:workflow.currentVersionId,workflowId:workflow.id,version:1,definition:window.homeTemplates[0].definition}]};
    }
    if(m==='ListProjects'||m.startsWith('List')) return [];
    if(m==='StartResearch') throw Error('Fixture captured planning request; no model called');
    if(m==='GetLatestRunSnapshot') return null;
    if(m==='GetNetworkConfig') return {global:{mode:'direct',url:''},modules:{},revision:0};
    return {};
  }})})};window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:callback=>{window.dropFiles=paths=>callback(0,0,paths)},OnFileDropOff:()=>{window.dropFiles=null}};`;
  const browser = await chromium.launch({channel:'msedge',headless:true});
  try {
    const page = await browser.newPage({viewport:{width:1280,height:900}});
    const errors = []; page.on('pageerror', error => {errors.push(error.message);console.error(error.message);});
    // The entry is central and has no bottom chat; other views retain the
    // original right-column footer. Test both geometry and transition.
    if(process.env.SCIAIDE_HOME_LAYOUT_TEST==='1') {
      fs.mkdirSync(guideOutput,{recursive:true});
      const home = await browser.newPage({viewport:{width:1920,height:1080}});
      home.on('pageerror',error=>errors.push(error.message));
      await home.setContent('<div id="root"></div>');
      await home.addStyleTag({content:productionCSS || ['styles.css','referenceMaterials.css','modal.css'].map(file=>fs.readFileSync(`frontend/src/${file}`,'utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n')});
      await home.addScriptTag({content:mock});
      await home.evaluate(()=>{
        window.homeRuns=Array.from({length:3},(_,i)=>({id:'home-'+i,workflowId:'saved-'+i,projectId:'p',researchTaskId:'home-task-'+i,workflowPurpose:'user_plan',workflowName:'文献与数据研究',status:i===2?'cancelled':'completed',createdAt:'2026-10-08T01:00:00Z',inputs:{research_goal:'从已有研究继续讨论 '+i},outputs:{}}));
        window.homePlans=Array.from({length:9},(_,i)=>({id:'saved-'+i,projectId:'p',name:'研究方案 '+(i+1),description:'可复用方案',currentVersionId:'saved-version-'+i,version:1,createdAt:'2026-10-08T01:00:00Z'}));
        window.homeTemplates=Array.from({length:4},(_,i)=>({id:'template-'+i,name:'研究模板 '+(i+1),definition:{schemaVersion:1,name:'可复用研究方案',nodes:[{id:'stage',name:'分析',kind:'agent_stage'}],edges:[],inputs:[],outputs:[]}}));
      });
      await home.addScriptTag({content:bundle.outputFiles[0].text});
      await home.getByRole('button',{name:'科研模式',exact:true}).click();
      await home.getByRole('heading',{name:'从左侧继续一项科研任务',exact:true}).waitFor();
      const verify = async(label,width,height)=>{
        await home.setViewportSize({width,height});
        await home.waitForTimeout(550);
        const geometry=await home.evaluate(()=>{
          const rect=selector=>{const el=document.querySelector(selector),r=el.getBoundingClientRect();return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,width:r.width,height:r.height,scrollHeight:el.scrollHeight,clientHeight:el.clientHeight}};
          return {workspace:rect('.workspace'),side:rect('.workflow-side'),content:rect('.workflow-layout > main'),footer:rect('.composer-wrap'),form:rect('.composer'),hero:rect('.research-mode-hero'),entry:!!document.querySelector('.research-entry-slot'),bottomComposers:document.querySelectorAll('.workspace > footer.composer-wrap').length,textboxes:document.querySelectorAll('.composer textarea').length,overflow:document.documentElement.scrollWidth>innerWidth};
        });
        fs.writeFileSync(path.join(guideOutput,`${label}-${width}-bounds.json`),JSON.stringify(geometry,null,2));
        await home.screenshot({path:path.join(guideOutput,`${label}-${width}.png`)});
        assert.equal(geometry.textboxes,1);
        assert.equal(geometry.overflow,false);
        assert.ok(Math.abs(geometry.side.bottom-geometry.workspace.bottom)<=1,`${label}: sidebar stops early`);
        if(geometry.entry) {
          assert.equal(geometry.bottomComposers,0,`${label}: extra bottom chat box`);
          assert.ok(geometry.footer.left>=geometry.content.left && geometry.footer.right<=geometry.content.right,`${label}: entry crosses content column`);
          assert.ok(Math.abs(geometry.content.bottom-geometry.workspace.bottom)<=1,`${label}: phantom bottom chat row`);
          assert.ok(geometry.form.top>=geometry.content.top && geometry.form.bottom<=geometry.content.bottom,`${label}: entry action hidden below fold`);
        } else {
          assert.equal(geometry.bottomComposers,1);
          assert.ok(geometry.footer.left>=geometry.side.right-1,`${label}: composer crosses sidebar`);
          assert.ok(Math.abs(geometry.footer.left-geometry.content.left)<=1,`${label}: footer not owned by content column`);
          assert.ok(Math.abs(geometry.content.bottom-geometry.footer.top)<=1,`${label}: composer overlaps scrollable content`);
        }
        assert.ok(geometry.form.left>=geometry.content.left && geometry.form.right<=geometry.content.right,`${label}: form outside content column`);
        assert.ok(geometry.footer.bottom<=geometry.workspace.bottom+1,`${label}: footer clipped`);
        assert.ok(geometry.content.top>=geometry.hero.bottom-1,`${label}: content covers header`);
        assert.ok(Math.abs((geometry.form.left+geometry.form.right)/2-(geometry.content.left+geometry.content.right)/2)<=1,`${label}: composer off center`);
      };
      const sizes=[[1920,1080],[1365,951],[1280,900],[960,720]];
      for(const [width,height] of sizes) await verify('task-home',width,height);
      await home.getByRole('button',{name:'创建新的科研任务',exact:true}).click();
      await home.getByRole('heading',{name:'你想研究什么？',exact:true}).waitFor();
      assert.equal(await home.locator('.workflow-template-gallery').count(),0);
      assert.equal(await home.locator('.workflow-saved-plan-list').count(),0);
      assert.equal((await home.evaluate(()=>window.calls)).filter(c=>c.m==='Templates').length,0);
      for(const [width,height] of sizes) {
        await verify('plan-home',width,height);
        const focus=await home.getByRole('button',{name:'开始讨论',exact:true}).boundingBox();
        const content=await home.locator('.workflow-plan-home').boundingBox();
        assert.ok(focus.y>=content.y && focus.y+focus.height<=content.y+content.height,'first action hidden below fold');
        await home.getByRole('button',{name:'历史方案',exact:true}).click();
        const history=home.getByRole('dialog',{name:'历史方案',exact:true});
        await history.waitFor();
        const bounds=await history.boundingBox();
        assert.ok(bounds.x>=0 && bounds.y>=38 && bounds.x+bounds.width<=width && bounds.y+bounds.height<=height,'history dialog clipped');
        const before=await home.locator('.composer').boundingBox();
        await home.locator('.research-plan-history-body').evaluate(el=>{el.scrollTop=el.scrollHeight});
        const last=await home.getByRole('button',{name:/^研究方案 9 可复用方案 v1$/}).boundingBox();
        const body=await home.locator('.research-plan-history-body').boundingBox();
        assert.ok(last.y+last.height<=body.y+body.height+1,'last history plan not reachable');
        assert.ok(Math.abs(before.y-(await home.locator('.composer').boundingBox()).y)<=1,'history scroll moves composer');
        await home.screenshot({path:path.join(guideOutput,`history-${width}.png`)});
        await home.getByRole('button',{name:'关闭历史方案',exact:true}).click();
        assert.equal(await home.locator('.workflow-saved-plan-list').count(),0);
      }
      const text=home.locator('.composer textarea');
      assert.equal(await home.getByRole('textbox',{name:'研究问题',exact:true}).count(),1);
      assert.equal(await home.getByRole('textbox',{name:'聊天消息',exact:true}).count(),0);
      await text.fill('/');
      await home.locator('.slash-command-menu').waitFor();
      const menuBounds=await home.locator('.slash-command-menu').evaluate(el=>{
        const r=el.getBoundingClientRect(),pane=document.querySelector('.workflow-plan-home').getBoundingClientRect();
        return {left:r.left,right:r.right,top:r.top,paneLeft:pane.left,paneRight:pane.right};
      });
      assert.ok(menuBounds.left>=menuBounds.paneLeft && menuBounds.right<=menuBounds.paneRight && menuBounds.top>=38,'slash menu crosses sidebar or titlebar');
      await text.fill('');
      // Use the real textarea resize handle, not a second composer/DOM stub.
      const textarea=await text.boundingBox();
      await home.mouse.move(textarea.x+textarea.width-3,textarea.y+textarea.height-3);
      await home.mouse.down(); await home.mouse.move(textarea.x+textarea.width-3,textarea.y+textarea.height+65,{steps:6}); await home.mouse.up();
      assert.ok((await text.boundingBox()).height>textarea.height+30,'textarea resize did not occur');
      await verify('plan-home-expanded',960,720);
      await text.fill('保留自然对话输入');
      await home.getByText('研究问题或想法',{exact:true}).click();
      assert.equal(await text.inputValue(),'保留自然对话输入');
      assert.equal(await text.evaluate(el=>el===document.activeElement),true);
      await home.getByRole('button',{name:'历史方案',exact:true}).click();
      await home.getByRole('button',{name:'多选',exact:true}).click();
      await home.getByRole('checkbox',{name:'全选历史方案',exact:true}).check();
      assert.equal(await home.getByRole('checkbox').count(),10);
      await home.getByRole('button',{name:'删除所选（9）',exact:true}).click();
      await home.getByRole('dialog',{name:'删除选中的 9 个研究方案？',exact:true}).waitFor();
      assert.equal((await home.evaluate(()=>window.calls)).filter(c=>c.m==='Delete').length,0);
      await home.getByRole('button',{name:'取消',exact:true}).click();
      assert.equal(await home.getByRole('dialog',{name:'历史方案',exact:true}).count(),1);
      await home.getByRole('button',{name:'关闭历史方案',exact:true}).click();
      assert.equal(await text.inputValue(),'保留自然对话输入');
      await home.getByRole('button',{name:'历史方案',exact:true}).click();
      assert.equal(await home.getByRole('button',{name:'取消多选',exact:true}).count(),0);
      await home.getByRole('button',{name:'删除历史方案 研究方案 9',exact:true}).click();
      await home.getByRole('dialog',{name:'删除研究方案“研究方案 9”？',exact:true}).waitFor();
      assert.equal((await home.evaluate(()=>window.calls)).filter(c=>c.m==='Delete').length,0);
      await home.getByRole('button',{name:'取消',exact:true}).click();
      assert.equal(await home.getByRole('dialog',{name:'历史方案',exact:true}).count(),1);
      await home.evaluate(()=>{window.homeOpenFail=true});
      await home.getByRole('button',{name:/^研究方案 9 可复用方案 v1$/}).click();
      await home.getByRole('status').filter({hasText:'Fixture: saved plan unavailable'}).waitFor();
      assert.equal(await home.getByRole('dialog',{name:'历史方案',exact:true}).count(),1);
      assert.equal(await text.inputValue(),'保留自然对话输入');
      await home.keyboard.press('Escape');
      assert.equal(await home.getByRole('dialog',{name:'历史方案',exact:true}).count(),0);
      await home.evaluate(()=>{window.homeOpenFail=false});
      await home.getByRole('button',{name:'历史方案',exact:true}).click();
      await home.getByRole('button',{name:'刷新历史方案',exact:true}).click();
      await home.getByRole('button',{name:/^研究方案 9 可复用方案 v1$/}).click();
      await home.getByRole('button',{name:'创建任务',exact:true}).waitFor();
      assert.equal(await home.getByRole('dialog',{name:'历史方案',exact:true}).count(),0);
      for(const [width,height] of sizes) await verify('saved-plan-setup',width,height);
      await home.getByRole('button',{name:'编辑方案',exact:true}).click();
      await verify('saved-plan-editor',960,720);
      await home.getByRole('button',{name:'创建任务',exact:true}).click();
      // Adoption is a CSS scene fixture only: no route adoption/save/run call.
      // display:contents must not leave its blocking curtain at the old origin.
      await home.evaluate(()=>{
        const section=document.querySelector('.research-mode-workspace');
        section.classList.add('scene-adopting');
        const curtain=document.createElement('div');curtain.className='research-scene-curtain';section.append(curtain);
      });
      const curtain=await home.locator('.research-scene-curtain').boundingBox();
      const hero=await home.locator('.research-mode-hero').boundingBox();
      assert.ok(Math.abs(curtain.y-hero.y-hero.height)<=1 && Math.abs(curtain.y+curtain.height-720)<=1,'adoption curtain has wrong workspace origin');
      await home.emulateMedia({reducedMotion:'reduce'});
      for(const phase of ['scene-adopting','scene-adopted']) {
        await home.locator('.research-mode-workspace').evaluate((el,phase)=>{el.classList.remove('scene-adopting','scene-adopted');el.classList.add(phase)},phase);
        const motion=await home.locator('.workflow-editor').evaluate(el=>{const s=getComputedStyle(el);return {animation:s.animationName,transform:s.transform,transition:s.transitionDuration}});
        assert.equal(motion.animation,'none');assert.equal(motion.transform,'none');
        // The application's global reduced-motion fallback uses .001s
        // !important so transition-end listeners still settle normally.
        assert.ok(motion.transition.split(',').every(value=>parseFloat(value)<=.001),'scene transition ignores reduced motion');
      }
      await home.evaluate(()=>{const el=document.querySelector('.research-mode-workspace');el.classList.remove('scene-adopting','scene-adopted');el.querySelector('.research-scene-curtain').remove()});
      await home.emulateMedia({reducedMotion:'no-preference'});
      await home.getByRole('button',{name:'返回研究方案',exact:true}).click();
      await text.fill('保留自然对话输入');
      await home.getByRole('button',{name:/^科研任务/}).click();
      assert.equal(await text.inputValue(),'保留自然对话输入');
      await home.getByRole('button',{name:'自由对话',exact:true}).click();
      assert.equal(await text.inputValue(),'保留自然对话输入');
      assert.equal(await home.locator('.research-mode-workspace').count(),0);
      const chatLayout=await home.evaluate(()=>({workspace:document.querySelector('.workspace').getBoundingClientRect().left,footer:document.querySelector('.composer-wrap').getBoundingClientRect().left}));
      assert.ok(Math.abs(chatLayout.workspace-chatLayout.footer)<=1,'home column ownership leaks into free chat');
      assert.equal((await home.evaluate(()=>window.calls)).filter(c=>['StartResearch','StartChat','Start','Retry','Decide','Delete','Save','AdoptResearchRoute'].includes(c.m)).length,0);
      console.log('PASS: centered question entry, no bottom chat or duplicate input; first action visible; templates absent; 9 past plans hidden until history dialog; label focus preserves draft; history scroll/selection/refresh/open; saved setup/editor retain right-column footer; 1920/1365/1280/960; no execution.');
      await home.close();
    }
    await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:productionCSS || ['styles.css','referenceMaterials.css','modal.css'].map(file=>fs.readFileSync(`frontend/src/${file}`,'utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n')});
    await page.addScriptTag({content:mock});
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    const question = page.locator('.composer textarea');
    await question.waitFor();
    await page.getByRole('button',{name:'历史方案',exact:true}).click();
    await page.getByText('暂无历史方案',{exact:true}).waitFor();
    await page.getByRole('button',{name:'返回研究方案',exact:true}).click();
    assert.equal(await page.locator('.workflow-saved-plan-list').count(),0);
    await page.locator('.research-start-examples > summary').click();
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
    await page.getByRole('combobox',{name:'思考强度',exact:true}).selectOption('low');
    assert.equal(await page.getByRole('textbox').count(),1);
    await page.getByRole('button',{name:/^(开始讨论|发送)$/,exact:true}).click();
    await page.getByText('Fixture captured planning request; no model called',{exact:false}).waitFor();
    const commands = await page.evaluate(()=>window.calls.filter(call=>call.m==='StartResearch'));
    assert.equal(commands.length,1);
    assert.equal(commands[0].args[0].researchIdea,'请分析我的表格，只做描述统计。');
    assert.deepEqual(commands[0].args[0].referenceAttachmentIds,[]);
    assert.equal(commands[0].args[0].discussionOnly,true);
    fs.mkdirSync(guideOutput,{recursive:true});
    for (const width of [1280,960]) {
      await page.setViewportSize({width,height:900});
      await page.getByRole('region',{name:'科研对话示例'}).scrollIntoViewIfNeeded();
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth > innerWidth),false);
      assert.equal(await page.locator('.workflow-plan-home').evaluate(element=>element.scrollWidth>element.clientWidth),false);
      await page.screenshot({path:path.join(guideOutput,`entry-${width}.png`)});
    }
    // First normal reply fails after the owner was saved: retry stays in the same task.
    await page.evaluate(()=>{window.openingEnabled=true;});
    await page.getByRole('button',{name:/^(开始讨论|发送)$/,exact:true}).click();
    await page.getByText('Fixture captured opening chat; no model called',{exact:false}).waitFor();
    await page.getByRole('button',{name:/^(开始讨论|发送)$/,exact:true}).click();
    await page.waitForTimeout(100);
    assert.equal(await page.evaluate(()=>window.calls.filter(c=>c.m==='StartResearch'&&window.openingEnabled).length),2);
    assert.equal(await question.inputValue(),'请分析我的表格，只做描述统计。');
    await page.evaluate(()=>{window.openingChatSuccess=true;});
    await page.getByRole('button',{name:/^(开始讨论|发送)$/,exact:true}).click();
    await page.getByText('先看看你希望交付什么，暂不执行研究。',{exact:true}).first().waitFor();
    assert.equal(await question.inputValue(),'');
    assert.equal(await page.getByRole('textbox',{name:'研究问题',exact:true}).count(),0);
    assert.equal(await page.getByRole('textbox',{name:'聊天消息',exact:true}).count(),1);
    assert.equal(await page.locator('.research-entry-input').count(),0);
    assert.equal(await page.locator('.workspace > footer.composer-wrap').count(),1);
    assert.equal(await page.locator('.chat .message.user').filter({hasText:'请分析我的表格，只做描述统计。'}).count(),1,'first turn duplicated in waterfall');
    const chatCalls=await page.evaluate(()=>window.calls.filter(c=>c.m==='StartChat'));
    assert.equal(chatCalls.length,3); // Two failures, then one accepted first turn.
    assert.equal(chatCalls.every(c=>c.args[0].conversationId==='opening-c'),true);
    assert.equal(chatCalls[0].args[0].reasoningLevel,'low','entry reasoning setting lost when owner was created');
    for(const [width,height] of [[1920,1080],[1365,951],[1280,900],[960,720]]) {
      await page.setViewportSize({width,height});
      await page.waitForTimeout(550);
      const bounds=await page.evaluate(()=>{
        const r=selector=>{const v=document.querySelector(selector).getBoundingClientRect();return {left:v.left,right:v.right,top:v.top,bottom:v.bottom}};
        return {workspace:r('.workspace'),route:r('.research-route-panel'),chat:r('.chat'),footer:r('.workspace > .composer-wrap'),overflow:document.documentElement.scrollWidth>innerWidth};
      });
      assert.equal(bounds.overflow,false);
      assert.ok(Math.abs(bounds.footer.left-bounds.chat.left)<=1 && Math.abs(bounds.footer.right-bounds.chat.right)<=1,'task footer does not belong to waterfall');
      assert.ok(Math.abs(bounds.footer.bottom-bounds.workspace.bottom)<=1 && Math.abs(bounds.route.bottom-bounds.workspace.bottom)<=1,'task footer or route no longer reaches bottom');
      assert.ok(Math.abs(bounds.chat.bottom-bounds.footer.top)<=1,'task chat is obscured by footer');
      await page.screenshot({path:path.join(guideOutput,`opening-task-${width}.png`)});
    }
    assert.equal(await page.getByRole('button',{name:'发送',exact:true}).isDisabled(),true);
    await question.fill('你好，先继续聊聊');
    assert.equal(await page.getByRole('button',{name:'发送',exact:true}).isEnabled(),true);
    await page.getByRole('button',{name:/^(开始讨论|发送)$/,exact:true}).click();
    await page.waitForTimeout(150);
    assert.equal(await page.evaluate(()=>window.calls.filter(c=>c.m==='StartResearch').length),2);
    assert.equal(await page.evaluate(()=>window.calls.filter(c=>['Start','AdoptResearchRoute','ConfirmResearchRevision'].includes(c.m)).length),0);
    await page.screenshot({path:path.join(guideOutput,'opening-discussion.png')});
    await page.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    await page.getByText('unsent.txt',{exact:true}).waitFor();
    await page.evaluate(()=>{
      const route={routeId:'design',title:'先准备研究方案',reason:'先商定研究目标和所需材料',availableNow:true,stagePlans:[{stageId:'question_refinement'}],stageIds:['question_refinement','research_design','independent_review','report_publication'],gaps:[],requiredResources:[],deliverables:['可执行研究方案'],blockers:[],planningNotes:[],reviewCheckpoints:[],layers:[{layerId:'scope',title:'目标与设计',objective:'把协商目标转为方案',stages:[]},{layerId:'review',title:'独立检查',objective:'核验范围和可实施性',stages:[]},{layerId:'delivery',title:'交付',objective:'说明局限与未执行内容',stages:[]}],validation:'ready',templateId:'dynamic'};
      const run={id:'prepared-run',workflowId:'prepared-w',projectId:'p',researchTaskId:'opening-task',conversationId:'prepared-c',workflowPurpose:'research_starter',status:'completed',inputs:{starter_context:{researchIdea:'讨论后的完整研究目标',priorStarterRunId:'opening-run',discussionUserMessageId:'human'}},outputs:{plan:{normalizedQuestion:'讨论后的完整研究目标',recommendationReason:'已商定目标，先产出方案。',confidence:'medium',missingInformation:[],limitations:[],resourceGaps:[],selectedSkills:[],recommendedRouteId:'design',routes:[route]}},compilation:{nodes:[],edges:[],order:[],outputs:[]},createdAt:'2026-10-08T01:02:00Z',updatedAt:'2026-10-08T01:02:00Z'};
      window.prepared={run,steps:[],events:[{id:'prepared-completion',type:'workflow.completed',payload:{},createdAt:run.updatedAt}],pendingApprovals:[],aiExecutions:[],artifactCount:0};
    });
    // Unsent files keep their original conversation owner until sent/removed.
    await page.waitForTimeout(2000);
    await page.getByText('unsent.txt',{exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>window.calls.some(c=>c.m==='GetConversation'&&c.args[0]==='prepared-c')),false);
    await page.getByRole('button',{name:'移除 unsent.txt',exact:true}).click();
    await page.getByText('先准备研究方案',{exact:true}).first().waitFor();
    assert.equal(await page.evaluate(()=>window.calls.filter(c=>['Start','AdoptResearchRoute','ConfirmResearchRevision'].includes(c.m)).length),0);
    await question.fill('先解释计划，不要执行');
    assert.equal(await page.getByRole('button',{name:'发送',exact:true}).isEnabled(),true);
    await page.screenshot({path:path.join(guideOutput,'prepared-plan.png')});
    // File-first entry is only a local draft. Cancelling, choosing, removing
    // and dropping files must never create a task or import project materials.
    const fileEntry=await browser.newPage({viewport:{width:1280,height:900}});
    fileEntry.on('pageerror',error=>errors.push(error.message));
    await fileEntry.setContent('<div id="root"></div>');
    await fileEntry.addStyleTag({content:productionCSS || ['styles.css','referenceMaterials.css','modal.css'].map(file=>fs.readFileSync(`frontend/src/${file}`,'utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n')});
    await fileEntry.addScriptTag({content:mock});
    await fileEntry.evaluate(()=>{window.openingEnabled=true;window.openingChatSuccess=true});
    await fileEntry.addScriptTag({content:bundle.outputFiles[0].text});
    await fileEntry.getByRole('button',{name:'科研模式',exact:true}).click();
    await fileEntry.getByRole('button',{name:/^研究方案/}).click();
    const fileQuestion=fileEntry.locator('.composer textarea');
    await fileEntry.getByRole('textbox',{name:'研究问题',exact:true}).waitFor();
    await fileQuestion.fill('先看看这份文件');
    await fileQuestion.press('Shift+Enter');
    assert.equal(await fileQuestion.inputValue(),'先看看这份文件\n');
    assert.equal((await fileEntry.evaluate(()=>window.calls)).filter(c=>['StartChat','StartResearch'].includes(c.m)).length,0);
    await fileEntry.evaluate(()=>{window.choosePaths=[]});
    await fileEntry.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    assert.equal(await fileEntry.locator('.pending-attachments').count(),0);
    assert.equal((await fileEntry.evaluate(()=>window.calls)).filter(c=>['StartResearch','ImportConversationDocumentPaths','ChooseAndImportConversationDocuments'].includes(c.m)).length,0);
    await fileEntry.evaluate(()=>{window.choosePaths=['D:/fixture/unsent.txt']});
    await fileEntry.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    await fileEntry.locator('.research-entry-input').getByText('unsent.txt',{exact:true}).waitFor();
    assert.equal(await fileQuestion.inputValue(),'先看看这份文件\n');
    assert.equal(await fileEntry.getByRole('textbox',{name:'研究问题',exact:true}).count(),1);
    assert.equal(await fileEntry.locator('.research-entry-input').count(),1);
    assert.equal((await fileEntry.evaluate(()=>window.calls)).filter(c=>c.m==='StartResearch').length,0);
    assert.equal((await fileEntry.evaluate(()=>window.calls)).filter(c=>c.m==='StartChat').length,0);
    await fileEntry.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    assert.equal(await fileEntry.locator('.pending-attachments > div').count(),1,'duplicate choice was not deduplicated');
    await fileEntry.getByRole('button',{name:'移除 unsent.txt',exact:true}).click();
    assert.equal(await fileEntry.locator('.pending-attachments').count(),0);
    await fileEntry.evaluate(()=>window.dropFiles(['D:/fixture/unsent.txt','D:/fixture/unsent.txt']));
    await fileEntry.locator('.research-entry-input').getByText('unsent.txt',{exact:true}).waitFor();
    assert.equal(await fileEntry.locator('.pending-attachments > div').count(),1);
    assert.equal((await fileEntry.evaluate(()=>window.calls)).filter(c=>['StartResearch','ImportConversationDocumentPaths','ChooseAndImportConversationDocuments'].includes(c.m)).length,0);
    await fileEntry.screenshot({path:path.join(guideOutput,'file-first-draft.png')});
    await fileQuestion.fill('');
    await fileQuestion.press('Enter');
    await fileEntry.getByText('先看看你希望交付什么，暂不执行研究。',{exact:true}).first().waitFor();
    const fileCalls=await fileEntry.evaluate(()=>window.calls);
    const fileChat=fileCalls.filter(c=>c.m==='StartChat');
    assert.equal(fileChat.length,1);
    assert.equal(fileChat[0].args[0].text,'');
    assert.deepEqual(fileChat[0].args[0].attachmentIds,['opening-file']);
    assert.equal(fileCalls.filter(c=>c.m==='StartResearch').length,1);
    const importCall=fileCalls.find(c=>c.m==='ImportConversationDocumentPaths');
    assert.deepEqual(importCall.args,['p','opening-c',['D:/fixture/unsent.txt']]);
    assert.ok(fileCalls.indexOf(importCall)>fileCalls.findIndex(c=>c.m==='StartResearch'));
    assert.equal(fileCalls.filter(c=>['Start','AdoptResearchRoute','ConfirmResearchRevision'].includes(c.m)).length,0);
    assert.equal(await fileEntry.locator('.pending-attachments').count(),0);
    await fileEntry.screenshot({path:path.join(guideOutput,'file-first-discussion.png')});
    await fileEntry.close();

    async function draftEntry(flags={}) {
      const testPage=await browser.newPage({viewport:{width:1280,height:900}});
      testPage.on('pageerror',error=>errors.push(error.message));
      await testPage.setContent('<div id="root"></div>');
      await testPage.addStyleTag({content:productionCSS || fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
      await testPage.addScriptTag({content:mock});
      await testPage.evaluate(flags=>Object.assign(window,{openingEnabled:true,openingChatSuccess:true},flags),flags);
      await testPage.addScriptTag({content:bundle.outputFiles[0].text});
      await testPage.getByRole('button',{name:'科研模式',exact:true}).click();
      await testPage.getByRole('button',{name:/^研究方案/}).click();
      await testPage.getByRole('textbox',{name:'研究问题',exact:true}).waitFor();
      return testPage;
    }
    // Native chooser may remain open while the user switches workspace; a late
    // callback must not leak files into the ordinary chat or create a task.
    const lateEntry=await draftEntry({choosePending:true});
    await lateEntry.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    await lateEntry.waitForFunction(()=>typeof window.finishChoose==='function');
    await lateEntry.getByRole('button',{name:'自由对话',exact:true}).click();
    await lateEntry.evaluate(()=>window.finishChoose(['D:/fixture/unsent.txt']));
    await lateEntry.waitForTimeout(100);
    assert.equal(await lateEntry.locator('.pending-attachments').count(),0);
    assert.equal(await lateEntry.evaluate(()=>window.calls.filter(c=>['StartResearch','StartChat','ImportConversationDocumentPaths'].includes(c.m)).length),0);
    await lateEntry.close();

    const returnedEntry=await draftEntry({choosePending:true});
    await returnedEntry.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    await returnedEntry.waitForFunction(()=>typeof window.finishChoose==='function');
    await returnedEntry.getByRole('button',{name:'自由对话',exact:true}).click();
    await returnedEntry.getByRole('button',{name:'科研模式',exact:true}).click();
    await returnedEntry.getByRole('textbox',{name:'研究问题',exact:true}).waitFor();
    await returnedEntry.evaluate(()=>window.finishChoose(['D:/fixture/unsent.txt']));
    await returnedEntry.waitForTimeout(100);
    assert.equal(await returnedEntry.locator('.pending-attachments').count(),0,'old choice leaked after leaving and returning to entry');
    assert.equal(await returnedEntry.evaluate(()=>window.calls.filter(c=>['StartResearch','ImportConversationDocumentPaths','StartChat'].includes(c.m)).length),0);
    await returnedEntry.close();

    // Whole import failure, then partial failure, then transport failure. All
    // retries reuse one owner, do not reimport ready files, and retain the draft.
    const failedEntry=await draftEntry({importFails:true,choosePaths:['D:/fixture/unsent.txt','D:/fixture/bad.txt']});
    const failedDraft=failedEntry.locator('.composer textarea');
    await failedDraft.fill('请核对两份材料');
    await failedEntry.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    await failedEntry.locator('.pending-attachments > div').nth(1).waitFor();
    await failedEntry.getByRole('button',{name:'开始讨论',exact:true}).click();
    await failedEntry.getByText('Fixture: attachment import unavailable',{exact:false}).waitFor();
    assert.equal(await failedEntry.locator('.pending-attachments > div').count(),2);
    assert.equal(await failedDraft.inputValue(),'请核对两份材料');
    assert.equal(await failedEntry.evaluate(()=>window.calls.filter(c=>c.m==='StartChat').length),0);
    await failedEntry.evaluate(()=>{window.importFails=false;window.badPath='D:/fixture/bad.txt'});
    await failedEntry.getByRole('button',{name:'发送',exact:true}).click();
    await failedEntry.getByText(/消息尚未发送/).waitFor();
    assert.equal(await failedEntry.locator('.pending-attachments > div').count(),2);
    assert.equal(await failedEntry.locator('.pending-attachments').getByText('待发送 · 发送时读取',{exact:true}).count(),1);
    assert.equal(await failedEntry.evaluate(()=>window.calls.filter(c=>c.m==='StartChat').length),0);
    await failedEntry.getByRole('button',{name:'移除 bad.txt',exact:true}).click();
    await failedEntry.evaluate(()=>{window.openingChatSuccess=false});
    await failedEntry.getByRole('button',{name:'发送',exact:true}).click();
    await failedEntry.getByText('Fixture captured opening chat; no model called',{exact:false}).waitFor();
    assert.equal(await failedEntry.locator('.pending-attachments > div').count(),1);
    await failedEntry.evaluate(()=>{window.openingChatSuccess=true});
    await failedEntry.getByRole('button',{name:'发送',exact:true}).click();
    await failedEntry.getByText('先看看你希望交付什么，暂不执行研究。',{exact:true}).first().waitFor();
    const failedCalls=await failedEntry.evaluate(()=>window.calls);
    assert.equal(failedCalls.filter(c=>c.m==='StartResearch').length,1);
    assert.equal(failedCalls.filter(c=>c.m==='ImportConversationDocumentPaths').length,2);
    assert.deepEqual(failedCalls.filter(c=>c.m==='StartChat').map(c=>c.args[0].attachmentIds),[['opening-file'],['opening-file']]);
    assert.equal(await failedEntry.locator('.pending-attachments').count(),0);
    await failedEntry.close();

    const limitEntry=await draftEntry();
    await limitEntry.evaluate(()=>window.dropFiles(Array.from({length:21},(_,i)=>'D:/fixture/file-'+i+'.txt')));
    await limitEntry.getByText(/每条消息最多添加 20 个附件/).waitFor();
    assert.equal(await limitEntry.locator('.pending-attachments').count(),0);
    assert.equal(await limitEntry.evaluate(()=>window.calls.filter(c=>['StartResearch','ImportConversationDocumentPaths','StartChat'].includes(c.m)).length),0);
    await limitEntry.close();

    // Open a report-free, reviewed analysis through the original task list.
    const analysisPage = await browser.newPage({viewport:{width:1280,height:900}});
    analysisPage.on('pageerror',error=>errors.push(error.message));
    await analysisPage.setContent('<div id="root"></div>');
    await analysisPage.addStyleTag({content:productionCSS || fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
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
    await repairPage.addStyleTag({content:productionCSS || fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
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
    await currentRepairPage.addStyleTag({content:productionCSS || fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
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
    const missingPage=await browser.newPage({viewport:{width:1280,height:900}});
    missingPage.on('pageerror',error=>errors.push(error.message));
    await missingPage.setContent('<div id="root"></div>');
    await missingPage.addStyleTag({content:productionCSS || fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
    await missingPage.addScriptTag({content:mock});
    await missingPage.evaluate(value=>{
      Object.assign(window,value);
      delete window.detail.evidenceReselection;
      window.detail.reviewRevisionRecommendation={status:'needs_input',summary:'核对测量方案',requiredInputs:['确认交付草稿还是实施前定稿'],repairableCorrections:['交付实际计算结果和独立 Python 脚本'],pendingStartLabel:'实现方法',pendingAffectedStages:['实现方法','执行计算','形成报告','独立审查'],reviewOutputSha256:'review-hash',repeatsSideEffects:false};
    },fixture);
    await missingPage.addScriptTag({content:bundle.outputFiles[0].text});
    await missingPage.getByRole('button',{name:'科研模式',exact:true}).click();
    await missingPage.locator('.workflow-project-tasks article > button').first().click();
    await missingPage.getByText('本次审查仍有未解决的输入',{exact:true}).waitFor();
    const draft=missingPage.locator('#workspace-message-input');
    await draft.fill('保留我的未发送草稿');
    await missingPage.getByRole('button',{name:'添加当前对话附件',exact:true}).click();
    await missingPage.locator('.composer-wrap').getByText('supplement.txt').waitFor();
    assert.equal(await missingPage.getByText('已有修正建议',{exact:true}).count(),1);
    assert.equal(await missingPage.getByText('确认交付草稿还是实施前定稿',{exact:true}).count(),1);
    assert.equal(await missingPage.locator('.research-retry-actions button.primary').count(),0);
    assert.equal(await missingPage.getByRole('button',{name:'调整修改范围',exact:true}).count(),0);
    for(const width of [1280,960]) {
      await missingPage.setViewportSize({width,height:900});
      await missingPage.locator('.research-revision-advice').scrollIntoViewIfNeeded();
      assert.equal(await missingPage.locator('.research-revision-advice').evaluate(el=>el.scrollWidth>el.clientWidth),false);
      assert.equal(await missingPage.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await missingPage.screenshot({path:path.join(repairOutput,`mixed-repair-${width}.png`)});
    }
    assert.equal(await draft.inputValue(),'保留我的未发送草稿');
    assert.equal(await missingPage.locator('.composer-wrap').getByText('supplement.txt').count(),1);
    assert.equal(await missingPage.evaluate(()=>window.calls.filter(c=>c.m==='StartChat').length),0);
    assert.equal(await missingPage.evaluate(()=>window.calls.filter(c=>['Retry','ConfirmResearchRevision','Decide'].includes(c.m)).length),0);
    // New frozen reviews ask a real decision while the Workflow is still
    // running. The shared waterfall card answers the SAME Chat Run; it is not
    // an extra discussion/check round and it does not execute the repair.
    const questionPage=await browser.newPage({viewport:{width:1280,height:900}});
    questionPage.on('pageerror',error=>errors.push(error.message));
    await questionPage.setContent('<div id="root"></div>');
    await questionPage.addStyleTag({content:productionCSS || fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
    await questionPage.addScriptTag({content:mock});
    await questionPage.evaluate(value=>{
      Object.assign(window,value);
      window.detail.run.status='running';window.detail.run.currentStep=2;
      delete window.detail.evidenceReselection;delete window.detail.reviewRevisionRecommendation;
      const review=window.detail.steps.find(s=>s.nodeId==='independent_review');review.status='running';
      window.detail.steps.at(-1).status='queued';
      window.detail.events=[{id:'review-running',type:'workflow.ai_started',payload:{stepId:review.id}}];
      window.timeline.entries=[{id:'event:review-running',sequence:'1',kind:'event',eventType:'workflow.ai_started',runId:'r',conversationId:'c',active:true,step:review,snapshot:{event:{stepId:review.id}}}];
      window.repairChatSnapshot={sequence:1,workflowAI:true,run:{id:'review-chat',conversationId:'c',status:'waiting_user_input',assistantMessageId:'review-assistant',createdAt:'2026-10-12T01:05:00Z'},userInput:{runId:'review-chat',callId:'review-choice',questions:[{id:'age',question:'是否将样本范围限定为成人？',required:true,options:[{id:'adults',label:'仅成人样本',description:'保留现有测量并修订招募范围'},{id:'all',label:'全部年龄',description:'需要额外伦理安排'}]}]},messages:[],toolCalls:[],pendingApprovals:[],runSteps:[]};
      const initial=window.go.wails;
      window.questionDeferred=true;
      window.go.wails=new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
        if(f==='ChatFacade'&&m==='PollLatestRunSnapshot'&&window.questionPollOverride){
          window.calls.push({f,m,args});window.staleQuestionPolls=(window.staleQuestionPolls||0)+1;
          return {revision:String(window.questionPollOverride.sequence),unchanged:false,value:window.questionPollOverride};
        }
        if(f==='ChatFacade'&&m==='RespondToUserInput'){
          window.calls.push({f,m,args});
          const pending=window.repairChatSnapshot;
          if(window.questionDeferred)await new Promise(resolve=>{window.finishQuestion=resolve});
          window.repairChatSnapshot={...pending,sequence:pending.sequence+1,run:{...pending.run,status:'running'},userInput:null};
          return window.repairChatSnapshot;
        }
        return initial[f][m](...args);
      }})});
    },fixture);
    await questionPage.addScriptTag({content:bundle.outputFiles[0].text});
    await questionPage.getByRole('button',{name:'科研模式',exact:true}).click();
    await questionPage.locator('.workflow-project-tasks article > button').first().click();
    const questionCard=questionPage.locator('.user-input-card');await questionCard.waitFor();
    assert.equal(await questionCard.evaluate(el=>!!el.closest('.research-task-timeline')&&!!el.closest('.chat')),true);
    assert.equal(await questionPage.getByRole('dialog').count(),0);
    assert.equal(await questionPage.locator('#workspace-message-input').isEnabled(),true);
    assert.match(await questionPage.locator('#workspace-message-input').getAttribute('placeholder'),/问题卡片/);
    assert.equal(await questionPage.getByRole('button',{name:'添加当前对话附件',exact:true}).isDisabled(),true);
    assert.equal(await questionPage.getByRole('button',{name:'科研流程执行中',exact:true}).count(),0);
    await questionCard.getByRole('radio',{name:/仅成人样本/}).check();
    for(const width of [1280,960]){
      await questionPage.setViewportSize({width,height:900});await questionCard.scrollIntoViewIfNeeded();
      assert.equal(await questionCard.evaluate(el=>el.scrollWidth>el.clientWidth),false);
      assert.equal(await questionPage.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await questionPage.screenshot({path:path.join(repairOutput,`review-inline-question-${width}.png`)});
    }
    await questionCard.getByRole('button',{name:'确定',exact:true}).click();
    await questionPage.waitForFunction(()=>!!window.finishQuestion);
    assert.equal(await questionCard.getByRole('button',{name:'正在提交…',exact:true}).isDisabled(),true);
    await questionPage.evaluate(()=>{window.questionDeferred=false;window.finishQuestion()});
    await questionCard.waitFor({state:'detached'});
    const responses=await questionPage.evaluate(()=>window.calls.filter(c=>c.m==='RespondToUserInput'));
    assert.equal(responses.length,1);assert.equal(responses[0].args[0].runId,'review-chat');assert.equal(responses[0].args[0].callId,'review-choice');
    assert.deepEqual(responses[0].args[0].answers.age.optionIds,['adults']);
    assert.equal(await questionPage.evaluate(()=>window.calls.filter(c=>['StartChat','Retry','ConfirmResearchRevision','Decide'].includes(c.m)).length),0);
    assert.equal(await questionPage.locator('#workspace-message-input').isDisabled(),true);
    // A delayed older wait snapshot cannot resurrect an answered question.
    await questionPage.evaluate(()=>{
      window.questionPollOverride={...window.repairChatSnapshot,sequence:1,run:{...window.repairChatSnapshot.run,status:'waiting_user_input'},userInput:{runId:'review-chat',callId:'review-choice',questions:[{id:'age',question:'过期问题不得重新出现',required:true}]}};
      window.staleQuestionPolls=0;
    });
    await questionPage.waitForFunction(()=>window.staleQuestionPolls>=2);
    assert.equal(await questionCard.count(),0);
    assert.equal(await questionPage.locator('#workspace-message-input').isDisabled(),true);
    // While a later answer is in flight, leaving the task must isolate its
    // eventual response from the new-research entry and its unsent draft.
    await questionPage.evaluate(()=>{
      window.questionPollOverride=null;window.finishQuestion=null;window.questionDeferred=true;
      window.repairChatSnapshot={...window.repairChatSnapshot,sequence:3,run:{...window.repairChatSnapshot.run,status:'waiting_user_input'},userInput:{runId:'review-chat',callId:'review-choice-2',questions:[{id:'scope',question:'请选择下一步研究范围',required:true,options:[{id:'adults',label:'保持成人范围'},{id:'all',label:'调整研究范围'}]}]}};
    });
    await questionCard.waitFor();
    await questionCard.getByRole('radio',{name:'保持成人范围',exact:true}).check();
    await questionCard.getByRole('button',{name:'确定',exact:true}).click();
    await questionPage.waitForFunction(()=>!!window.finishQuestion);
    await questionPage.getByRole('button',{name:'返回科研任务列表',exact:true}).click();
    await questionPage.getByRole('button',{name:'创建新的科研任务',exact:true}).click();
    await questionPage.getByRole('heading',{name:'你想研究什么？',exact:true}).waitFor();
    const entryDraft=questionPage.locator('#workspace-message-input');
    await entryDraft.fill('这是新研究入口的未发送草稿');
    await questionPage.evaluate(()=>{window.questionDeferred=false;window.finishQuestion()});
    await questionPage.waitForFunction(()=>window.repairChatSnapshot.sequence===4);
    await questionPage.waitForTimeout(1800);
    assert.equal(await questionCard.count(),0);
    assert.equal(await questionPage.getByRole('heading',{name:'你想研究什么？',exact:true}).isVisible(),true);
    assert.equal(await entryDraft.inputValue(),'这是新研究入口的未发送草稿');
    assert.equal(await entryDraft.isEnabled(),true);
    assert.equal(await questionPage.evaluate(()=>window.calls.filter(c=>c.m==='RespondToUserInput').length),2);
    assert.equal(await questionPage.evaluate(()=>window.calls.filter(c=>['StartChat','Retry','ConfirmResearchRevision','Decide'].includes(c.m)).length),0);
    // Explicit prerequisite stop has no fake check button, blind Python retry
    // or downstream shortcut; the existing composer is available for new input.
    const blockedPage=await browser.newPage({viewport:{width:1280,height:900}});
    blockedPage.on('pageerror',error=>errors.push(error.message));
    await blockedPage.setContent('<div id="root"></div>');
    await blockedPage.addStyleTag({content:productionCSS || fs.readFileSync('frontend/src/styles.css','utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))});
    await blockedPage.addScriptTag({content:mock});
    await blockedPage.evaluate(value=>{
      Object.assign(window,value);
      delete window.detail.evidenceReselection;delete window.detail.reviewRevisionRecommendation;
      window.detail.run.errorCode='WORKFLOW_RESEARCH_PREREQUISITE_BLOCKED';
      window.detail.steps[0].status='completed';
      const step=window.detail.steps.at(-1);
      step.nodeId='python_analysis';step.nodeKind='python';step.errorCode='WORKFLOW_RESEARCH_PREREQUISITE_BLOCKED';step.errorMessage='需要选择实际测量定义，不是重复绑定';
      const node=window.detail.run.compilation.nodes.find(n=>n.id==='delivery_gate');
      node.id='python_analysis';node.kind='python';delete node.tool;
      window.detail.run.currentStep=step.ordinal;
      window.detail.events=[{id:'blocked',type:'workflow.failed',payload:{stepId:step.id,errorCode:step.errorCode,errorMessage:step.errorMessage}}];
      window.timeline.entries=[{id:'event:blocked',sequence:'1',kind:'event',eventType:'workflow.failed',runId:'r',conversationId:'c',active:true,step,snapshot:{event:{stepId:step.id,errorCode:step.errorCode,errorMessage:step.errorMessage}}}];
    },fixture);
    await blockedPage.addScriptTag({content:bundle.outputFiles[0].text});
    await blockedPage.getByRole('button',{name:'科研模式',exact:true}).click();
    await blockedPage.locator('.workflow-project-tasks article > button').first().click();
    await blockedPage.getByText('本次审查仍有未解决的输入',{exact:true}).waitFor();
    assert.equal(await blockedPage.getByRole('button',{name:'重试当前阶段',exact:true}).count(),0);
    assert.equal(await blockedPage.getByRole('button',{name:'调整修改范围',exact:true}).count(),0);
    assert.equal(await blockedPage.getByRole('button',{name:'让 AI 核对并继续',exact:true}).count(),0);
    assert.equal(await blockedPage.locator('#workspace-message-input').isEnabled(),true);
    assert.equal(await blockedPage.evaluate(()=>window.calls.filter(c=>c.m==='StartChat').length),0);
    assert.equal(await blockedPage.evaluate(()=>window.calls.filter(c=>['Retry','ConfirmResearchRevision','Decide'].includes(c.m)).length),0);
    assert.deepEqual(errors,[]);
    console.log('PASS: entry and original citation/revision regression; running review uses same-run inline questions; late old wait snapshots and in-flight replies after leaving the task cannot revive the card or overwrite a new-entry draft; unresolved inputs preserve complete advice without check/planning/retry button or new chat; explicit prerequisite has no blind retry/report shortcut; 1280/960 no overflow. Offline React/facade fixture, no model/native EXE/formal DB.');
  } finally { await browser.close(); }
})().catch(error => {console.error(error);process.exit(1);});
