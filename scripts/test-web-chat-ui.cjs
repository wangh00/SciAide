const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
(async()=>{
const app=fs.readFileSync('frontend/src/App.tsx','utf8').replace('export default function App','function App');
const markdown='示例代码：\n\n```python\nfrom curl_cffi import requests\nr = requests.get("https://example.com", timeout=20)\nprint(r.status_code)\n```\n\n行内 `timeout=20`。';
const harness=`
import {createRoot} from 'react-dom/client';
function TestPage(){
 const [approval,setApproval]=useState({id:'approval-1',runId:'run'});
 const [status,setStatus]=useState('awaiting_approval');
 const [source,setSource]=useState('chat');
 window.resetApproval=()=>{setApproval({id:'approval-'+Date.now(),runId:'run'});setStatus('awaiting_approval');setSource('workflow')};
 return <div style={{padding:28,maxWidth:850,margin:'auto'}}>
 <MarkdownAnswer text={${JSON.stringify(markdown)}} citations={new Map()} selectReference={()=>{}}/>
 <UnifiedToolActivityCard activity={{id:'tool',toolName:'builtin.web.search',status,risk:'moderate',source,arguments:{query:'curl_cffi'},approval,resolveApproval:async(_a,allow)=>{setApproval(undefined);setStatus(allow?'completed':'denied')}}}/>
 </div>
}
createRoot(document.getElementById('root')).render(<TestPage/>);`;
const bundle=await build({stdin:{contents:app+harness,resolveDir:path.resolve('frontend/src'),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
const browser=await chromium.launch({channel:'msedge',headless:true});
try{
const page=await browser.newPage({viewport:{width:1000,height:800}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
await page.setContent('<div id="root"></div>');
await page.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8')});
await page.addScriptTag({content:"window.runtime={ClipboardSetText:async(text)=>{window.copiedText=text;return true}};"});
await page.addScriptTag({content:bundle.outputFiles[0].text});
assert.equal(await page.getByRole('button',{name:'复制代码'}).count(),1);
await page.getByRole('button',{name:'复制代码'}).click();await page.getByText('已复制',{exact:true}).waitFor();
assert.equal(await page.evaluate(()=>window.copiedText),'from curl_cffi import requests\nr = requests.get("https://example.com", timeout=20)\nprint(r.status_code)\n');
assert.equal(await page.locator('.tool-card-toggle').getAttribute('aria-expanded'),'true');
assert.equal(await page.locator('.tool-card .risk').count(),0);
await page.getByRole('button',{name:'接受',exact:true}).click();
await page.waitForFunction(()=>document.querySelector('.tool-card-toggle').getAttribute('aria-expanded')==='false');
await page.locator('.tool-card-toggle').click();assert.equal(await page.locator('.tool-card-toggle').getAttribute('aria-expanded'),'true');
await page.evaluate(()=>window.resetApproval());await page.getByRole('button',{name:'拒绝',exact:true}).click();
await page.waitForFunction(()=>document.querySelector('.tool-card-toggle').getAttribute('aria-expanded')==='false');
await page.screenshot({path:'build/qa/web-chat-tools.png'});
assert.deepEqual(errors,[]);console.log('PASS exact code copy; inline code unchanged; no risk badge; chat accept and workflow deny collapse; manual reopen');
}finally{await browser.close()}
})().catch(e=>{console.error(e);process.exit(1)});
