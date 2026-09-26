const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
(async()=>{let app=fs.readFileSync('frontend/src/App.tsx','utf8');app=app.replace('export default function App','function App');
// Render the actual application with controlled backend fixtures.
const mock=`window.go={wails:new Proxy({}, {get:(_t,f)=>new Proxy({}, {get:(_o,m)=>async(...args)=>{
if(m==='ListProjects')return [{id:'p',name:'网络测试项目',workspacePath:'D:/Project',workspaceKind:'external'}];
if(m==='ListModelProfiles')return [{id:'model1',name:'研究主模型',baseUrl:'https://example.org/v1',apiProtocol:'openai_responses',models:[{id:'research-model',contextWindowTokens:128000,autoCompactTokenLimit:100000,contextWindowSource:'manual',reasoningLevels:['high'],enabled:true,isDefault:true}],secretConfigured:true,customHeaders:{},enabled:true,isDefault:true,modelId:'research-model',timeoutSeconds:60}];
if(m==='ListMCPServers')return [{id:'mcp1',name:'文献工具',namespace:'literature',transport:'stdio',status:'stopped',enabled:true,trust:'user_trusted',args:[],env:{},headers:{},secretNames:[],command:'node',toolCount:3,resourceCount:0,promptCount:0}];
if(m==='ListConversations'||m==='ListResearchTasks')return [];
if(m==='ListSearchChannels')return [{provider:'baidu',enabled:true,priority:1,configured:true},{provider:'firecrawl',enabled:false,priority:2,configured:false},{provider:'duckduckgo',enabled:true,priority:99,configured:true}];
if(m==='GetNetworkConfig')return window.savedNetwork||{global:{mode:'direct',url:''},modules:{},revision:0};
if(m==='SaveNetworkConfig'){window.savedNetwork=args[0];return;}
if(m==='ListSkills')return {skills:[{name:'anndata',description:'Annotated data for single-cell analysis. 用于研究数据整理、验证及分析。',category:'biology',tags:['biology'],routingAliases:[],origin:'default',entry:true,enabled:true,allowedTools:[],contentHash:'abc',packageHash:'abc',fileCount:6,referenceCount:5,assetCount:0,scriptCount:0,instructionRunes:1000,overridden:[],capability:'requires_dependency',capabilityReason:'需要项目 Python 依赖',requiredTools:[],missingTools:[],pythonPackages:['anndata'],cliDependencies:[],externalServices:[],capabilityLimitations:[]}],categories:[{name:'biology',count:1}],diagnostics:[],capabilities:{requires_dependency:1},defaultCount:1,enabledCount:1};
if(m==='GetProjectEnvironment')return {state:'ready',environmentKind:'workspace_managed',lock:[],environmentPythonPath:'D:/Project/.sciaide/python/venv/Scripts/python.exe'};
if(m==='DetectInterpreters')return {interpreters:[],status:'ready'};
return {};
}})})};window.runtime={EventsOn:()=>()=>{},EventsOnMultiple:()=>()=>{},OnFileDrop:()=>{},OnFileDropOff:()=>{}};`;
const bundle=await build({stdin:{contents:app+`\nimport {createRoot} from 'react-dom/client';createRoot(document.getElementById('root')).render(<App/>);`,resolveDir:path.resolve('frontend/src'),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
const browser=await chromium.launch({channel:'msedge',headless:true});try{const page=await browser.newPage({viewport:{width:1280,height:900}});const errors=[];page.on('pageerror',e=>{errors.push(e.message);console.error(e.message)});await page.setContent('<div id="root"></div>');await page.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8')+fs.readFileSync('frontend/src/applicationSettings.css','utf8')+fs.readFileSync('frontend/src/modal.css','utf8')});await page.addScriptTag({content:mock});await page.addScriptTag({content:bundle.outputFiles[0].text});await page.locator('.sidebar-footer').getByRole('button',{name:/设置/}).click();
const nav=page.locator('.application-settings-nav');
const assertStableTransition=async()=>{
 const result=await page.evaluate(()=>{
  const shell=document.querySelector('.application-settings-content');
  const panel=document.querySelector('.settings-page');
  const animations=panel.getAnimations({subtree:true}).filter(a=>a.animationName==='settings-page-in');
  const samples=[];
  for(const time of [0,40,100,200,250]){
   for(const animation of animations){animation.pause();animation.currentTime=time}
   const rect=panel.getBoundingClientRect();
   samples.push({x:rect.x,width:rect.width,height:rect.height,overflow:shell.scrollHeight-shell.clientHeight,transform:getComputedStyle(panel).transform});
  }
  for(const animation of animations)animation.finish();
  return {samples,outerOverflow:getComputedStyle(shell).overflowY};
 });
 assert.equal(result.outerOverflow,'hidden');
 for(const sample of result.samples){
  assert.equal(sample.transform,'none','whole page must not move during transitions');
  assert.ok(sample.overflow<=1,`transient outer overflow: ${JSON.stringify(sample)}`);
  assert.ok(Math.abs(sample.x-result.samples[0].x)<.1);
  assert.ok(Math.abs(sample.width-result.samples[0].width)<.1);
 }
};
const shot=async name=>{await assertStableTransition();await page.waitForTimeout(250);await page.screenshot({path:`build/qa/settings-integrated-${name}.png`});assert.equal(await page.locator('.application-settings-content').evaluate(e=>e.scrollWidth>e.clientWidth+1),false)};
for(const width of [1280,960]) {
 await page.setViewportSize({width,height:width===1280?900:760});
 await nav.getByRole('button',{name:'模型与 API',exact:true}).click();
 assert.equal(await page.locator('.application-settings [role="dialog"]').count(),0);
 await page.getByRole('button',{name:/研究主模型/}).waitFor();await shot(`models-${width}`);
 await page.getByRole('button',{name:/研究主模型/}).click();
 await page.locator('.settings-collection.is-editing').waitFor();
 assert.equal(await page.locator('.settings-collection>aside').isVisible(),false);
 await shot(`model-editor-${width}`);
 await page.getByRole('button',{name:'返回配置列表'}).click();
 await page.getByRole('button',{name:/添加 API 配置/}).click();await page.getByPlaceholder('例如：实验室模型服务').fill('未保存草稿');
 await page.getByRole('button',{name:'返回配置列表'}).click();await page.getByRole('button',{name:/添加 API 配置/}).click();assert.equal(await page.getByPlaceholder('例如：实验室模型服务').inputValue(),'未保存草稿');
 await nav.getByRole('button',{name:'MCP 服务',exact:true}).click();await page.getByRole('button',{name:'放弃修改',exact:true}).click();await page.getByRole('button',{name:/文献工具.*stdio/}).waitFor();await shot(`mcp-${width}`);
 await page.getByRole('button',{name:/添加 MCP Server/}).click();await shot(`mcp-editor-${width}`);assert.equal(await page.locator('.settings-collection>aside').isVisible(),false);
 await page.getByRole('button',{name:'返回配置列表'}).click();await page.getByRole('button',{name:/从 JSON 导入/}).click();await page.getByRole('heading',{name:'导入 MCP 配置'}).waitFor();
 await nav.getByRole('button',{name:'Skills',exact:true}).click();await page.getByRole('heading',{name:'anndata',exact:true}).waitFor();await shot(`skills-${width}`);
 await page.getByPlaceholder('搜索名称、描述或标签').fill('notfound');assert.equal(await page.locator('.skill-side-list .skill-profile').count(),0);
 await nav.getByRole('button',{name:'联网搜索',exact:true}).click();await page.getByRole('button',{name:'配置 Key'}).first().waitFor();await shot(`search-${width}`);await page.getByRole('button',{name:'配置 Key'}).first().click();await page.getByRole('dialog',{name:/百度搜索/}).waitFor();await page.keyboard.press('Escape');
 // Close the key dialog explicitly if Escape is not handled by its existing component.
 const keyDialog=page.getByRole('dialog',{name:/百度搜索/});if(await keyDialog.count())await keyDialog.getByRole('button',{name:/关闭/}).click();
 await nav.getByRole('button',{name:'网络与代理',exact:true}).click();await shot(`network-${width}`);
 const scroll=await page.locator('.network-settings-page .settings-page-body').evaluate(e=>{const before=e.scrollTop;e.scrollTop=e.scrollHeight;const moved=e.scrollTop>before;const fits=e.scrollHeight<=e.clientHeight;e.scrollTop=0;return {moved,fits}});assert.ok(scroll.moved||scroll.fits,'inner pane must remain scrollable');

}
await nav.getByRole('button',{name:'联网搜索',exact:true}).click();await nav.getByRole('button',{name:'关闭设置'}).click();assert.equal(await page.locator('.application-settings').count(),0);
await page.emulateMedia({reducedMotion:'reduce'});await page.locator('.sidebar-footer').getByRole('button',{name:/设置/}).click();await nav.getByRole('button',{name:'网络与代理',exact:true}).click();assert.equal(await page.locator('.settings-page').evaluate(e=>getComputedStyle(e).animationName),'none');// Content clicks and dragging from content to the mask must never dismiss.
await page.locator('.settings-page-header h2').click();assert.equal(await page.locator('.application-settings').count(),1);
const heading=await page.locator('.settings-page-header h2').boundingBox();
await page.mouse.move(heading.x+10,heading.y+10);await page.mouse.down();await page.mouse.move(5,5);await page.mouse.up();assert.equal(await page.locator('.application-settings').count(),1);
await page.mouse.click(5,5);assert.equal(await page.locator('.application-settings').count(),0);
await page.locator('.sidebar-footer').getByRole('button',{name:/设置/}).click();
await nav.getByRole('button',{name:'联网搜索',exact:true}).click();await page.getByRole('button',{name:'配置 Key'}).first().click();
await page.keyboard.press('Escape');assert.equal(await page.locator('.application-settings').count(),1);assert.equal(await page.getByRole('dialog',{name:/百度搜索/}).count(),0);
await page.keyboard.press('Escape');assert.equal(await page.locator('.application-settings').count(),0);

// Actual settings drafts: native Key, network save, and create form.
await page.locator('.sidebar-footer').getByRole('button',{name:/设置/}).click();
await nav.getByRole('button',{name:'联网搜索',exact:true}).click();await page.getByRole('button',{name:'配置 Key'}).first().click();
await page.getByLabel('API Key',{exact:true}).fill('test-not-a-secret');await page.keyboard.press('Escape');
await page.getByRole('dialog',{name:'放弃未保存的修改'}).waitFor();await page.keyboard.press('Escape');
assert.equal(await page.getByLabel('API Key',{exact:true}).inputValue(),'test-not-a-secret');
await page.keyboard.press('Escape');await page.getByRole('button',{name:'放弃修改',exact:true}).click();
await nav.getByRole('button',{name:'网络与代理',exact:true}).click();await page.getByLabel('全局网络模式').selectOption('custom');
await page.getByLabel('全局网络地址').fill('http://127.0.0.1:7890');await page.keyboard.press('Escape');
await page.getByRole('button',{name:'继续编辑',exact:true}).click();await page.getByRole('button',{name:'保存设置',exact:true}).click();
await page.getByText('已保存，新请求生效',{exact:true}).waitFor();await page.keyboard.press('Escape');assert.equal(await page.locator('.application-settings').count(),0);
await page.getByRole('button',{name:'新建科研项目',exact:true}).click();await page.getByPlaceholder('例如：单细胞转录组研究').fill('Draft');await page.keyboard.press('Escape');await page.getByRole('button',{name:'放弃修改',exact:true}).click();assert.equal(await page.locator('.create-modal').count(),0);
assert.deepEqual(errors,[]);console.log('PASS integrated settings lists/editors, draft return, Skills filter, search key dialog, close, scroll, stable animation bounds and 1280/960 layout');}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});
