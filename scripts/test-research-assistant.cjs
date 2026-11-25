// Offline rendered-App fixture: controlled facade only, no native app, model or user DB.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const out = path.resolve(process.env.SCIAIDE_UI_FIXTURE_OUTPUT || '.tmp/diagnostics/20261112-research-assistant-first-slice/ui');
const cssFiles = ['styles.css','referenceMaterials.css','modal.css','researchMaterialsLibrary.css','recordIdentity.css','applicationSettings.css'];
const css = cssFiles.map(file => fs.readFileSync(path.join('frontend/src',file),'utf8').replace('@import "./researchCard.css";', fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n');
const existing = fs.readFileSync('scripts/test-research-conversation-entry.cjs','utf8');
const mockStart = existing.indexOf('const mock = `');
const mockEnd = existing.indexOf('`;', mockStart);
assert.ok(mockStart >= 0 && mockEnd > mockStart, 'existing controlled facade fixture is unavailable');
const baseline = Function('return '+existing.slice(mockStart+'const mock = '.length,mockEnd+1))();
const failures = [], errors = [], requests = [], observations = [];
function check(condition,message) { if(!condition) {failures.push(message); console.error('FAIL:',message);} else console.log('PASS:',message); }
async function shot(page,name) { await page.screenshot({path:path.join(out,name),animations:'disabled'}); }
async function fit(page,selector) {
 return page.locator(selector).evaluate(el=>{const rect=el.getBoundingClientRect();return {left:rect.left,right:rect.right,top:rect.top,bottom:rect.bottom,width:rect.width,height:rect.height,scrollWidth:el.scrollWidth,clientWidth:el.clientWidth,visible:getComputedStyle(el).display!=='none'};});
}

(async()=>{
 fs.mkdirSync(out,{recursive:true});
 const bundle=await build({stdin:{contents:`import React from 'react';import {createRoot} from 'react-dom/client';import App from './App';createRoot(document.getElementById('root')).render(<App/>);`,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
 const browser=await chromium.launch({channel:'msedge',headless:true});
 const context=await browser.newContext({viewport:{width:1280,height:900}});
 await context.setOffline(true);
 await context.route('**/*',route=>{requests.push(route.request().url());return route.abort();});
 const page=await context.newPage();
 page.setDefaultTimeout(7000);
 page.on('pageerror',e=>errors.push(`pageerror: ${e.message}`));
 page.on('console',msg=>{if(msg.type()==='error') errors.push(`console: ${msg.text()}`);});
 try {
  await page.setContent('<!doctype html><html><head><meta charset="utf-8"></head><body><div id="root"></div></body></html>');
  await page.addStyleTag({content:css});
  await page.addScriptTag({content:baseline});
  await page.evaluate(()=>{
   const initial=window.go.wails;
   const when='2026-11-12T01:00:00Z';
   const emptyCompilation={nodes:[],edges:[],order:[],inputs:[],outputs:[]};
   const task=(key,question,formal=false)=>{
    const workflowId=`workflow-${key}`, taskId=`task-${key}`, conversationId=`conversation-${key}`;
    const compilation=formal?{...emptyCompilation,nodes:[{id:'work',name:'受控执行',kind:'agent_stage'}],order:['work']}:{...emptyCompilation};
    const run={id:`run-${key}`,projectId:'p',workflowId,workflowVersionId:`version-${key}`,workflowName:question,researchTaskId:taskId,conversationId,workflowPurpose:formal?'user_plan':'research_starter',permissionMode:'full_access',status:formal?'running':'completed',inputs:formal?{}:{discussion_only:true,starter_context:{researchIdea:question}},outputs:{},compilation,createdAt:when,updatedAt:when};
    const steps=formal?[{id:`step-${key}`,workflowRunId:run.id,nodeId:'work',nodeKind:'agent_stage',ordinal:1,attempt:1,status:'running',input:{},output:{}}]:[];
    const workflow={id:workflowId,projectId:'p',purpose:run.workflowPurpose,name:question,currentVersionId:run.workflowVersionId,version:1,createdAt:when,updatedAt:when};
    const researchDiscussion=formal?undefined:{projectId:'p',researchTaskId:taskId,conversationId,primaryConversationId:conversationId,sourceWorkflowRunId:run.id,canExplore:true};
    return {run,detail:{run,researchDiscussion,steps,events:[],pendingApprovals:[],aiExecutions:[],toolActivities:[],artifactCount:0},owner:{workflow,versions:[{id:run.workflowVersionId,workflowId,version:1,definition:{name:question,nodes:[],edges:[],inputs:[],outputs:[]},compilation}]},conversation:{id:conversationId,projectId:'p',title:question,modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium',permissionMode:'full_access'}};
   };
   const a=task('a','甲研究：识别证据缺口'), b=task('b','乙研究：比较抽样方法'), c=task('c','丙研究：执行中',true);
   const free={id:'free-chat',projectId:'p',title:'普通对话',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium',permissionMode:'plan'};
   window.uiFixture={items:[a,b,c], free, queries:[], calls:[], stateDelay:null, stateError:null, sourceError:null, basisError:null, searchMode:'ok', sequence:0};
   const working=(item)=>({id:`state-${item.run.researchTaskId}-${item.noteVersion||2}`,projectId:'p',researchTaskId:item.run.researchTaskId,version:item.noteVersion||2,currentFocus:item===a?'甲当前关注：核对样本':'乙当前关注：比较方法',rationale:'受控夹具：先识别证据缺口',nextStep:'受控夹具：比较材料',unknowns:['尚未核实样本范围'],sourceChatRunId:`chat-${item.run.id}`,sourceUserMessageId:`message-${item.run.id}`,sourceCallId:`call-${item.run.id}`,createdAt:when,status:'working_note'});
   const basisContent='保存摘要🧪<script>window.badBasis=true</script> 仍未核验';
   const basis=(task)=>({readCallId:`basis-read-${task}`,status:'linked_unverified_abstract',queryId:'assistant-query',candidateId:'assistant-candidate',sha256:'a'.repeat(64),offset:10,endOffset:10+Array.from(basisContent).length,totalRunes:100});
   const queryResult=(command,count=0)=>{const id=`query-${++window.uiFixture.sequence}`;const q={id,projectId:'p',researchTaskId:command.researchTaskId||undefined,text:command.query,sourceIds:['fixture-source'],limitPerSource:20,sources:[{sourceId:'fixture-source',status:'ok',count,retryable:false}],partial:false,resultCount:count,createdAt:when,updatedAt:when};return {query:q,page:{items:[],total:0,offset:0,limit:20}};};
   window.uiFixture.queries.push({...queryResult({researchTaskId:'task-a',query:'助手已保存的检索'},1).query,id:'assistant-query'});
   const savedCandidate={id:'assistant-candidate',projectId:'p',candidateKey:'fixture-record',reviewStatus:'pending',importStatus:'not_imported',preferred:{sourceId:'fixture-source',sourceRecordId:'fixture-record',title:'助手发现的待核验论文',abstract:'受控摘要，仅为候选。',authors:[],identifiers:{},year:2025,openAccess:false},records:[],aliases:[],createdAt:when,updatedAt:when};
   savedCandidate.records=[{id:'assistant-source-record',projectId:'p',work:savedCandidate.preferred,firstSeenAt:when,updatedAt:when}];
   window.go.wails=new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
    const s=window.uiFixture; s.calls.push({f,m,args});
    if(f==='ConversationFacade') {
     if(m==='ListConversations') return [free];
     if(m==='GetConversation') return s.items.find(x=>x.conversation.id===args[0])?.conversation||free;
     if(m==='ListMessages') return [];
    }
    if(f==='WorkflowFacade') {
     if(m==='List') return s.items.map(x=>x.owner.workflow);
     if(m==='ListProjectRuns') return s.items.map(x=>x.run);
     if(m==='Get') return s.items.find(x=>x.run.workflowId===args[1])?.owner;
     if(m==='GetRun') return s.items.find(x=>x.run.id===args[1])?.detail;
     if(m==='PollRun') return {revision:'fixture-'+(s.items.find(x=>x.run.id===args[1])?.run.status||'missing'),unchanged:false,value:s.items.find(x=>x.run.id===args[1])?.detail};
     if(m==='GetCurrentResearchTaskRun') return s.items.find(x=>x.run.researchTaskId===args[1])?.detail;
     if(m==='GetRunByConversation') {const item=s.items.find(x=>x.conversation.id===args[0]);return item?{found:true,run:item.detail}:{found:false};}
     if(m==='GetPendingResearchAdoption') return null;
     if(m==='GetResearchWorkingState') {
      const item=s.items.find(x=>x.run.researchTaskId===args[1]);
      if(s.stateDelay===args[1]) return await new Promise(resolve=>{window.finishDelayedState=()=>resolve(working(item));});
      if(s.stateError===args[1]) throw Error('fixture state read failed');
      return item ? working(item) : null;
     }
     if(m==='GetResearchWorkingStateProvenance') {
      const item=s.items.find(x=>x.run.researchTaskId===args[1]), note=working(item);
      if(args[0]!=='p'||args[2]!==note.version)throw Error('fixture provenance scope/version changed');
      if(s.sourceError===args[1])throw Error('fixture provenance read failed');
      return {projectId:'p',researchTaskId:args[1],stateId:note.id,version:note.version,status:'unverified_submission_provenance',fields:['currentFocus','rationale','nextStep','unknowns'].map(field=>{const v=field==='nextStep'?note.version:1;return {field,status:'recorded',stateId:`state-${args[1]}-${v}`,version:v,sourceChatRunId:`source-chat-${args[1]}-${v}`,sourceUserMessageId:`source-human-${args[1]}-${v}`,sourceCallId:`source-call-${args[1]}-${v}`,createdAt:when,...(field==='rationale'?{basis:[basis(args[1])]}:{})};})};
     }
     if(m==='GetResearchWorkingStateBasis') {
      const note=working(s.items.find(x=>x.run.researchTaskId===args[1]));
      if(args[0]!=='p'||args[2]!==note.version||args[3]!=='rationale'||args[4]!==basis(args[1]).readCallId)throw Error('fixture basis scope changed');
      if(s.basisError===args[1])throw Error('fixture original page unavailable');
      return {projectId:'p',researchTaskId:args[1],stateId:note.id,version:note.version,field:'rationale',basis:basis(args[1]),content:basisContent};
     }
     if(m==='ResearchTimeline'||m==='PollResearchTimeline') {const item=s.items.find(x=>x.run.researchTaskId===args[0].taskId);const value={taskId:args[0].taskId,latestRunId:item?.run.id||'',conversationId:item?.conversation.id||'',entries:[],hasMore:false};return m==='ResearchTimeline'?value:{revision:'fixture-1',unchanged:false,value};}
     if(m==='Cancel') {const item=s.items.find(x=>x.run.id===args[1]);item.run.status='cancelled';return item.detail;}
    }
    if(f==='ResearchFacade') {
     if(m==='Catalog') return [{id:'fixture-source',name:'Fixture Source',domain:'local',description:'Fixture',homepage:'',host:'',keyFree:true,fullText:false}];
     if(m==='QueryOrigins') return [{id:'all',title:'全部检索',count:s.queries.length},{id:'manual',title:'手动检索',count:s.queries.filter(q=>!q.researchTaskId).length},{id:'task-a',title:'甲任务历史',count:s.queries.filter(q=>q.researchTaskId==='task-a').length},{id:'task-b',title:'乙任务历史',count:s.queries.filter(q=>q.researchTaskId==='task-b').length}];
     if(m==='QueryHistory') return s.queries.filter(q=>args[0].origin==='all'||(args[0].origin==='manual'?!q.researchTaskId:q.researchTaskId===args[0].origin));
     if(m==='ListCandidates') return args[0].queryId==='assistant-query' ? {items:[savedCandidate],total:1,offset:0,limit:20} : {items:[],total:0,offset:0,limit:20};
     if(m==='Search') {
      const command=args[0];
      if(s.searchMode==='fail') throw Error('fixture search failed');
      if(s.searchMode==='delay') return await new Promise(resolve=>{window.finishDelayedSearch=()=>resolve(queryResult(command,999));});
      const result=queryResult(command);s.queries.unshift(result.query);return result;
     }
    }
    if(f==='ChatFacade') {
     if(m==='ListConversationActivity') return [];
     if(m==='GetLatestRunSnapshot') return null;
     if(m==='PollLatestRunSnapshot'||m==='PollRunSnapshot') return {revision:'fixture-1',unchanged:false,value:null};
     if(m==='StartChat') throw Error('Unexpected model call in UI fixture');
    }
    if(f==='PythonFacade'&&m==='GetProjectEnvironment') return {state:'ready'};
    if(f==='PythonFacade'&&m==='DetectInterpreters') return {interpreters:[]};
    return initial[f][m](...args);
   }})});
  });
  await page.addScriptTag({content:bundle.outputFiles[0].text});
  await page.locator('#project').selectOption('p');
  check(await page.locator('.research-working-state').count()===0,'普通聊天不显示任务工作状态');
  await shot(page,'ordinary-chat-1280.png');
  await page.getByRole('button',{name:'科研模式',exact:true}).click();
  await page.locator('.workflow-project-tasks').waitFor();
  async function openTask(text) {await page.locator('.workflow-project-tasks article').filter({hasText:text}).locator('button').first().click();}
  await openTask('甲研究');
  await page.getByText('甲当前关注：核对样本',{exact:true}).waitFor();
  check(await page.getByText('研究讨论 · 模型工作记录 · 未核验',{exact:true}).isVisible(),'真实 App 渲染未核验的研究工作记录');
  check(await page.getByText('下一步建议 · 未自动执行').isVisible(),'下一步未自动执行的边界可见');
  const sourceCalls=()=>page.evaluate(()=>window.uiFixture.calls.filter(c=>c.m==='GetResearchWorkingStateProvenance').length);
  const basisCalls=()=>page.evaluate(()=>window.uiFixture.calls.filter(c=>c.m==='GetResearchWorkingStateBasis').length);
  check(await sourceCalls()===0,'打开工作记录不自动读取字段历史');
  const sourceDetails=page.locator('.research-working-source');
  await sourceDetails.locator(':scope > summary').click();
  await page.getByText('工作理由 · 继承自版本 1',{exact:true}).waitFor();
  check(await page.getByText('下一步建议 · 本版显式提交',{exact:true}).isVisible(),'稀疏记录区分继承字段和本版显式提交');
  check(await sourceCalls()===1,'展开字段来源仅调用一次只读 Facade');
  check(await basisCalls()===0,'字段关联只显示元数据，未展开不读摘要正文');
  const basisDetails=page.locator('.research-working-basis');
  await basisDetails.locator('summary').click();await basisDetails.locator('blockquote').waitFor();
  check((await basisDetails.locator('blockquote').innerText()).includes('<script>window.badBasis=true</script>'),'关联页按原样纯文本呈现，不当作 HTML');
  check(await page.evaluate(()=>window.badBasis===undefined),'来源文本不能执行脚本或作为宿主指令');
  check(await basisCalls()===1,'展开摘要仅一次只读调用，无联网/模型/导入');
  await basisDetails.scrollIntoViewIfNeeded();await shot(page,'working-basis-1280.png');
  await basisDetails.locator('summary').click();await basisDetails.locator('summary').click();
  check(await basisCalls()===1,'同版关联摘要再次展开复用原页');
  await basisDetails.locator('summary').click();
  await shot(page,'working-field-sources-1280.png');
  await page.getByText('下一步建议 · 本版显式提交',{exact:true}).scrollIntoViewIfNeeded();await shot(page,'working-field-sources-scrolled-1280.png');
  await sourceDetails.locator(':scope > summary').click();await sourceDetails.locator(':scope > summary').click();
  check(await sourceCalls()===1,'同一不可变版本再次展开复用只读结果');
  await page.evaluate(()=>{window.uiFixture.sourceError='task-a';window.uiFixture.items[0].noteVersion=3;window.dispatchEvent(new CustomEvent('research-message-saved',{detail:{projectId:'p',taskId:'task-a'}}));});
  await page.getByText(/^版本 3 ·/).waitFor();
  check(await sourceDetails.getAttribute('open')===null,'工作记录换版后旧来源面板关闭，不冒认本版来源');
  check(await page.locator('.research-working-basis blockquote').count()===0,'换版后不展示旧摘要正文或沿用旧关联读取');
  check(await sourceCalls()===1,'换版不会自动查询字段历史');
  await sourceDetails.locator(':scope > summary').click();await page.getByText(/无法核对当前版本的字段来源/).waitFor();
  check(await page.getByText('工作理由 · 继承自版本 1',{exact:true}).count()===0,'字段来源读取失败不继续显示旧版本来源');
  await page.evaluate(()=>{window.uiFixture.sourceError=null;});
  await page.getByRole('button',{name:'重试字段来源',exact:true}).click();await page.getByText('工作理由 · 继承自版本 1',{exact:true}).waitFor();
  check(await sourceCalls()===3,'明确重试只读来源，不生成新工作记录或规划');
  await page.evaluate(()=>{window.uiFixture.basisError='task-a';});
  await basisDetails.locator('summary').click();await page.getByRole('button',{name:'重试原摘要页',exact:true}).waitFor();
  check(await page.locator('.research-working-basis blockquote').count()===0,'摘要读取失败不回退到旧版正文');
  await page.evaluate(()=>{window.uiFixture.basisError=null;});
  await page.getByRole('button',{name:'重试原摘要页',exact:true}).click();await basisDetails.locator('blockquote').waitFor();
  check(await basisCalls()===3,'摘要错误只明确重试一次，不自动取源或生成替代内容');
  await basisDetails.locator('summary').click();
  await sourceDetails.locator(':scope > summary').click();
  const mutating=['StartChat','StartResearch','Start','AdoptResearchRoute','ConfirmResearchRevision'];
  check((await page.evaluate(ms=>window.uiFixture.calls.filter(c=>ms.includes(c.m)).length,mutating))===0,'打开任务和记录没有调用模型或规划');
  await page.evaluate(()=>{const a=window.uiFixture.items[0];a.run.status='running';a.run.inputs.discussion_only=false;a.run.conversationId='candidate-conversation-a';a.detail.researchDiscussion.conversationId=a.run.conversationId;});
  await page.getByRole('button',{name:'取消',exact:true}).waitFor({state:'visible'});
  check(await page.locator('#workspace-message-input').isEnabled(),'候选规划进行中仍可在原人类讨论编辑消息');
  check(await page.getByText('研究讨论可继续',{exact:true}).isVisible(),'规划运行与研究讨论的差别可见');
  check(await page.getByText('候选规划中',{exact:true}).isVisible(),'候选运行不再标为科研执行中');
  check((await page.evaluate(()=>window.uiFixture.calls.filter(c=>c.f==='ConversationFacade'&&c.m==='GetConversation').at(-1)?.args[0]))==='conversation-a','查看规划没有切换到内部 AI 会话');
  await shot(page,'planning-discussion-1280.png');
  await page.evaluate(()=>{window.uiFixture.items[0].run.status='failed';});
  await page.getByRole('button',{name:'取消',exact:true}).waitFor({state:'hidden'});
  check(await page.locator('#workspace-message-input').isEnabled(),'候选失败不锁定研究讨论');
  check(await page.getByText('候选规划失败',{exact:true}).isVisible(),'失败明确归属于候选规划而非整个科研任务');
  await shot(page,'failed-planning-discussion-1280.png');
  await page.evaluate(()=>{const a=window.uiFixture.items[0];a.run.status='completed';a.run.inputs.discussion_only=true;a.run.conversationId='conversation-a';a.detail.researchDiscussion.conversationId=a.run.conversationId;});
  await page.getByText('围绕研究问题持续探索',{exact:true}).waitFor();
  for(const [width,height] of [[1280,900],[960,720],[900,720],[880,720],[760,720]]) {
   await page.setViewportSize({width,height});await page.waitForTimeout(250);
   const mobile=width<=880;
   if(mobile) {const toggle=page.locator('.research-mobile-toggle');await toggle.waitFor({state:'visible'});if(await toggle.getAttribute('aria-expanded')!=='true')await toggle.click();}
   const info=await page.evaluate(()=>({viewport:innerWidth,htmlScroll:document.documentElement.scrollWidth,bodyScroll:document.body.scrollWidth,bodyMin:getComputedStyle(document.body).minWidth}));
   const panel=await fit(page,'.research-route-panel');
   const tool=page.getByRole('button',{name:'本任务文献发现'});await tool.scrollIntoViewIfNeeded();
   const toolBounds=await fit(page,'.research-working-tools button');
   observations.push({width,height,...info,panel,tool:toolBounds});
   check(info.htmlScroll<=width && info.bodyScroll<=width,`${width}px 无横向溢出 (html=${info.htmlScroll}, body=${info.bodyScroll})`);
   check(toolBounds.left>=0 && toolBounds.right<=width+1 && toolBounds.bottom<=height+1,`${width}px 本任务工具可见于视口`);
   check(panel.visible && panel.left>=0 && panel.right<=width+1,`${width}px 研究状态面板可见于视口`);
   await shot(page,`discussion-${width}.png`);
   if(mobile) await page.getByRole('button',{name:'收起研究面板'}).click();
  }
  await page.setViewportSize({width:1280,height:900});
  const taskTool=page.getByRole('button',{name:'本任务文献发现'});await taskTool.scrollIntoViewIfNeeded();await taskTool.click();
  const dialog=page.getByRole('dialog',{name:'文献发现'});await dialog.waitFor();
  await dialog.getByText('助手发现的待核验论文',{exact:false}).first().waitFor();
  check(await dialog.getByText('助手已保存的检索',{exact:true}).count()>0,'打开本任务发现即可看到助手已保存的查询与候选');
  check((await page.evaluate(()=>window.uiFixture.calls.filter(c=>c.f==='ResearchFacade'&&c.m==='Search').length))===0,'查看助手已保存发现不重复检索');
  await shot(page,'assistant-saved-discovery-1280.png');
  await dialog.getByRole('button',{name:'乙任务历史'}).click();
  await dialog.getByPlaceholder('输入题名、作者、DOI、主题词或布尔检索式').fill('任务内检索');
  await dialog.getByRole('button',{name:'检索并保存到本任务'}).click();
  await dialog.getByText('已保存 0 条来源记录。').waitFor();
  let searchCalls=await page.evaluate(()=>window.uiFixture.calls.filter(c=>c.f==='ResearchFacade'&&c.m==='Search'));
  check(searchCalls.at(-1).args[0].researchTaskId==='task-a','切换历史筛选后检索仍写入原任务 task-a');
  await shot(page,'task-search-1280.png');
  await page.evaluate(()=>{window.uiFixture.searchMode='fail';});
  await dialog.getByPlaceholder('输入题名、作者、DOI、主题词或布尔检索式').fill('失败检索');
  await dialog.getByRole('button',{name:'检索并保存到本任务'}).click();
  await dialog.getByText('fixture search failed',{exact:false}).waitFor();
  check(await dialog.getByText('已保存 0 条来源记录。').count()===0,'检索失败不显示上次成功');
  await shot(page,'search-error-1280.png');
  await page.evaluate(()=>{window.uiFixture.searchMode='delay';});
  await dialog.getByPlaceholder('输入题名、作者、DOI、主题词或布尔检索式').fill('延迟检索');
  await dialog.getByRole('button',{name:'检索并保存到本任务'}).click();
  await page.waitForFunction(()=>typeof window.finishDelayedSearch==='function');
  await dialog.getByRole('button',{name:'关闭文献发现'}).click();
  await page.getByRole('button',{name:'返回科研任务列表'}).click();
  await openTask('乙研究');
  await page.getByText('乙当前关注：比较方法',{exact:true}).waitFor();
  const taskBTool=page.getByRole('button',{name:'本任务文献发现'});await taskBTool.scrollIntoViewIfNeeded();await taskBTool.click();
  const nextDialog=page.getByRole('dialog',{name:'文献发现'});await nextDialog.waitFor();
  await page.evaluate(()=>window.finishDelayedSearch());await page.waitForTimeout(250);
  check(await nextDialog.getByText('已保存 999 条来源记录。').count()===0,'旧任务迟到检索响应不写入新任务窗口');
  searchCalls=await page.evaluate(()=>window.uiFixture.calls.filter(c=>c.f==='ResearchFacade'&&c.m==='Search'));
  check(searchCalls.at(-1).args[0].researchTaskId==='task-a','已发出的延迟搜索保留原任务归属');
  await shot(page,'late-search-new-task-1280.png');
  await nextDialog.getByRole('button',{name:'关闭文献发现'}).click();
  await page.evaluate(()=>{window.uiFixture.stateError='task-b';window.dispatchEvent(new CustomEvent('research-message-saved',{detail:{projectId:'p',taskId:'task-b'}}));});
  await page.getByText('无法读取研究记录。').waitFor();
  check(await page.getByText('乙当前关注：比较方法',{exact:true}).count()===0,'读取失败不伪装为成功或继续展示旧记录');
  await shot(page,'state-error-1280.png');
  await page.evaluate(()=>{window.uiFixture.stateError=null;});
  await page.getByRole('button',{name:'重试',exact:true}).click();
  await page.getByText('乙当前关注：比较方法',{exact:true}).waitFor();
  await page.getByRole('button',{name:'返回科研任务列表'}).click();
  await page.evaluate(()=>{window.uiFixture.stateDelay='task-a';});
  await openTask('甲研究');
  await page.waitForFunction(()=>typeof window.finishDelayedState==='function');
  await page.getByRole('button',{name:'返回科研任务列表'}).click();
  await openTask('乙研究');
  await page.getByText('乙当前关注：比较方法',{exact:true}).waitFor();
  await page.evaluate(()=>window.finishDelayedState());await page.waitForTimeout(250);
  check(await page.getByText('乙当前关注：比较方法',{exact:true}).isVisible() && await page.getByText('甲当前关注：核对样本',{exact:true}).count()===0,'旧任务迟到状态不会覆盖当前任务');
  await shot(page,'late-state-new-task-1280.png');
  await page.getByRole('button',{name:'自由对话',exact:true}).click();
  check(await page.locator('.research-working-state').count()===0,'返回普通聊天后任务状态卸载');
  await page.evaluate(()=>{window.uiFixture.searchMode='ok';});
  await page.getByRole('button',{name:'打开文献发现'}).click();
  const globalDialog=page.getByRole('dialog',{name:'文献发现'});await globalDialog.waitFor();
  await globalDialog.getByPlaceholder('输入题名、作者、DOI、主题词或布尔检索式').fill('普通检索');
  await globalDialog.getByRole('button',{name:'检索并保存',exact:true}).click();
  await globalDialog.getByText('已保存 0 条来源记录。').waitFor();
  searchCalls=await page.evaluate(()=>window.uiFixture.calls.filter(c=>c.f==='ResearchFacade'&&c.m==='Search'));
  check(!('researchTaskId' in searchCalls.at(-1).args[0]),'普通聊天的文献搜索不带任务 ID');
  await shot(page,'global-search-1280.png');await globalDialog.getByRole('button',{name:'关闭文献发现'}).click();
  await page.getByRole('button',{name:'科研模式',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.workflow-project-tasks')||document.querySelector('button[aria-label="返回科研任务列表"]'));
  const backToTasks=page.getByRole('button',{name:'返回科研任务列表'});
  if(await backToTasks.isVisible()) await backToTasks.click();
  await page.locator('.workflow-project-tasks').waitFor();await openTask('丙研究');
  await page.setViewportSize({width:960,height:720});
  const desktopCancel=page.getByRole('button',{name:'取消',exact:true});await desktopCancel.waitFor({state:'visible'});await desktopCancel.scrollIntoViewIfNeeded();
  const desktopCancelBox=await desktopCancel.boundingBox();
  check(await page.getByText('执行中',{exact:true}).isVisible(),'正式执行仍使用原执行状态，不冒认探索权限');
  check(desktopCancelBox && desktopCancelBox.x>=0 && desktopCancelBox.x+desktopCancelBox.width<=960 && desktopCancelBox.y+desktopCancelBox.height<=720,'960px 活动任务取消控件可见');
  await shot(page,'active-task-cancel-960.png');
  await page.setViewportSize({width:880,height:720});
  await page.getByRole('button',{name:'研究路线与控制'}).click();
  const cancel=page.getByRole('button',{name:'取消',exact:true});await cancel.waitFor({state:'visible'});await cancel.scrollIntoViewIfNeeded();
  const cancelBox=await cancel.boundingBox();
  check(cancelBox && cancelBox.x>=0 && cancelBox.x+cancelBox.width<=880 && cancelBox.y+cancelBox.height<=720,'880px 活动任务取消控件可见且可点击');
  await shot(page,'active-task-cancel-880.png');await cancel.click();
  check((await page.evaluate(()=>window.uiFixture.calls.filter(c=>c.f==='WorkflowFacade'&&c.m==='Cancel').length))===1,'取消只调用受控 WorkflowFacade.Cancel');
  check((await page.evaluate(ms=>window.uiFixture.calls.filter(c=>ms.includes(c.m)).length,mutating))===0,'整个受控验收未启动模型、研究或路线');
 } catch(error) {
  failures.push(`交互夹具异常: ${error.stack||error}`);console.error(error.stack||error);
  try {fs.writeFileSync(path.join(out,'failure-debug.json'),JSON.stringify(await page.evaluate(()=>({calls:window.uiFixture?.calls.slice(-80),text:document.body.innerText.slice(0,12000)})),null,2),'utf8');await shot(page,'unexpected-failure.png');} catch {}
 } finally {
  await page.waitForTimeout(100);
  if(errors.length) failures.push(...errors);
  if(requests.length) failures.push(`意外网络请求: ${requests.join(', ')}`);
  fs.writeFileSync(path.join(out,'result.json'),JSON.stringify({observations,failures,errors,requests},null,2),'utf8');
  await context.close();await browser.close();
 }
 console.log(`UI fixture: ${failures.length ? 'FAIL' : 'PASS'}; ${failures.length} issue(s); output ${out}`);
 if(failures.length) process.exitCode=1;
})().catch(error=>{console.error(error);process.exitCode=1;});
