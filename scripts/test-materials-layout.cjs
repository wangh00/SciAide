const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { createRequire } = require("node:module");
const { build } = createRequire(path.resolve("frontend/package.json"))("esbuild");
const { chromium } = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || "playwright");

fs.mkdirSync("build/qa", {recursive:true});
const baseProject = { id: "project-materials", name: "资料库界面检查" };
const now = new Date().toISOString();
const materials = Array.from({length:Number(process.argv[2]||100)},(_,i)=>({
  id:`material-${i}`, projectId:baseProject.id, scopeKind:"project_shared", sourceKind:i%2 ? "user_import" : "research_import",
  originalName:`research-reference-${i}-long-name-for-card-overflow-check.pdf`, format:i%3 ? "pdf" : "txt", sizeBytes:8192+i*101000,
  sha256:`hash-${i}`, status:"ready", createdAt:now, title:`研究资料 ${i}`, notes:i%9 ? "" : "滚动与长文本布局检查", archived:false,reusable:true,
  contentKind:i%4===0 ? "metadata_abstract" : i%4===1 ? "full_text" : "user_file", indexStatus:i%7===0 ? "failed" : "ready",
}));
(async () => {
  const source = fs.readFileSync("frontend/src/ResearchMaterialsLibrary.tsx", "utf8");
  const bundle = await build({ stdin: { contents: source + `
    import { createRoot } from "react-dom/client";
    const service=async (_facade,method)=>method==="ListLibraryMaterials"?${JSON.stringify(materials)}:method==="ListResearchTasks"?[]:method==="GetEmbeddingConfig"?({enabled:false,baseUrl:"",modelId:"",timeoutSeconds:30,dimensions:0,secretConfigured:false}):({text:"test",nextOffset:-1,totalRunes:4});
    createRoot(document.getElementById("root")).render(<ResearchMaterialsLibrary project={${JSON.stringify(baseProject)}} close={() => {}} service={service}/>);
  `, resolveDir: path.resolve("frontend/src"), loader: "tsx" }, loader: { ".css": "text" }, bundle: true, write: false, format: "iife", jsx: "automatic", logLevel: "silent" });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.setContent("<div id=\"root\"></div>");
  await page.addStyleTag({ content: fs.readFileSync("frontend/src/styles.css", "utf8")+"\n"+fs.readFileSync("frontend/src/researchMaterialsLibrary.css", "utf8") });
  await page.addScriptTag({ content: bundle.outputFiles[0].text });
  await page.locator(".research-materials-modal").waitFor();
  await page.screenshot({ path: "build/qa/research-material-many-1280.png", fullPage: true });
  const rows=page.locator(".research-materials-row");
  assert.equal(await rows.count(),materials.length);
  const geometry=await page.locator(".research-materials-list").evaluate(el=>({height:el.clientHeight,scroll:el.scrollHeight,widths:[...el.children].map(v=>v.getBoundingClientRect().width)}));
  assert.ok(geometry.scroll>geometry.height,"list must scroll");
  assert.ok(geometry.widths.every(width=>width>0&&width<1000),"rows must remain constrained");
  await page.setViewportSize({ width: 960, height: 900 });
  await page.screenshot({ path: "build/qa/research-material-many-960.png", fullPage: true });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  if (overflow) {
    const offenders = await page.evaluate(() => [...document.querySelectorAll("*")].map((element) => {
      const rect = element.getBoundingClientRect();
      return { name: element.className || element.tagName, left: rect.left, right: rect.right, width: rect.width };
    }).filter((item) => item.right > window.innerWidth + 1 || item.left < -1).slice(0, 20));
    throw new Error(`mobile horizontal overflow: ${JSON.stringify(offenders)}`);
  }
  const last=rows.last();
  await last.click();
  await page.getByRole('dialog', {name: `研究资料 ${materials.length-1} 资料详情`, exact:true}).waitFor();
  await page.getByRole('button',{name:'关闭资料详情',exact:true}).click();
  await page.screenshot({path:'build/qa/materials-fixed-'+materials.length+'.png'});
  await page.getByRole('checkbox',{name:'全选当前资料',exact:true}).check();
  assert.equal(await page.locator('.research-materials-check:checked').count(),materials.length);
  await page.getByRole('button',{name:'移出所选',exact:true}).click();
  await page.getByRole('button',{name:'确认批量移出',exact:true}).click();
  await page.waitForFunction(()=>document.querySelectorAll('.research-materials-row').length===0);
  assert.deepEqual(errors, []);
  console.log('PASS: 100-item scrolling, detail, viewport bounds, select-all and bulk archive');
  await browser.close();
})().catch((error) => { console.error(error); process.exit(1); });
