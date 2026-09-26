const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {build} = createRequire(path.resolve('frontend/package.json'))('esbuild');
const {chromium} = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
(async () => {
  const bundle = await build({stdin: {contents: `
    import {createRoot} from 'react-dom/client';
    import {BrowserEnvironment} from './ApplicationSettings';
    let finish; window.calls=[];
    async function service(f,m,id) {
      window.calls.push(m);
      if(m==='GetBrowserEnvironment') return {ready:false,message:'尚未配置浏览器环境',root:'D:/Project/.sciaide/browser'};
      if(m==='InstallBrowserEnvironment') return new Promise((resolve,reject)=>{finish=reject});
      if(m==='CancelBrowserOperation') finish(new Error('已取消安装'));
    }
    createRoot(document.getElementById('root')).render(<BrowserEnvironment service={service} projectId="p" enabled={true} onChanged={()=>{window.changed=true}}/>);
  `, resolveDir: path.resolve('frontend/src'), loader: 'tsx'}, bundle:true, write:false, format:'iife', jsx:'automatic', loader:{'.css':'text'}, logLevel:'silent'});
  const browser = await chromium.launch({channel:'msedge', headless:true});
  try {
    const page = await browser.newPage({viewport:{width:960,height:760}});
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.setContent('<div id="root"></div>');
    await page.addStyleTag({content:fs.readFileSync('frontend/src/applicationSettings.css','utf8')});
    await page.addScriptTag({content:bundle.outputFiles[0].text});
    await page.getByRole('button',{name:/配置浏览器环境/}).click();
    await page.getByText('尚未配置浏览器环境',{exact:true}).waitFor();
    await page.getByRole('button',{name:'安装浏览器环境',exact:true}).click();
    assert.equal(await page.getByRole('button',{name:'配置中…'}).isDisabled(),true);
    await page.getByRole('button',{name:'取消安装',exact:true}).click();
    await page.getByText('Error: 已取消安装',{exact:true}).waitFor();
    assert.equal(await page.getByRole('button',{name:'安装浏览器环境',exact:true}).isEnabled(),true);
    assert.equal(await page.evaluate(()=>window.changed),true);
    assert.deepEqual(await page.evaluate(()=>window.calls),['GetBrowserEnvironment','InstallBrowserEnvironment','CancelBrowserOperation']);
    assert.deepEqual(errors,[]);
    console.log('PASS browser setup status, install, disabled state, cancellation and refresh');
  } finally {await browser.close()}
})().catch(e=>{console.error(e);process.exit(1)});
