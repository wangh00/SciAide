// Real App/React with offline facade responses; never writes formal task state.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve(process.env.SCIAIDE_REPORT_UI_OUTPUT || '.tmp/report-presentation-ui');
const capture = process.env.SCIAIDE_REPORT_CAPTURE ? JSON.parse(fs.readFileSync(process.env.SCIAIDE_REPORT_CAPTURE,'utf8')) : null;
const revisionCapture = process.env.SCIAIDE_REVISION_CAPTURE ? JSON.parse(fs.readFileSync(process.env.SCIAIDE_REVISION_CAPTURE,'utf8')) : null;
// Only the saved content is captured. Availability is an explicit offline facade fixture.
const revisionProposal = revisionCapture ? {...JSON.parse(revisionCapture.proposals.find(p=>p.status==='pending').proposal_json),status:'pending',canConfirm:true} : null;
const dataCapture = capture && JSON.parse(capture.run.outputs_json).analysis && !JSON.parse(capture.run.outputs_json).citations?.length;
const citations = capture ? JSON.parse(capture.run.outputs_json).citations || [] : [{reference:'[K-0123456789AB]',sourceName:'source.pdf',locator:'page:18',quote:'Original bounded evidence.',quoteSha256:'a'.repeat(64)}];
const report = capture ? JSON.parse(capture.run.outputs_json).report_draft : {markdown:'# 方法比较\n\n## 比较结果\n\n有边界的原文判断 [K-0123456789AB]。\n\n| 方法 | 原文支持 |\n| --- | --- |\n| 方法 A | 原文证据 [K-0123456789AB] |\n\n## 研究边界\n\n'+('当前证据不能直接外推为实际应用效果。\n\n'.repeat(30)),claimSummary:['有原文支持的判断 [K-0123456789AB]'],methodSummary:'比较已核读原文。',limitations:['未核验应用效果。'],confidence:'low'};
const audit = capture ? JSON.parse(capture.steps.find(step=>step.node_id==='independent_review').input_json).evidenceSelectionAudit : {selectedExcerptCount:17,missingRecommendedReferences:Array(12).fill('missing'),screeningStrength:'limited'};
const quality = {coverage:audit?.screeningStrength || 'unknown',limitedDraft:!!audit,selectedExcerpts:audit?.selectedExcerptCount || 0,unselectedRecommended:audit?.missingRecommendedReferences.length || 0,confidencePolicy:dataCapture?'claim_based':'legacy_limited'};
const reference = citations[0]?.reference || '[K-0123456789AB]';
const dataFixture = dataCapture ? {
  steps:capture.steps.map(s=>({id:s.id,nodeId:s.node_id,status:s.status,attempt:s.attempt,ordinal:s.ordinal,input:JSON.parse(s.input_json||'{}'),output:JSON.parse(s.output_json||'{}')})),
  metrics:fs.readFileSync(path.join(path.dirname(process.env.SCIAIDE_REPORT_CAPTURE),'artifacts',`dynamic-${capture.run.id}-1-metrics.csv`),'utf8').trim().split(/\r?\n/).map(line=>line.split(',')),
} : null;
const structured = {format:'markdown',blocks:[{kind:'heading',level:1,text:'原文比较报告'},{kind:'paragraph',text:`正文与证据 ${reference}`},{kind:'heading',level:2,text:'方法与结果'},{kind:'table',rows:[['方法','证据','局限'],['Method A',`原文 ${reference}`,'不能直接外推']]} ,...Array.from({length:20},(_,i)=>({kind:'paragraph',text:`第 ${i+1} 项核读说明：${'保持研究边界与来源原文。'.repeat(8)}`})),{kind:'heading',level:2,text:'未核验内容'},{kind:'quote',text:`引用原文 ${reference}`},{kind:'code',text:`literal ${reference} <script>not executed</script>`}]};

