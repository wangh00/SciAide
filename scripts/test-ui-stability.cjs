// Actual React timeline/boundary in an isolated browser, no database or model.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const output = path.resolve(process.env.SCIAIDE_STABILITY_OUTPUT || '.tmp/ui-stability');
const harness = `
import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
import {ResearchTaskTimeline} from './ResearchTaskTimeline';import {UIErrorBoundary} from './UIErrorBoundary';
const all=Array.from({length:400},(_,i)=>({id:'message:'+i,sequence:String(i+1),kind:'message',runId:'r',conversationId:'c',snapshot:{},createdAt:'2026-10-13T00:00:00Z',active:i===385,message:{id:String(i),role:'assistant',status:i===399?'streaming':'complete',parts:[{text:('科研原文 '+i+' ').repeat(120)}]}}));
window.fixtureRows=all;window.follow=false;window.acknowledged=[];window.readCount=0;
const read=async q=>{window.readCount++;let rows=all.filter(e=>!q.before||BigInt(e.sequence)<BigInt(q.before));if(q.after)rows=rows.filter(e=>BigInt(e.sequence)>BigInt(q.after));const n=q.limit||50;return{taskId:'task',latestRunId:'r',conversationId:'c',entries:q.after?rows.slice(0,n):rows.slice(-n),hasMore:rows.length>n}};
const follow=v=>window.follow=v;const acknowledge=v=>window.acknowledged.push(...v);
function Bug(){throw new TypeError('fixture render failure')}
function Harness(){const[open,setOpen]=useState(true),[fail,setFail]=useState(false),[pending,setPending]=useState([]);window.append=()=>{const v={...all.at(-1),id:'message:human',sequence:String(all.length+1),message:{id:'human',role:'user',status:'complete',parts:[{text:'我的回答'}]}};setPending([v]);all.push(v);window.dispatchEvent(new CustomEvent('research-message-saved',{detail:{projectId:'p',taskId:'task'}}))};return <><button onClick={()=>setOpen(v=>!v)}>switch task</button><button onClick={()=>setFail(v=>!v)}>toggle failure</button><div className='chat' style={{height:600,overflow:'auto',scrollBehavior:'auto'}}><UIErrorBoundary scope='conversation' resetKey={String(fail)}>{fail?<Bug/>:open?<ResearchTaskTimeline projectId='p' taskId='task' read={read} follow={follow} pending={pending} acknowledge={acknowledge} trailing={<div className='user-input-card'>必要疑问仍在瀑布流</div>} render={e=><article style={{padding:16,minHeight:180}}><h3>{e.id}</h3><p>{e.message?.parts[0].text}</p><details><summary>历史详情 {e.id}</summary><pre>完整详情</pre></details><input aria-label={'draft '+e.id}/></article>}/>:null}</UIErrorBoundary></div></>}
createRoot(document.getElementById('root')).render(<React.StrictMode><Harness/></React.StrictMode>);
`;
(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const bundle=await build({stdin:{contents:harness,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',logLevel:'silent'});
  const browser=await chromium.launch({channel:'msedge',headless:true});
  try {
    const page=await browser.newPage({viewport:{width:1280,height:900}});
    await page.route('**/*',route=>route.abort());await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:'.chat{overflow-anchor:none}.message-stack{display:flex;flex-direction:column}.research-task-timeline > [data-timeline-id]{display:flex;flex-direction:column;min-width:0}.research-task-timeline > [data-timeline-id]:not(:empty){margin-bottom:18px}'});
    await page.evaluate(()=>{window.reports=[];window.go={wails:{SystemFacade:{ReportUIError:async(...args)=>window.reports.push(args)}}}});
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    const scroll=async y=>{await page.locator('.chat').evaluate((e,y)=>e.scrollTop=y,y);await page.waitForTimeout(200)};
    await page.waitForFunction(()=>document.querySelectorAll('[data-timeline-id]').length===50);
    await scroll(0);const old=await page.locator('[data-timeline-id]').first().evaluate(e=>({id:e.dataset.timelineId,top:e.getBoundingClientRect().top}));
    await page.getByRole('button',{name:'加载更早记录'}).click();
    await page.waitForFunction(()=>document.querySelectorAll('[data-timeline-id]').length===100);
    await page.waitForTimeout(200);
    const moved=await page.locator('[data-timeline-id="'+old.id+'"]').evaluate(e=>e.getBoundingClientRect().top);
    assert.ok(Math.abs(moved-old.top)<3,'prepend lost reading anchor '+(moved-old.top));
    for(let n=150;n<=400;n+=50){await scroll(0);await page.getByRole('button',{name:'加载更早记录'}).click();await page.waitForFunction(n=>document.querySelectorAll('[data-timeline-id]').length===n,n)}
    await scroll(0);
    assert.equal(await page.locator('[data-timeline-id="message:0"] article').count(),1);
    const firstBox=await page.locator('[data-timeline-id="message:0"]').evaluate(e=>({height:e.getBoundingClientRect().height,margin:getComputedStyle(e).marginBottom}));
    assert.equal(await page.locator('[data-timeline-id="message:399"] article').count(),1,'streaming row unmounted');
    assert.equal(await page.locator('[data-timeline-id="message:385"] article').count(),1,'active action unmounted');
    const count=await page.locator('[data-timeline-mounted="true"]').count();assert.ok(count<55,'offscreen bodies retained '+count);
    // Expanded details and input drafts remain mounted while the reader scrolls.
    const summary=page.locator('[data-timeline-id="message:0"] summary');await summary.click();
    const draft=page.getByRole('textbox',{name:'draft message:0',exact:true});await draft.fill('未提交草稿');
    await scroll(1e9);assert.equal(await draft.inputValue(),'未提交草稿');assert.equal(await page.locator('[data-timeline-id="message:0"] details[open]').count(),1);
    await page.locator('[data-timeline-id="message:399"] input').focus();
    await page.locator('[data-timeline-id="message:0"] details').evaluate(e=>e.open=false);
    await page.waitForTimeout(150);assert.equal(await page.locator('[data-timeline-id="message:0"] article').count(),0);
    const placeholderBox=await page.locator('[data-timeline-id="message:0"]').evaluate(e=>({height:e.getBoundingClientRect().height,margin:getComputedStyle(e).marginBottom}));
    assert.ok(Math.abs(firstBox.height-placeholderBox.height)<1,'placeholder lost content height');
    assert.equal(firstBox.margin,'18px');assert.equal(placeholderBox.margin,firstBox.margin,'placeholder lost outer row spacing');
    assert.equal(await page.getByText('必要疑问仍在瀑布流').count(),1);
    await page.evaluate(()=>window.append());await page.waitForFunction(()=>window.acknowledged.includes('human'));assert.equal(await page.locator('[data-timeline-id="message:human"]').count(),1,'local send duplicated');
    for(let i=0;i<12;i++){await page.getByRole('button',{name:'switch task',exact:true}).click();assert.equal(await page.locator('[data-timeline-id]').count(),0);await page.getByRole('button',{name:'switch task',exact:true}).click();await page.waitForFunction(()=>document.querySelectorAll('[data-timeline-id]').length===50)}
    assert.deepEqual(errors,[]);
    await page.getByRole('button',{name:'toggle failure'}).click();await page.getByRole('alert').waitFor();assert.equal(await page.getByText('这不代表科研任务失败。已保存的记录仍保留，重新显示不会重跑任务。').count(),1);
    assert.ok(await page.evaluate(()=>window.reports.some(r=>r[0]==='conversation'&&r[1]==='TypeError')));
    await page.getByRole('button',{name:'toggle failure'}).click();await page.waitForFunction(()=>document.querySelectorAll('[data-timeline-id]').length===50);
    const result={source:'isolated React/Edge fixture, no native WebView, database or model',maxOffscreenMountedAtTop:count,loaded:400,anchorDelta:moved-old.top,firstBox,placeholderBox,checked:['prepend anchor','history still readable','equal placeholder height and margin','streaming and active rows retained','expanded details and focused drafts retained','pending send acknowledged','12 mount/unmount cycles','boundary catches render error and read-only recovery']};
    fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(result,null,2));console.log('PASS',JSON.stringify(result));
  } finally {await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
