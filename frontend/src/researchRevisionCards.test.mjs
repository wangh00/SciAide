import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';
const source=await readFile(new URL('./ResearchRevisionCards.tsx',import.meta.url),'utf8');
const exports=loadTsx(new URL('./ResearchRevisionCards.tsx',import.meta.url));
const proposal={id:'new',status:'pending',canConfirm:true,nodeId:'method_implementation',label:'实现分析方法',summary:'修复图中文字',changes:['保留统计方法','修复中文字体'],reason:'需要修改绘图代码',affectedStages:['实现分析方法','执行可复现分析','报告撰写','独立审查','交付门禁'],repeatsSideEffects:true};
const render=(proposals=[proposal],busy=false)=>renderToStaticMarkup(React.createElement(exports.ResearchRevisionCards,{proposals,busy,conversationId:'conversation',confirm:()=>{}}));
test('confirmation displays complete negotiated changes and host replay scope',()=>{
 const html=render();for(const text of ['修复图中文字','保留统计方法','修复中文字体','独立审查','交付门禁','模型调用费用','确认方案并开始返修']) assert.ok(html.includes(text));assert.doesNotMatch(html,/disabled/);
});

test('supplemental documents are named in confirmation, without claiming automatic adoption',()=>{
 const html=render([{...proposal,materials:[{attachmentId:'a',name:'new-paper.pdf',sha256:'hash'}]}]);
 assert.match(html,/确认后纳入的补充文献/);assert.match(html,/new-paper.pdf/);assert.match(html,/不会重新搜索候选文献/);
});
test('import proposal without bound documents warns before confirmation',()=>{
 const html=render([{...proposal,nodeId:'evidence_import',changes:['纳入 paper.pdf']}]);
 assert.match(html,/此方案未绑定补充文献/);assert.match(html,/文字中提到的附件不会自动纳入/);
 assert.doesNotMatch(render([{...proposal,nodeId:'evidence_import',materials:[{attachmentId:'a',name:'paper.pdf',sha256:'hash'}]}]),/此方案未绑定补充文献/);
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

 test('reused stages are visible without a separate workspace',()=>{
 const html=render([{...proposal,reusedStages:['数据预检','证据整理']}]);
 assert.match(html,/沿用已有结果/);assert.match(html,/数据预检、证据整理/);assert.match(html,/这些阶段不重新执行/);
 assert.doesNotMatch(render(),/沿用已有结果/);
 });

test('data update names the complete confirmed set and preserves old versions',()=>{
 const html=render([{...proposal,nodeId:'data_preflight',dataVersion:{previousPaths:['research-inputs/old.csv'],files:[{relativePath:'research-inputs/keep.csv',name:'keep.csv',sha256:'hash'},{relativePath:'research-inputs/new.csv',name:'new.csv',sha256:'new-hash',attachmentId:'sent'}]}}]);
 for(const text of ['更新前的数据','research-inputs/old.csv','确认后使用的完整数据集合','keep.csv','new.csv','沿用','对话上传','旧数据及旧成果保留','不会自动拼接']) assert.ok(html.includes(text));
 assert.match(render([{...proposal,nodeId:'data_preflight'}]),/沿用当前已确认的数据版本/);
});

test('source reference is named without claiming direct execution or data replacement',()=>{
 const html=render([{...proposal,sourceVersion:{files:[{relativePath:'research-inputs/method.py',name:'method.py',sha256:'source-hash',attachmentId:'sent'}]}}]);
 for(const text of ['源码参考更新','method.py','source-hash','不会直接运行上传文件','表格数据集合不因此改变','旧代码和旧结果保留']) assert.ok(html.includes(text));
 assert.match(render([{...proposal,sourceVersion:{files:[]}}]),/取消当前源码参考/);
});

test('pending proposal shares the real frame and keeps full changes in a labelled disclosure',()=>{
 const html=render([{...proposal,changes:['literal <script>not executable</script>','完整要求\nSHA-256: identity']}]);
 assert.match(html,/research-card research-card--pending/);
 assert.match(html,/完整修改要求/);assert.match(html,/2 项 · 确认前请核对/);
 assert.doesNotMatch(html,/<details[^>]*class="research-revision-changes"[^>]*open/);
 assert.match(html,/&lt;script&gt;not executable&lt;\/script&gt;/);
 assert.match(html,/完整要求\nSHA-256: identity/);
 assert.equal((html.match(/<button/g)||[]).length,1);
 assert.ok(html.indexOf('返修执行范围') < html.indexOf('完整修改要求'));
});
