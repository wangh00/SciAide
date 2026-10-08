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
test('cancelled research recovery is explicit and restricted to the current AI stage',async()=>{
 const app=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
 assert.match(app,/detail\.run\.status === "cancelled" && step\.status === "cancelled" && step\.ordinal === detail\.run\.currentStep && \["agent_stage", "ai_analysis"\]\.includes\(step\.nodeKind\)/);
 assert.match(app,/cancelledAIRecovery \? "恢复当前阶段" : "重试当前阶段"/);
 assert.match(app,/保留已完成阶段、原始资料和历史记录/);
 assert.match(app,/不会自动恢复；点击后会再次调用模型，可能产生费用/);
 assert.match(app,/onClick=\{\(\) => retryStep\(retryableStep\)\}/);
});
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
test('genuinely missing input stays visible, without another plan button or copy step',()=>{
 const html=render({recommendation:{status:'needs_input',summary:'先补充资料',requiredInputs:['核查 bmj.full.pdf 是否进入索引','确认研究人群'],repeatsSideEffects:false}});
 assert.match(html,/核查 bmj.full.pdf 是否进入索引/);assert.match(html,/确认研究人群/);
 assert.match(html,/单纯重复执行不能补齐/);assert.match(html,/<button[^>]*class="primary"[^>]*disabled/);
 assert.doesNotMatch(html,/让 AI 整理返修方案|复制补充要求|先讨论完整返修方案/);
 assert.doesNotMatch(source,/navigator.clipboard|discussRepair|copyRequirements/);
});
test('legacy adoption repair has exactly one direct execution action and preserves other missing inputs',()=>{
 const props={targets:[{nodeId:'evidence_extraction',label:'引用选择',repeatsSideEffects:false}],reselection:{nodeId:'evidence_extraction',availableCount:64,recommendedCount:29,selectedRecommendedCount:15,missingRecommendedCount:14,previouslyUnofferedCount:14,reviewOutputSha256:'review'},recommendation:{status:'needs_input',summary:'confirm',requiredInputs:['仍需原始数据'],reviewOutputSha256:'review',repeatsSideEffects:false}};
 const html=render(props);assert.match(html,/打开引用补选/);assert.match(html,/此按钮只打开引用补选/);assert.match(html,/14 条没有出现在旧选择列表/);assert.match(html,/程序遗漏，不是你主动排除/);assert.match(html,/不重跑导入、核读或综合/);assert.match(html,/不保证其他科学缺口已解决/);assert.match(html,/仍需原始数据/);
 assert.equal((html.match(/class="primary"/g)||[]).length,1);
 assert.doesNotMatch(html,/重新选择已有证据<|让 AI 整理返修方案|复制补充要求/);
 assert.match(render({...props,busy:true}),/<button[^>]*class="primary"[^>]*disabled/);
 assert.match(render({...props,reselection:{...props.reselection,reviewOutputSha256:''}}),/<button[^>]*class="primary"[^>]*disabled/);
 assert.match(render({...props,reselection:{...props.reselection,reviewOutputSha256:'stale'}}),/<button[^>]*class="primary"[^>]*disabled/);
 // Unchosen citations by themselves do not turn genuine missing input into
 // a default executable repair; only a verified old-list omission gets it.
 assert.match(render({...props,reselection:{...props.reselection,previouslyUnofferedCount:0}}),/<button[^>]*class="primary"[^>]*disabled/);
});
test('a valid full recommendation wins over optional reselection, with consent only for its own scope',()=>{
 const html=render({targets:[{...target,repeatsSideEffects:false},{nodeId:'evidence_extraction',label:'引用选择',repeatsSideEffects:true}],recommendation:{...recommendation,repeatsSideEffects:false},reselection:{nodeId:'evidence_extraction',reviewOutputSha256:'current-review'}});
 assert.match(html,/按建议修改/);assert.doesNotMatch(html,/打开引用补选|type="checkbox"/);
 assert.equal((html.match(/class="primary"/g)||[]).length,1);
});

test('recommended citation repair opens a checkpoint, not a completed modification or automatic adoption',()=>{
 const html=render({targets:[{nodeId:'evidence_extraction',label:'引用选择',repeatsSideEffects:false}],recommendation:{status:'recommended',nodeId:'evidence_extraction',label:'引用选择',summary:'补入已有原文',reviewOutputSha256:'review',repeatsSideEffects:false},reselection:{nodeId:'evidence_extraction',availableCount:64,recommendedCount:29,selectedRecommendedCount:15,missingRecommendedCount:14,previouslyUnofferedCount:0,reviewOutputSha256:'review'}});
 assert.match(html,/打开引用补选/);assert.match(html,/需要在列表中点/);assert.doesNotMatch(html,/按建议修改<|确认补选并返修/);
 assert.equal((html.match(/class="primary"/g)||[]).length,1);
});
test('recommendations with unknown targets or missing review identity are disabled',()=>{
 assert.match(render({recommendation:{...recommendation,nodeId:'unknown'}}),/<button[^>]*class="primary"[^>]*disabled/);
 assert.match(render({recommendation:{...recommendation,reviewOutputSha256:''}}),/<button[^>]*class="primary"[^>]*disabled/);
});
test('new review identity resets prior selection and confirmation',()=>{
 assert.match(source,/recommendation\?\.reviewOutputSha256/);assert.match(source,/setSelected\(''\); setConfirmed\(false\)/);
});
