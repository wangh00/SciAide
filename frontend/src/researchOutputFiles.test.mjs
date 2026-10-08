import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
const source=await readFile(new URL('./ResearchOutputFiles.tsx',import.meta.url),'utf8');
const exports={};
vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+source,{compilerOptions:{jsx:ts.JsxEmit.React,module:ts.ModuleKind.CommonJS}}).outputText,{exports,require:createRequire(import.meta.url)});
const render=steps=>renderToStaticMarkup(React.createElement(exports.ResearchOutputFiles,{steps,openArtifacts:()=>{}}));
test('generated tables and figures are visible inline with version identity',()=>{
 const html=render([{nodeId:'python_analysis',attempt:2,status:'completed',output:{artifacts:[{name:'比较表',workspacePath:'analysis-output/results.csv'},{workspacePath:'analysis-output/plot.svg'}]}}]);
 for(const text of ['比较表','plot.svg','第 2 次执行','预览或下载','本次生成的文件']) assert.ok(html.includes(text));
});
test('failed, missing, and malformed outputs do not invent files',()=>{
 assert.equal(render([{nodeId:'p',status:'failed',output:{artifacts:[{workspacePath:'bad.csv'}]}},{status:'completed',output:{artifacts:[null,{},'wrong']}}]),'');
 assert.equal(render([]),'');
});

test('generated identifiers are shortened only in display and preflight is distinguished',()=>{
 const prefix='49e3fbaa-2121-461f-8e39-55b31aa6c245-1';
 const html=render([{nodeId:'data_preflight',attempt:1,status:'completed',output:{artifacts:[{name:`preflight-${prefix}.json`,workspacePath:'analysis-output/preflight.json'}]}},{nodeId:'python_analysis',attempt:1,status:'completed',output:{artifacts:[{name:`dynamic-${prefix}-metrics.csv`,workspacePath:'analysis-output/full-metrics.csv'},{name:42,workspacePath:'results/data.tsv'}]}}]);
 assert.match(html,/本次生成的文件 · 3/);
 assert.match(html,/output-file-name">metrics.csv/);
 assert.match(html,/output-file-name">数据预检.json/);
 assert.match(html,/预检记录/);
 assert.match(html,/title="dynamic-49e3fbaa/);
 assert.match(html,/analysis-output\/full-metrics.csv/);
 assert.match(html,/type-csv/);
 assert.match(html,/data.tsv/);
});

test('code display uses successful frozen Kernel input, never the implementation draft',()=>{
 const step={nodeId:'python_analysis',attempt:2,status:'completed',input:{code:'print("<script>literal</script>")'},output:{structured:{status:'success',codeSha256:'a'.repeat(64)}}};
 const before=structuredClone(step);const html=render([step,{nodeId:'method_implementation',status:'completed',output:{analysis:{code:'DECOY'}}}]);
 assert.match(html,/实际执行代码/);assert.match(html,/第 2 次执行/);assert.match(html,/&lt;script&gt;literal/);assert.doesNotMatch(html,/DECOY|<script>/);assert.deepEqual(step,before);
 for(const invalid of [{...step,status:'failed'},{...step,output:{structured:{status:'error',codeSha256:'a'.repeat(64)}}},{...step,output:{structured:{status:'success',codeSha256:'bad'}}}]) assert.equal(render([invalid]),'');
});
