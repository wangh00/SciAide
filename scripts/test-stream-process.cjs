const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
(async()=>{
const app=fs.readFileSync('frontend/src/App.tsx','utf8').replace('export default function App','function App');
const harness=`
import {createRoot} from 'react-dom/client';
function Harness(){
const [text,setText]=useState(''),[done,setDone]=useState(false),[stage,setStage]=useState(0);
const [cancelled,setCancelled]=useState(false);
const [fullAccess,setFullAccess]=useState(false),[pending,setPending]=useState(false);
const ref=useRef(null),follow=useRef(true);
const time='2026-09-24T15:00:00Z';
const calls=[0,1,2].map(i=>({id:'t'+i,toolName:'builtin.workspace.list',arguments:{path:'.'},createdAt:time,status:stage>=2?'completed':i<stage?'completed':i===stage?(fullAccess?(pending?'pending':'running'):'awaiting_approval'):'pending'}));
const approvals=!fullAccess&&stage<2?[{id:'a'+stage,toolCallId:'t'+stage,runId:'run'}]:[];
const run={id:'run',createdAt:time,status:cancelled?'cancelled':done?'completed':approvals.length?'waiting_approval':'running',completedAt:done?time:undefined};
window.streamText=t=>setText(t);window.finish=()=>setDone(true);window.cancelSnapshot=()=>{setCancelled(true);setDone(true);setText('')};
window.fullAccess=(p=false)=>{setFullAccess(true);setPending(p);setStage(0)};window.completeTools=()=>setStage(2);
useEffect(()=>observeChatAutoFollow(ref.current,()=>follow.current),[]);
useLayoutEffect(()=>{if(follow.current)ref.current.scrollTop=ref.current.scrollHeight},[text,stage]);
return <section className="chat" ref={ref} style={{height:600,overflow:'auto',display:'block',padding:20}} onScroll={e=>{let c=e.currentTarget;follow.current=c.scrollHeight-c.scrollTop-c.clientHeight<60}}>
<MessageRow message={{id:'m',runId:'run',role:'assistant',status:cancelled?'incomplete':done?'complete':'streaming',parts:cancelled?[{type:'text',text:''},{type:'tool_result',payload:{kind:'run_termination_context',activities:[{commentary:'只查 MCP 配置。',reasoningSummary:'PRIVATE REASONING'},{commentary:'接着只查 MCP，不碰 Skill。'}]}}]:[{type:'text',text}]}} providerName="Test" run={run} runActive={!done} retryStatus={null} runSteps={[]} toolCalls={calls} approvals={approvals} revealing={!done} resolvingApprovalId="" resolveApproval={async()=>setStage(v=>v+1)} saveArtifact={async()=>{}}/>
</section>;
}
createRoot(document.getElementById('root')).render(<Harness/>);`;
const bundle=await build({stdin:{contents:app+harness,resolveDir:path.resolve('frontend/src'),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
const browser=await chromium.launch({channel:'msedge',headless:true});
try{
const page=await browser.newPage({viewport:{width:1100,height:760}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
await page.setContent('<div id="root"></div>');await page.addStyleTag({content:fs.readFileSync('frontend/src/styles.css','utf8')});await page.addScriptTag({content:bundle.outputFiles[0].text});
assert.equal(await page.locator('.tool-card').count(),1);await page.getByText('另有 2 项排队，尚未执行').waitFor();
assert.equal(await page.getByText('查看过程',{exact:true}).count(),0);
await page.locator('.run-process-toggle').click();
assert.equal(await page.locator('.current-tool-activity .tool-card').count(),1);
assert.equal(await page.locator('.tool-card').count(),3);
await page.locator('.run-process-toggle').click();
await page.getByRole('button',{name:'接受',exact:true}).click();assert.equal(await page.locator('.tool-card').count(),1);await page.getByText('另有 1 项排队，尚未执行').waitFor();
await page.getByRole('button',{name:'接受',exact:true}).click();
const text=Array.from({length:30},(_,i)=>'第 '+i+' 段研究解释。这里是用户需要看到的回答正文。').join('\n\n');
await page.evaluate(t=>window.streamText(t),text);await page.waitForTimeout(150);
assert.equal(await page.locator('.tool-card').count(),0);
assert.ok(await page.locator('.compact-process').evaluate(e=>Boolean(e.compareDocumentPosition(document.querySelector('.bubble')) & Node.DOCUMENT_POSITION_FOLLOWING)));
assert.ok(await page.locator('.chat').evaluate(e=>e.scrollHeight-e.scrollTop-e.clientHeight<10));
await page.locator('.run-process-toggle').click();assert.equal(await page.locator('.tool-card').count(),3);
await page.evaluate(t=>window.streamText(t+'\n\n新段落'),text);assert.equal(await page.locator('.run-process-toggle').getAttribute('aria-expanded'),'true');
await page.locator('.run-process-toggle').click();
await page.evaluate(()=>window.fullAccess());
await page.locator('.current-tool-activity .tool-card.running').waitFor();
assert.equal(await page.getByRole('button',{name:'接受',exact:true}).count(),0);
await page.locator('.run-process-toggle').click();
assert.equal(await page.locator('.current-tool-activity .tool-card.running').count(),1);
assert.equal(await page.locator('.tool-card.running').count(),1);
await page.locator('.run-process-toggle').click();
await page.evaluate(()=>window.fullAccess(true));
await page.locator('.current-tool-activity .tool-card.pending').waitFor();
await page.evaluate(()=>window.completeTools());
await page.waitForTimeout(150);
assert.equal(await page.locator('.tool-card').count(),0);
await page.locator('.chat').evaluate(e=>{e.scrollTop=100;e.dispatchEvent(new Event('scroll'))});
await page.evaluate(t=>window.streamText(t+'\n\n更多内容'.repeat(30)),text);await page.waitForTimeout(150);
assert.ok(await page.locator('.chat').evaluate(e=>Math.abs(e.scrollTop-100)<5));
await page.locator('.chat').evaluate(e=>{e.scrollTop=e.scrollHeight;e.dispatchEvent(new Event('scroll'))});
await page.locator('.run-process-toggle').click();
await page.evaluate(()=>window.finish());await page.waitForTimeout(150);
assert.equal(await page.locator('.run-process-toggle').getAttribute('aria-expanded'),'true');
assert.ok((await page.locator('.bubble').innerText()).includes('更多内容'));
await page.locator('.run-process-toggle').click();await page.locator('.chat').evaluate(e=>e.scrollTop=e.scrollHeight);
await page.screenshot({path:'build/qa/stream-process.png'});
await page.evaluate(()=>window.cancelSnapshot());
await page.getByText('生成已中断',{exact:true}).waitFor();
assert.equal(await page.locator('.bubble p').count(),2);
assert.ok((await page.locator('.bubble').innerText()).includes('接着只查 MCP'));
assert.ok(!(await page.locator('.bubble').innerText()).includes('PRIVATE REASONING'));
assert.equal(await page.getByRole('button',{name:'保存为科研产物',exact:true}).count(),0);
assert.deepEqual(errors,[]);console.log('PASS one approval at a time, queue count, process-before-answer, auto-follow, manual scroll, explicit history preserved through streaming and completion');
}finally{await browser.close()}
})().catch(e=>{console.error(e);process.exit(1)});