(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const bundle=await build({stdin:{contents:`import React from 'react'; import {createRoot} from 'react-dom/client'; import App from './App'; createRoot(document.getElementById('root')).render(<App/>);`,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const browser=await chromium.launch({channel:'msedge',headless:true});
  const errors=[];
  try {
    const page=await browser.newPage({viewport:{width:1280,height:1000}});
    page.on('pageerror',e=>errors.push(e.message));
    await page.setContent('<div id="root"></div>');
    const styles=process.env.SCIAIDE_COMPILED_STYLES ? fs.readdirSync('frontend/dist/assets').filter(f=>f.endsWith('.css')).map(f=>fs.readFileSync(`frontend/dist/assets/${f}`,'utf8')).join('\n') : ['styles.css','referenceMaterials.css','modal.css'].map(f=>fs.readFileSync(`frontend/src/${f}`,'utf8').replace('@import "./researchCard.css";',fs.readFileSync('frontend/src/researchCard.css','utf8'))).join('\n');
    await page.addStyleTag({content:styles});
    await page.evaluate(({report,citations,quality,structured,dataFixture,revisionProposal})=>{
      window.calls=[];
      window.copiedCode='';
      const nodes=[...(dataFixture?[{id:'python_analysis',name:'运行 Python',kind:'python'}]:[]),{id:'report_drafting',name:'形成报告',kind:'agent_stage'},{id:'independent_review',name:'独立审查',kind:'agent_stage'},{id:'delivery_gate',name:'交付检查',kind:'tool'}];
      const compilation={nodes,edges:[],outputs:[]};
      window.owner={workflow:{id:'w',projectId:'p',name:'文献比较报告',currentVersionId:'v',version:1},versions:[{id:'v',workflowId:'w',definition:{schemaVersion:1,name:'文献比较报告',nodes,edges:[],inputs:[],outputs:[]},compilation}]};
      window.conversation={id:'c',projectId:'p',title:'文献比較任务',permissionMode:'full_access',modelProfileId:'model1',modelId:'fixture-model',reasoningLevel:'medium'};
      window.detail={run:{id:'r',workflowId:'w',workflowVersionId:'v',projectId:'p',researchTaskId:'t',workflowName:'文献比较报告',workflowPurpose:'user_plan',conversationId:'c',status:'completed',createdAt:'2026-10-07T12:02:05Z',inputs:{},outputs:{report_draft:report,citations,delivery_gate:{approved:true}},compilation},steps:[],events:[{id:'e',type:'workflow.completed',payload:{},createdAt:'2026-10-07T12:02:05Z'}],pendingApprovals:[],aiExecutions:[],artifactCount:1,registeredDeliverables:['report_draft'],deliveryAssessment:{status:'reviewed',label:'证据综述稿',report:quality,checks:[],limitations:['原模型审查记录保留']}};
      // Completion card is the current run, not an invented history snapshot.
      window.timeline={taskId:'t',latestRunId:'r',conversationId:'c',hasMore:false,entries:[{id:'event:e',sequence:'1',kind:'event',eventType:'workflow.completed',runId:'r',conversationId:'c',active:true,snapshot:{event:{},outputs:window.detail.run.outputs}}]};
      if(revisionProposal) {
        window.detail.revisionProposals=[revisionProposal];
        window.timeline.entries.push({id:`proposal:${revisionProposal.id}`,sequence:'2',kind:'proposal',runId:'r',conversationId:'c',active:true,proposal:revisionProposal,snapshot:{}});
      }
      const version=(id,n)=>({id,artifactId:'a',versionNumber:n,fileName:'report.md',mimeType:'text/markdown',sizeBytes:9000,sha256:'a'.repeat(64),sourceKind:'tool',provenance:{modelId:'fixture-model',toolName:'reviewed report',skills:[]},lineage:[],citations,exports:[],createdAt:'2026-10-07T12:02:05Z'});
      window.artifact={id:'a',projectId:'p',researchTaskId:'t',scopeKind:'task',name:'文献比较 · 本轮报告',kind:'document',status:'active',currentVersionId:'a2',currentVersion:version('a2',2)};
      window.artifactDetail={artifact:window.artifact,versions:[version('a2',2),version('a1',1),{...version('csv',0),fileName:'data.csv',mimeType:'text/csv'}]};
      window.previews={a2:{versionId:'a2',kind:'document',mimeType:'text/markdown',truncated:false,document:structured},a1:{versionId:'a1',kind:'document',mimeType:'text/markdown',truncated:true,document:{format:'markdown',blocks:[{kind:'heading',level:1,text:'旧版报告'},{kind:'paragraph',text:'旧版独立文本'},{kind:'heading',level:2,text:'旧版边界'}]}},csv:{versionId:'csv',kind:'document',mimeType:'text/csv',truncated:false,document:{format:'csv',blocks:[{kind:'table',rows:[['group','value'],['A','12']]}]}}};
      if(dataFixture) {
        window.detail.steps=dataFixture.steps;
        window.detail.deliveryAssessment.kind='analysis_result';
        window.detail.deliveryAssessment.label='计算分析结果';
        window.detail.registeredDeliverables=[];
        const artifacts=dataFixture.steps.flatMap(s=>s.output.artifacts||[]);
        window.detail.artifactCount=artifacts.length;
        window.artifacts=artifacts.map((a,i)=>({id:`file${i}`,projectId:'p',researchTaskId:'t',scopeKind:'task',name:a.name,kind:a.mimeType.startsWith('image/')?'image':'document',status:'active',currentVersionId:`filev${i}`,currentVersion:{...version(`filev${i}`,1),artifactId:`file${i}`,fileName:a.name,mimeType:a.mimeType,sizeBytes:a.sizeBytes,sha256:a.sha256}}));
        window.artifact=window.artifacts.find(a=>a.name.endsWith('metrics.csv'));
        window.artifactDetail={artifact:window.artifact,versions:[window.artifact.currentVersion]};
        window.previews[window.artifact.currentVersionId]={versionId:window.artifact.currentVersionId,kind:'document',mimeType:'text/csv',truncated:false,document:{format:'csv',blocks:[{kind:'table',rows:dataFixture.metrics}]}};
      }
      window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
        window.calls.push({f,m,args});
        if(m==='ListProjects') return [{id:'p',name:'报告阅读测试',workspacePath:'D:/Fixture',workspaceKind:'external'}];
        if(m==='ListModelProfiles') return [{id:'model1',name:'Fixture',models:[{id:'fixture-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,reasoningLevels:[],enabled:true,isDefault:true}],secretConfigured:true,enabled:true,isDefault:true,modelId:'fixture-model',customHeaders:{},apiProtocol:'openai_chat_completions',baseUrl:'http://fixture.invalid'}];
        if(m==='ListProjectRuns') return [window.detail.run];
        if(m==='Get') return window.owner;
        if(m==='List'&&f==='WorkflowFacade') return [window.owner.workflow];
        if(m==='GetRun') return window.detail;
        if(m==='PollRun') return {revision:'1',unchanged:false,value:window.detail};
        if(m==='GetConversation') return window.conversation;
        if(m==='ListConversations') return [window.conversation];
        if(m==='ResearchTimeline') return window.timeline;
        if(m==='PollResearchTimeline') return {revision:'1',unchanged:false,value:window.timeline};
        if(m==='GetLatestRunSnapshot') return null;
        if(m==='PollLatestRunSnapshot') return {revision:'1',unchanged:false,value:null};
        if(['ListTaskArtifacts','ListArtifacts'].includes(m)) return window.artifacts || [window.artifact];
        if(m==='DownloadExecutedPythonCode') return 'D:/Fixture/analysis-source.py';
        if(['GetTaskArtifact','GetArtifact'].includes(m)) return window.artifactDetail;
        if(['PreviewTaskArtifactVersion','PreviewArtifactVersion'].includes(m)) return window.previews[args.at(-1)];
        if(m==='ListResearchTasks') return [{id:'t',projectId:'p',title:'文献比较任务',researchQuestion:'原始比较目标',status:'completed'}];
        if(m.startsWith('List')) return [];
        if(m==='GetNetworkConfig') return {global:{mode:'direct',url:''},modules:{},revision:0};
        if(['Start','StartChat','Retry','Decide','ConfirmResearchRevision'].includes(m)) throw Error('No execution allowed in UI fixture');
        return {};
      }})})};
      window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:()=>{},OnFileDropOff:()=>{},ClipboardSetText:async value=>{window.copiedCode=value;}};
    },{report,citations,quality,structured,dataFixture,revisionProposal});
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:'科研模式',exact:true}).click();
    await page.locator('.workflow-project-tasks article > button').first().click();
    const card=page.getByRole('region',{name:'本轮研究报告'});
    await card.waitFor({timeout:10000}).catch(async error=>{fs.writeFileSync(path.join(output,'failed-page.txt'),await page.locator('body').innerText());fs.writeFileSync(path.join(output,'failed-calls.json'),JSON.stringify(await page.evaluate(()=>window.calls),null,2));await page.screenshot({path:path.join(output,'failed-page.png')});throw error;});
    if(revisionProposal) {
      const proposal=page.getByRole('region',{name:'返修确认方案'});
      await proposal.getByRole('button',{name:'确认方案并开始返修',exact:true}).waitFor();
      assert.equal(await proposal.locator('button').count(),1);
      assert.equal(await proposal.locator('.research-revision-goal').textContent(),revisionProposal.summary);
      assert.equal(await proposal.locator('.research-revision-goal').evaluate(el=>getComputedStyle(el).fontWeight),'400');
      assert.equal(await proposal.locator('.research-revision-changes').getAttribute('open'),null);
      for(const cls of ['borderRadius','borderColor','boxShadow','fontFamily']) assert.equal(await proposal.evaluate((el,cls)=>getComputedStyle(el)[cls],cls),await card.evaluate((el,cls)=>getComputedStyle(el)[cls],cls));
      assert.equal(await proposal.locator('.research-card-badge').evaluate(el=>getComputedStyle(el).color),'rgb(137, 103, 45)');
      for(const width of [1280,960]) {
        await page.setViewportSize({width,height:1600});
        await proposal.scrollIntoViewIfNeeded();
        assert.equal(await proposal.evaluate(el=>el.scrollWidth>el.clientWidth),false);
        await proposal.screenshot({path:path.join(output,`revision-${width}.png`)});
        await proposal.locator('.research-revision-changes > summary').click();
        assert.deepEqual(await proposal.locator('.research-revision-changes > ol > li').allTextContents(),revisionProposal.changes);
        assert.equal(await proposal.evaluate(el=>el.scrollWidth>el.clientWidth),false);
        // A locator screenshot taller than the chat viewport intersects the fixed composer.
        await page.setViewportSize({width,height:Math.ceil(await proposal.evaluate(el=>el.getBoundingClientRect().height))+400});
        await proposal.scrollIntoViewIfNeeded();
        await proposal.screenshot({path:path.join(output,`revision-expanded-${width}.png`)});
        await proposal.locator('.research-revision-changes > summary').click();
      }
      for(const file of revisionProposal.sourceVersion?.files || []) {
        const identity=proposal.locator('.research-revision-identity').filter({hasText:file.sha256});
        await identity.locator('summary').click();
        for(const value of [file.relativePath,file.sha256,file.attachmentId]) assert.ok((await identity.textContent()).includes(value));
        await identity.locator('summary').click();
      }
      await page.evaluate(()=>window.dispatchEvent(new CustomEvent('research-discussion-sent',{detail:'other-conversation'})));
      assert.equal(await proposal.locator('button').count(),1);
      await page.evaluate(()=>window.dispatchEvent(new CustomEvent('research-discussion-sent',{detail:'c'})));
      await proposal.getByText('旧方案已失效',{exact:true}).waitFor();
      assert.equal(await proposal.locator('button').count(),0);
      assert.equal(await proposal.locator('.research-card-history').getAttribute('open'),null);
      await proposal.locator('.research-card-history > summary').click();
      await proposal.locator('.research-revision-changes > summary').click();
      assert.deepEqual(await proposal.locator('.research-revision-changes > ol > li').allTextContents(),revisionProposal.changes);
      await proposal.screenshot({path:path.join(output,'revision-invalidated.png')});
      assert.deepEqual(errors,[]);
      const mutations=(await page.evaluate(()=>window.calls)).filter(c=>['Start','StartChat','Retry','Decide','ConfirmResearchRevision','SaveDeliverable'].includes(c.m));
      assert.equal(mutations.length,0);
      fs.writeFileSync(path.join(output,'result.json'),JSON.stringify({source:'captured pending proposal content + real App offline facade',proposalId:revisionProposal.id,checks:['shared frame with report','normal-weight summary','complete changes disclosure','source identity preserved','1280/960 no overflow','single confirmation action not clicked','other conversation does not invalidate','same-conversation old scheme immediately disabled','history remains readable'],errors,mutations},null,2));
      console.log('PASS: captured proposal in actual App; shared frame, complete text, responsive layout and invalidation; no model/state mutation.');
      return;
    }
    if(dataFixture) {
      const files=card.getByRole('region',{name:'本次生成的文件'});
      assert.equal(await files.locator(':scope > ul > li').count(),8);
      assert.match(await card.textContent(),/绑定数据与计算产物/);
      assert.equal(await card.locator('.confidence-medium').textContent(),'中');
      assert.equal(await card.getByText('覆盖未单独标注',{exact:true}).count(),0);
      for(const width of [1280,960]) {
        await page.setViewportSize({width,height:1000});
        await page.locator('.chat').evaluate(el=>{el.scrollTop=0;});
        await page.waitForTimeout(150);
        assert.equal(await card.evaluate(el=>el.scrollWidth>el.clientWidth),false);
        assert.equal(await files.evaluate(el=>el.scrollWidth>el.clientWidth),false);
        assert.equal(await files.locator('ul').evaluate(el=>getComputedStyle(el).listStyleType),'none');
        assert.equal(await files.locator('button').first().evaluate(el=>getComputedStyle(el).borderRadius),'7px');
        assert.equal(await files.locator('ul li').first().evaluate(el=>getComputedStyle(el).fontSize),'12px');
        assert.equal(await files.locator('ul li').first().getAttribute('title').then(t=>t.includes(capture.run.id)),true);
        await page.screenshot({path:path.join(output,`data-report-${width}.png`)});
        // Capture the file panel without the fixed composer covering its last rows.
        await page.setViewportSize({width,height:1800});
        await files.screenshot({path:path.join(output,`data-files-${width}.png`)});
        await page.setViewportSize({width,height:1000});
      }
      await files.locator('details').last().locator('summary').click();
      const code=dataFixture.steps.find(s=>s.nodeId==='python_analysis').input.code;
      assert.equal(await files.locator('details').last().locator('pre code').textContent(),code);
      await files.locator('details').last().getByRole('button',{name:'复制代码',exact:true}).click();
      assert.equal(await page.evaluate(()=>window.copiedCode),code);
      assert.equal(await files.locator('details').last().getByRole('button',{name:'复制代码',exact:true}).textContent(),'已复制');
      await files.getByRole('button',{name:'下载执行代码 .py',exact:true}).last().click();
      assert.deepEqual((await page.evaluate(()=>window.calls)).find(c=>c.m==='DownloadExecutedPythonCode').args,['p','r','python_analysis',1]);
      await files.getByRole('button',{name:'查看、预览或下载产物',exact:false}).click();
      await page.getByRole('button',{name:/dynamic-.*-metrics.csv/}).last().click();
      const modal=page.locator('.resource-detail-dialog');await modal.waitFor();
      await modal.getByRole('cell',{name:'ols',exact:true}).waitFor();
      assert.ok((await modal.textContent()).includes(dataFixture.metrics[2][2]));
      assert.equal(await modal.getByRole('region',{name:'报告阅读区'}).count(),0);
      await page.screenshot({path:path.join(output,'data-metrics-preview.png')});
      assert.deepEqual(errors,[]);
      assert.equal((await page.evaluate(()=>window.calls)).filter(c=>['Start','StartChat','Retry','Decide','ConfirmResearchRevision','SaveDeliverable'].includes(c.m)).length,0);
      fs.writeFileSync(path.join(output,'result.json'),JSON.stringify({source:'real saved data run + real App offline facade',files:8,confidence:report.confidence,checks:['same report card','short filenames with original tooltip','responsive grid and styled action','successful frozen code display and download request','actual metrics CSV preview','no execution or DB mutation'],errors},null,2));
      console.log('PASS: real saved data report, 8 files, source download request and metrics preview; no model/state mutation.');
      return;
    }
    await card.getByText('范围受限的研究初稿',{exact:true}).waitFor();
    assert.equal(await card.locator('.confidence-low').textContent(),'低');
    assert.match(await card.textContent(),/旧版有限证据提示要求低置信/);
    assert.equal(await card.getByText('最终交付',{exact:true}).count(),0);
    assert.equal(await card.locator('.citation-unverified').count(),0);
    const reader=card.getByRole('region',{name:'报告阅读区'});
    await reader.locator('.report-reader-contents').waitFor();
    for(const width of [1280,960]) {
      await page.setViewportSize({width,height:1000});
      await page.locator('.chat').evaluate(el=>{el.scrollTop=0;});
      await page.waitForTimeout(150);
      assert.equal(await card.evaluate(el=>el.scrollWidth>el.clientWidth),false);
      assert.ok(await card.locator('.report-card-heading > div').evaluate(el=>el.getBoundingClientRect().width)>=220);
      assert.ok(await card.locator('.report-reviewed-badge').evaluate(el=>el.getBoundingClientRect().height)<40);
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await page.screenshot({path:path.join(output,`report-${width}.png`)});
    }
    const originalText=await reader.locator('.markdown-body').textContent();
    await reader.getByRole('button',{name:'舒适字号',exact:true}).click();
    assert.equal(await reader.locator('.markdown-body').evaluate(el=>getComputedStyle(el).fontSize),'15px');
    await reader.locator('.report-reader-contents > summary').click();
    const nav=reader.getByRole('navigation',{name:'报告章节'});
    await nav.getByRole('button').last().click();
    assert.ok(await reader.locator('.report-reader-body').evaluate(el=>el.scrollTop)>0);
    await reader.getByRole('button',{name:'展开阅读区',exact:true}).click();
    assert.equal(await reader.locator('.report-reader-body').evaluate(el=>getComputedStyle(el).maxHeight),'none');
    await nav.getByRole('button').first().click();
    assert.equal(await reader.evaluate(el=>el.contains(document.activeElement)),true);
    assert.equal(await reader.locator('.markdown-body').textContent(),originalText);
    await reader.getByRole('button',{name:'收起阅读区',exact:true}).click();
    await reader.getByRole('button',{name:'查看引用 1',exact:true}).first().click();
    const citationDialog=page.getByRole('dialog',{name:'引用文献与证据'});
    await citationDialog.waitFor();
    assert.ok((await citationDialog.textContent()).includes(citations[0].locator));
    await citationDialog.getByRole('button',{name:'关闭引用详情'}).click();

    await card.getByRole('button',{name:'已登记 · 查看报告',exact:true}).click();
    await page.getByRole('button',{name:'文献比较 · 本轮报告',exact:false}).last().click({timeout:10000}).catch(async error=>{fs.writeFileSync(path.join(output,'artifact-failed-page.txt'),await page.locator('body').innerText());fs.writeFileSync(path.join(output,'artifact-failed-calls.json'),JSON.stringify(await page.evaluate(()=>window.calls),null,2));await page.screenshot({path:path.join(output,'artifact-failed-page.png')});throw error;});
    const modal=page.locator('.resource-detail-dialog');
    await modal.waitFor();
    const artifactReader=modal.getByRole('region',{name:'报告阅读区'});
    await artifactReader.waitFor();
    assert.equal(await artifactReader.locator('.citation-marker').count(),3);
    assert.equal(await artifactReader.locator('pre').textContent(),`literal ${reference} <script>not executed</script>`);
    assert.equal(await artifactReader.locator('tbody tr').count(),2);
    const paragraph=artifactReader.locator('.report-document > p').first();
    assert.equal(await paragraph.evaluate(el=>getComputedStyle(el).fontSize),'13px');
    await artifactReader.getByRole('button',{name:'舒适字号',exact:true}).click();
    assert.equal(await paragraph.evaluate(el=>getComputedStyle(el).fontSize),'15px');
    await artifactReader.getByRole('button',{name:'展开阅读区',exact:true}).click();
    for(const width of [1280,960]) {
      await page.setViewportSize({width,height:1000});
      assert.equal(await modal.evaluate(el=>el.scrollWidth>el.clientWidth),false);
      await page.screenshot({path:path.join(output,`artifact-${width}.png`)});
    }
    await artifactReader.locator('.citation-marker').first().click();
    await citationDialog.waitFor();
    await citationDialog.getByRole('button',{name:'关闭引用详情'}).click();
    await modal.getByRole('combobox').first().selectOption('a1');
    await artifactReader.getByText('旧版独立文本',{exact:true}).waitFor();
    assert.equal(await artifactReader.getByRole('button',{name:'舒适字号',exact:true}).getAttribute('aria-pressed'),'false');
    assert.equal(await artifactReader.getByRole('button',{name:'展开阅读区',exact:true}).getAttribute('aria-expanded'),'false');
    assert.equal(await artifactReader.locator('.report-reader-body').evaluate(el=>el.scrollTop),0);
    assert.equal(await artifactReader.getByText('原文比较报告',{exact:true}).count(),0);
    await modal.getByText('预览已按结构或长度截断',{exact:true}).waitFor();
    await modal.getByRole('combobox').first().selectOption('csv');
    await modal.getByRole('cell',{name:'12',exact:true}).waitFor();
    assert.equal(await modal.getByRole('region',{name:'报告阅读区'}).count(),0);
    assert.deepEqual(errors,[]);
    const calls=await page.evaluate(()=>window.calls);
    assert.equal(calls.filter(c=>['Start','StartChat','Retry','Decide','ConfirmResearchRevision'].includes(c.m)).length,0);
    fs.writeFileSync(path.join(output,'result.json'),JSON.stringify({source:capture?'saved report replay':'synthetic fixture',confidence:report.confidence,selected:quality.selectedExcerpts,unselected:quality.unselectedRecommended,errors,checks:['current report card','preserved low score','separate coverage','headings','font toggle','expand and navigation','citation dialogs','artifact table and literal code','version reset','truncation label','CSV remains table','no model/state mutation']},null,2));
    console.log('PASS: real App report/artifact UI, citations, reader state, version reset; no model called.');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
