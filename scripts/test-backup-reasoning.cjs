const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
(async()=>{let app=fs.readFileSync('frontend/src/App.tsx','utf8');app=app.replace('export default function App','function App');
// Render the actual application with controlled backend fixtures.
const mock=`window.archiveCalls=[];window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
if(m==='ListProjects')return [{id:'p',name:'网络测试项目',workspacePath:'D:/Project',workspaceKind:'external'}];
if(m==='ListModelProfiles')return [{id:'model1',name:'研究主模型',baseUrl:'https://example.org/v1',apiProtocol:'openai_responses',models:[{id:'research-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,contextWindowSource:'manual',reasoningLevels:['high'],enabled:true,isDefault:true}],secretConfigured:true,customHeaders:{},enabled:true,isDefault:true,modelId:'research-model',timeoutSeconds:60}];
if(m==='ListMCPServers')return [{id:'mcp1',name:'文献工具',namespace:'literature',transport:'stdio',status:'stopped',enabled:true,trust:'user_trusted',args:[],env:{},headers:{},secretNames:[],command:'node',toolCount:3,resourceCount:0,promptCount:0}];
if(m==='ListConversations')return window.conversations||[];
if(m==='CreateConversation'){const c={...args[0],id:'c'+(window.conversations?.length||0),title:args[0].title||'新会话',autoTitlePending:!args[0].title,permissionMode:'plan'};window.conversations=[...(window.conversations||[]),c];window.created=c;return c;}
if(m==='GetConversation')return window.conversations.find(c=>c.id===args[0]);
if(m==='SetReasoningLevel'){const c=window.conversations.find(c=>c.id===args[0]);c.reasoningLevel=args[1];return {...c};}
if(m==='ListResearchTasks'||m==='ListMessages')return [];
if(m==='GetLatestRunSnapshot'||m==='PollLatestRunSnapshot')return null;
if(m==='ExportProject'||m==='RestoreProject'){window.archiveCalls.push(m);return {}}

if(m==='ListSearchChannels')return [{provider:'baidu',enabled:true,priority:1,configured:true},{provider:'firecrawl',enabled:false,priority:2,configured:false},{provider:'duckduckgo',enabled:true,priority:99,configured:true}];
if(m==='GetNetworkConfig')return window.savedNetwork||{global:{mode:'direct',url:''},modules:{},revision:0};
if(m==='SaveNetworkConfig'){window.savedNetwork=args[0];return;}
if(m==='ListSkills')return {skills:[{name:'anndata',description:'Annotated data for single-cell analysis. 用于研究数据整理、验证及分析。',category:'biology',tags:['biology'],routingAliases:[],origin:'default',entry:true,enabled:true,allowedTools:[],contentHash:'abc',packageHash:'abc',fileCount:6,referenceCount:5,assetCount:0,scriptCount:0,instructionRunes:1000,overridden:[],capability:'requires_dependency',capabilityReason:'需要项目 Python 依赖',requiredTools:[],missingTools:[],pythonPackages:['anndata'],cliDependencies:[],externalServices:[],capabilityLimitations:[]}],categories:[{name:'biology',count:1}],diagnostics:[],capabilities:{requires_dependency:1},defaultCount:1,enabledCount:1};
if(m==='GetProjectEnvironment')return {state:'ready',environmentKind:'workspace_managed',lock:[],environmentPythonPath:'D:/Project/.sciaide/python/venv/Scripts/python.exe'};
if(m==='DetectInterpreters')return {interpreters:[],status:'ready'};
return {};
}})})};window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:()=>{},OnFileDropOff:()=>{}};`;
const bundle=await build({stdin:{contents:app+`\nimport {createRoot} from 'react-dom/client';createRoot(document.getElementById('root')).render(<App/>);`,resolveDir:path.resolve('frontend/src'),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
const browser=await chromium.launch({channel:'msedge',headless:true});try{const page=await browser.newPage({viewport:{width:1280,height:900}});const errors=[];page.on('pageerror',e=>{errors.push(e.message);console.error(e.message)});await page.setContent('<div id="root"></div>');await page.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8')+fs.readFileSync('frontend/src/applicationSettings.css','utf8')});await page.addScriptTag({content:mock});await page.addScriptTag({content:bundle.outputFiles[0].text});
await page.getByRole('button',{name:'从备份导入项目',exact:true}).click();
await page.getByRole('heading',{name:'从备份导入项目',exact:true}).waitFor();
assert.equal(await page.evaluate(()=>window.archiveCalls.length),0);
await page.getByRole('button',{name:'取消',exact:true}).click();
await page.getByRole('button',{name:'从备份导入项目',exact:true}).click();await page.getByRole('button',{name:'选择备份文件',exact:true}).click();
await page.waitForFunction(()=>window.archiveCalls.length===1);
await page.getByRole('button',{name:'导出项目备份',exact:true}).click();await page.getByText(/备份不加密/).waitFor();assert.equal(await page.evaluate(()=>window.archiveCalls.length),1);
await page.getByRole('button',{name:'选择保存位置',exact:true}).click();await page.waitForFunction(()=>window.archiveCalls.length===2);
assert.deepEqual(await page.evaluate(()=>window.archiveCalls),['RestoreProject','ExportProject']);
await page.getByRole('button',{name:'自由对话',exact:true}).click();
async function createChat(){const count=await page.evaluate(()=>window.conversations?.length||0);await page.getByRole('button',{name:'新建会话',exact:true}).click();await page.waitForFunction(n=>window.conversations?.length===n+1,count);assert.equal(await page.getByRole('dialog').count(),0);assert.equal(await page.evaluate(()=>window.created.reasoningLevel),'high')}
await createChat('default-high');
const effort=page.locator('select').filter({has:page.locator('option[value="xhigh"]')});await effort.selectOption('medium');await page.waitForFunction(()=>window.conversations[0].reasoningLevel==='medium');
await createChat('still-high');assert.equal(await page.evaluate(()=>window.conversations[0].reasoningLevel),'medium');
assert.deepEqual(errors,[]);console.log('PASS backup confirmation/cancel and new chat high default independent of existing override');
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});
