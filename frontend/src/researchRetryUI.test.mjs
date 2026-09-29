import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
const source=await readFile(new URL('./ResearchRevisionControls.tsx',import.meta.url),'utf8');
const exports={};vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+source,{compilerOptions:{jsx:ts.JsxEmit.React,module:ts.ModuleKind.CommonJS}}).outputText,{exports,require:createRequire(import.meta.url)});
const target={nodeId:'method_implementation',label:'分析方法实现',repeatsSideEffects:true};
const recommendation={status:'recommended',nodeId:target.nodeId,label:target.label,summary:'计算问题需要从代码修订开始。',reasons:['修改报告不能修正实际计算。'],affectedStages:['分析方法实现','执行计算','报告撰写','二次审查'],repeatsSideEffects:true,reviewOutputSha256:'current-review'};
const render=(props={})=>renderToStaticMarkup(React.createElement(exports.ResearchRevisionControls,{resetKey:'run:step:1',targets:[target],recommendation,busy:false,confirmed:false,setConfirmed:()=>{},submit:()=>{},...props}));
test('recommendation explains affected stages and costs, manual choices stay collapsed',()=>{
 const html=render();assert.match(html,/建议从「分析方法实现」/);assert.match(html,/执行计算/);assert.match(html,/模型调用费用/);assert.match(html,/为什么这样建议/);assert.match(html,/调整修改范围/);assert.doesNotMatch(html,/<select/);assert.match(html,/按建议修改/);
});
test('side-effect consent and busy state gate recommended execution',()=>{
 assert.match(render(),/<button[^>]*class="primary"[^>]*disabled/);
 assert.doesNotMatch(render({confirmed:true}),/<button[^>]*class="primary"[^>]*disabled/);
 assert.match(render({confirmed:true,busy:true}),/<button[^>]*class="primary"[^>]*disabled/);
});
test('legacy and input-blocked reviews never fall back to modifying the report',()=>{
 for(const value of [undefined,{status:'unavailable',summary:'旧审查无建议',repeatsSideEffects:false}]) {
  const html=render({recommendation:value});assert.match(html,/<button[^>]*class="primary"[^>]*disabled/);assert.doesNotMatch(html,/只修改报告内容（默认）/);
 }
});
test('input-blocked review replaces dead retry with explicit gaps and copy action',()=>{
 const html=render({recommendation:{status:'needs_input',summary:'先补充资料',requiredInputs:['核查 bmj.full.pdf 是否进入索引','确认研究人群'],repeatsSideEffects:false}});
 assert.match(html,/复制补充要求/);assert.match(html,/核查 bmj.full.pdf 是否进入索引/);assert.match(html,/确认研究人群/);assert.match(html,/请新建任务/);
 assert.doesNotMatch(html,/<button[^>]*class="primary"[^>]*disabled/);assert.doesNotMatch(html,/<span>按建议修改/);
 assert.match(render({busy:true,recommendation:{status:'needs_input',summary:'缺文件'}}),/<button[^>]*class="primary"[^>]*disabled/);
 assert.match(source,/navigator.clipboard.writeText/);assert.match(source,/复制失败/);
});
test('recommendations with unknown targets or missing review identity are disabled',()=>{
 assert.match(render({recommendation:{...recommendation,nodeId:'unknown'}}),/<button[^>]*class="primary"[^>]*disabled/);
 assert.match(render({recommendation:{...recommendation,reviewOutputSha256:''}}),/<button[^>]*class="primary"[^>]*disabled/);
});
test('new review identity resets prior selection and confirmation',()=>{
 assert.match(source,/recommendation\?\.reviewOutputSha256/);assert.match(source,/setSelected\(''\); setConfirmed\(false\)/);
});
