import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
const source=await readFile(new URL('./ResearchRevisionCards.tsx',import.meta.url),'utf8');
const exports={};vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+source,{compilerOptions:{jsx:ts.JsxEmit.React,module:ts.ModuleKind.CommonJS}}).outputText,{exports,require:createRequire(import.meta.url)});
const proposal={id:'new',status:'pending',canConfirm:true,nodeId:'method_implementation',label:'实现分析方法',summary:'修复图中文字',changes:['保留统计方法','修复中文字体'],reason:'需要修改绘图代码',affectedStages:['实现分析方法','执行可复现分析','报告撰写','独立审查','交付门禁'],repeatsSideEffects:true};
const render=(proposals=[proposal],busy=false)=>renderToStaticMarkup(React.createElement(exports.ResearchRevisionCards,{proposals,busy,conversationId:'conversation',confirm:()=>{}}));
test('confirmation displays complete negotiated changes and host replay scope',()=>{
 const html=render();for(const text of ['修复图中文字','保留统计方法','修复中文字体','独立审查','交付门禁','模型调用费用','确认方案并开始返修']) assert.ok(html.includes(text));assert.doesNotMatch(html,/disabled/);
});
test('old and confirmed proposals cannot execute and stay collapsed',()=>{
 for(const status of ['superseded','confirmed']) { const html=render([{...proposal,status}]);assert.doesNotMatch(html,/<button/);assert.doesNotMatch(html,/<details open/); }
});
test('generation and local mutation block confirmation',()=>{
 const generating=render([{...proposal,canConfirm:false}]);
 assert.match(generating,/正在整理返修方案/);
 assert.doesNotMatch(generating,/<button|确认返修方案|待确认|修复图中文字/);
 assert.match(render([proposal],true),/<button[^>]*disabled/);
});
test('new natural-language turns immediately invalidate current cards',()=>{
 assert.match(source,/research-discussion-sent/);assert.match(source,/invalidated\.has\(proposal.id\)/);
});
