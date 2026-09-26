const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');const {createRequire}=require('node:module');const {build}=createRequire(path.resolve('frontend/package.json'))('esbuild');const {chromium}=require(process.env.SCIAIDE_PLAYWRIGHT_PATH||'playwright');
(async()=>{let app=fs.readFileSync('frontend/src/App.tsx','utf8').replace('export default function App','function App');const markdown=String.raw`## 第一阶段
用工具变量 $Z$ 对内生变量 $X$ 进行回归： $$X = \pi_0 + \pi_1 Z + v$$ 得到 $X$ 的拟合值 $\hat{X}$。

## 第二阶段
用拟合值 $\hat{X}$ 代替原始 $X$，对 $Y$ 进行回归：

$$
Y = \beta_0 + \beta_1 \hat{X} + \varepsilon
$$

| 条件 | 含义 |
| --- | --- |
| 相关性 | $Cov(Z,X) \neq 0$ |
| 排他性 | $Cov(Z,\varepsilon)=0$ |

依据 [K-0123456789AB]。代码示例：` + '`$X$`';
const result=await build({stdin:{contents:app+`\nimport {createRoot} from 'react-dom/client';createRoot(document.getElementById('root')).render(<div style={{padding:28,maxWidth:850,margin:'auto',fontSize:15}}><MarkdownAnswer text={${JSON.stringify(markdown)}} citations={new Map()} selectReference={()=>{}}/></div>);`,resolveDir:path.resolve('frontend/src'),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',loader:{'.css':'text'},logLevel:'silent'});
let css=fs.readFileSync('frontend/node_modules/katex/dist/katex.min.css','utf8');css=css.replace(/url\(([^)]+)\)/g,(_,p)=>{p=p.replace(/["']/g,'');const f=path.resolve('frontend/node_modules/katex/dist',p);return `url(data:font/woff2;base64,${fs.readFileSync(f).toString('base64')})`});
const browser=await chromium.launch({channel:'msedge',headless:true});try{const page=await browser.newPage({viewport:{width:1000,height:800}});const errors=[];page.on('pageerror',e=>errors.push(e.message));await page.setContent('<div id="root"></div>');await page.addStyleTag({content:css+fs.readFileSync('frontend/src/styles.css','utf8')});await page.addScriptTag({content:result.outputFiles[0].text});await page.evaluate(()=>document.fonts.ready);assert.ok(await page.locator('.katex').count()>=10);assert.equal(await page.locator('.katex-error').count(),0);assert.equal(await page.locator('table').count(),1);assert.ok(await page.locator('.citation-unverified').count());await page.screenshot({path:'build/qa/composer-math.png'});assert.deepEqual(errors,[]);console.log('PASS actual MarkdownAnswer with KaTeX fonts, inline/display math, GFM table and citation markers');}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});
