const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
(async () => {
  const source = `import React,{useState} from 'react'; import {createRoot} from 'react-dom/client'; import {ReferenceMaterials} from './ReferenceMaterials';
  const a={id:'a',originalName:'Old material.pdf',status:'ready',format:'pdf',scopeKind:'project_shared',sizeBytes:4000};
  const b={...a,id:'b',originalName:'Uploaded material.pdf'}; let calls=0;window.archived=false;
  const service=async(f,m)=>m==='ListReferenceMaterials'?(++calls===1?new Promise(resolve=>{window.resolveOld=()=>resolve([a])}):window.archived?[]:[a,b]):{attachments:[b],errors:[]};
  function Test(){const [ids,set]=useState([]);return <main><ReferenceMaterials projectId='p' selected={ids} onChange={set} service={service}/><output>{ids.join(',')}</output></main>};createRoot(document.getElementById('root')).render(<Test/>);`;
  const result=await build({stdin:{contents:source,loader:'tsx',resolveDir:path.resolve('frontend/src')},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
  const browser=await chromium.launch({channel:'msedge',headless:true});
  try {
    const page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:fs.readFileSync('frontend/src/referenceMaterials.css','utf8')});
    await page.addScriptTag({content:result.outputFiles[0].text});
    await page.getByRole('button',{name:'添加本地文献',exact:true}).click();
    await page.waitForFunction(()=>document.querySelector('output').textContent==='b');
    await page.getByRole('button',{name:'从资料库选择',exact:true}).click();
    assert.equal(await page.getByRole('checkbox').count(),2);
    assert.equal(await page.getByRole('checkbox').nth(1).isChecked(),true);
    await page.getByRole('button',{name:'完成',exact:true}).click();
    await page.evaluate(()=>window.archived=true);
    await page.getByRole('button',{name:'从资料库选择',exact:true}).click();
    await page.waitForFunction(()=>document.querySelector('output').textContent==='');
    await page.evaluate(()=>window.resolveOld());
    assert.equal(await page.getByRole('checkbox').count(),0);
    assert.deepEqual(errors,[]);
    console.log('PASS: upload selection, fresh library refresh, archive deselection, stale response rejection');
  }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exit(1)});
