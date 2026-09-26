const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
(async()=>{let app=fs.readFileSync('frontend/src/App.tsx','utf8');app=app.replace('export default function App','function App');
// Render the actual application with controlled backend fixtures.
const mock=`crypto.randomUUID=()=> '00000000-0000-4000-8000-'+String(Date.now()).slice(-12);window.archiveCalls=[];window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
if(m==='ListProjects')return [{id:'p',name:'网络测试项目',workspacePath:'D:/Project',workspaceKind:'external'}];
if(m==='ListModelProfiles')return [{id:'model1',name:'研究主模型',baseUrl:'https://example.org/v1',apiProtocol:'openai_responses',models:[{id:'research-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,contextWindowSource:'manual',reasoningLevels:['high'],enabled:true,isDefault:true}],secretConfigured:true,customHeaders:{},enabled:true,isDefault:true,modelId:'research-model',timeoutSeconds:60}];
if(m==='ListMCPServers')return [{id:'mcp1',name:'文献工具',namespace:'literature',transport:'stdio',status:'stopped',enabled:true,trust:'user_trusted',args:[],env:{},headers:{},secretNames:[],command:'node',toolCount:3,resourceCount:0,promptCount:0}];
if(m==='ListConversationActivity')return window.sidebarActivity||[];
if(m==='ListConversations')return window.conversations||[];
if(m==='CreateConversation'){const c={...args[0],id:'c'+(window.conversations?.length||0),title:args[0].title||'新会话',autoTitlePending:!args[0].title,permissionMode:'plan'};window.conversations=[...(window.conversations||[]),c];window.created=c;return c;}
if(m==='GetConversation')return window.conversations.find(c=>c.id===args[0]);
if(m==='SetReasoningLevel'){const c=window.conversations.find(c=>c.id===args[0]);c.reasoningLevel=args[1];return {...c};}
if(m==='ListResearchTasks'||m==='ListMessages')return [];
if(m==='GetLatestRunSnapshot'||m==='PollLatestRunSnapshot'||m==='GetRunSnapshot')return window.currentRun?{run:window.currentRun,sequence:1,messages:[],toolCalls:[],runSteps:[],pendingApprovals:[]}:null;
if(m==='StartChat'){window.lastCommand=args[0];const c=window.conversations.find(c=>c.id===args[0].conversationId);if(c.autoTitlePending&&args[0].text){c.title=args[0].text;c.autoTitlePending=false;}window.startCalls=(window.startCalls||0)+1;window.currentRun={id:'run'+window.startCalls,conversationId:args[0].conversationId,status:'running',createdAt:new Date().toISOString(),modelId:'research-model',requestedReasoningLevel:'high'};return window.currentRun;}
if(m==='CancelRun'){window.cancelCalls=(window.cancelCalls||0)+1;if(window.failCancel)throw Error('cancel unavailable');return;}
if(m==='ListReferenceMaterials'||m==='ListWorkflows'||m==='ListProjectRuns'||m==='ListWorkflowTemplates')return [];

if(m==='ExportProject'||m==='RestoreProject'){window.archiveCalls.push(m);return {}}

if(m==='ListSearchChannels')return [{provider:'baidu',enabled:true,priority:1,configured:true},{provider:'firecrawl',enabled:false,priority:2,configured:false},{provider:'duckduckgo',enabled:true,priority:99,configured:true}];
if(m==='GetNetworkConfig')return window.savedNetwork||{global:{mode:'direct',url:''},modules:{},revision:0};
if(m==='SaveNetworkConfig'){window.savedNetwork=args[0];return;}
if(m==='ListSkills')return {skills:[{name:'anndata',description:'Annotated data for single-cell analysis. 用于研究数据整理、验证及分析。',category:'biology',tags:['biology'],routingAliases:[],origin:'default',entry:true,enabled:true,allowedTools:[],contentHash:'abc',packageHash:'abc',fileCount:6,referenceCount:5,assetCount:0,scriptCount:0,instructionRunes:1000,overridden:[],capability:'requires_dependency',capabilityReason:'需要项目 Python 依赖',requiredTools:[],missingTools:[],pythonPackages:['anndata'],cliDependencies:[],externalServices:[],capabilityLimitations:[]}],categories:[{name:'biology',count:1}],diagnostics:[],capabilities:{requires_dependency:1},defaultCount:1,enabledCount:1};
if(m==='GetProjectEnvironment')return {state:'ready',environmentKind:'workspace_managed',lock:[],environmentPythonPath:'D:/Project/.sciaide/python/venv/Scripts/python.exe'};
if(m==='DetectInterpreters')return {interpreters:[],status:'ready'};
return {};
}})})};window.handlers={};window.emit=(name,event)=>(window.handlers[name]||[]).forEach(fn=>fn(event));window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:(name,fn)=>{(window.handlers[name]??=[]).push(fn);return ()=>{window.handlers[name]=window.handlers[name].filter(v=>v!==fn)}},OnFileDrop:()=>{},OnFileDropOff:()=>{}};`;
const bundle=await build({stdin:{contents:app+`\nimport {createRoot} from 'react-dom/client';createRoot(document.getElementById('root')).render(<App/>);`,resolveDir:path.resolve('frontend/src'),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
const browser=await chromium.launch({channel:'msedge',headless:true});try{const page=await browser.newPage({viewport:{width:1280,height:900}});const errors=[];page.on('pageerror',e=>{errors.push(e.message);console.error(e.message)});await page.setContent('<div id="root"></div>');await page.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8')+fs.readFileSync('frontend/src/applicationSettings.css','utf8')});await page.addScriptTag({content:mock});await page.addScriptTag({content:bundle.outputFiles[0].text});

await page.getByRole('button',{name:'自由对话',exact:true}).click();
await page.getByRole('button',{name:'新建会话',exact:true}).click();await page.waitForFunction(()=>window.created?.title==='新会话');assert.equal(await page.getByRole('dialog').count(),0);
await page.evaluate(()=>{window.sidebarActivity=[{conversationId:window.created.id,status:'running'}];window.emit('sciaide:run-event',{type:'run.started',aggregateId:'background',payload:{}})});
await page.locator('.conversation-running.running').waitFor();
assert.ok(await page.locator('.conversation-running').evaluate(e=>e.getBoundingClientRect().right<=e.nextElementSibling.getBoundingClientRect().left));
await page.getByRole('button',{name:'新建会话',exact:true}).click();
await page.locator('.conversation-row.active').waitFor();
assert.equal(await page.locator('.conversation-row:not(.active) .conversation-running').count(),1);
await page.evaluate(()=>{window.sidebarActivity[0].status='waiting_approval';window.emit('sciaide:run-event',{type:'run.waiting_approval',aggregateId:'background',payload:{}})});
await page.locator('.conversation-running.waiting_approval').waitFor();
await page.evaluate(()=>{window.sidebarActivity=[];window.emit('sciaide:run-event',{type:'run.cancelled',aggregateId:'background',payload:{}})});
await page.locator('.conversation-running').waitFor({state:'detached'});
assert.equal(await page.locator('.topbar .reasoning-picker').count(),0);
assert.equal(await page.locator('.composer-buttons .reasoning-picker + .attach').count(),1);
assert.equal(await page.getByLabel('思考强度',{exact:true}).inputValue(),'high');
assert.equal(await page.locator('.web-search-toggle + .reasoning-picker').count(),1);
assert.equal(await page.getByRole('button',{name:'联网搜索',exact:true}).getAttribute('aria-pressed'),'false');
await page.getByRole('button',{name:'联网搜索',exact:true}).click();
assert.equal(await page.getByRole('button',{name:'联网搜索',exact:true}).getAttribute('aria-pressed'),'true');
const input=page.locator('.composer textarea');await input.fill('Explain IV');await page.getByRole('button',{name:'发送',exact:true}).click();
await page.getByRole('button',{name:'停止生成',exact:true}).waitFor();await page.locator('.conversation-row').filter({hasText:'Explain IV'}).waitFor();
assert.equal(await page.evaluate(()=>window.lastCommand.webSearchEnabled),true);assert.ok(await page.getByRole('button',{name:'联网搜索',exact:true}).isDisabled());
await input.fill('Next question');await input.press('Enter');assert.equal(await page.evaluate(()=>window.startCalls),1);assert.equal(await input.inputValue(),'Next question');
await page.evaluate(()=>window.failCancel=true);await page.getByRole('button',{name:'停止生成',exact:true}).click();await page.getByText('cancel unavailable',{exact:true}).waitFor();assert.equal(await input.inputValue(),'Next question');await page.evaluate(()=>window.failCancel=false);
await page.getByRole('button',{name:'停止生成',exact:true}).click();await page.getByRole('button',{name:'正在停止',exact:true}).waitFor();assert.equal(await page.evaluate(()=>window.cancelCalls),2);assert.ok(await page.getByRole('button',{name:'正在停止',exact:true}).isDisabled());
await page.evaluate(()=>{window.currentRun={...window.currentRun,status:'cancelled'};window.emit('sciaide:run-event',{aggregateId:window.currentRun.id,sequence:3,type:'run.cancelled',payload:{run:window.currentRun}})});
await page.getByRole('button',{name:'发送',exact:true}).waitFor();assert.equal(await input.inputValue(),'Next question');
await page.getByRole('button',{name:'联网搜索',exact:true}).click();await page.getByRole('button',{name:'发送',exact:true}).click();await page.getByRole('button',{name:'停止生成',exact:true}).waitFor();assert.equal(await page.evaluate(()=>window.startCalls),2);assert.equal(await page.evaluate(()=>window.lastCommand.webSearchEnabled),false);
await page.evaluate(()=>{window.currentRun={...window.currentRun,status:'completed'};window.emit('sciaide:run-event',{aggregateId:window.currentRun.id,sequence:4,type:'run.completed',payload:{run:window.currentRun}})});await page.getByRole('button',{name:'发送',exact:true}).waitFor();
await page.evaluate(()=>window.currentRun=null);
await page.getByRole('button',{name:'科研模式',exact:true}).click();assert.equal(await page.locator('.composer-buttons .reasoning-picker + .attach').count(),1);
await page.getByRole('button',{name:'自由对话',exact:true}).click();
for(const width of [1280,960]){await page.setViewportSize({width,height:900});await page.waitForTimeout(200);const bounds=await page.locator('.composer-buttons').evaluate(e=>({right:e.getBoundingClientRect().right,width:innerWidth}));assert.ok(bounds.right<=bounds.width);await page.screenshot({path:`build/qa/composer-controls-${width}.png`});}
assert.deepEqual(errors,[]);console.log('PASS actual composer placement, high default, stop-only run action, Enter preserves draft, cancellation pending, completion reset, research placement and 1280/960 layout');
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});
