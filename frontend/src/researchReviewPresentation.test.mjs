import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import * as presentation from './researchReviewPresentation.js';
const sample=[
 '在 workspace 中落地一份统一的报告文件(如 analysis-output/report.md)，包含方法、图和表。',
 '在 workflow 中追加显式登记独立的 peer-review 与 delivery gate 阶段，并由其给出通过或驳回结论，当前 stage 角色说明清楚。',
 '在报告与图注中显式声明 Tukey HSD mean_diff 的符号约定为 mean(group2)-mean(group1)，并与 claimSummary 一致。',
 '说明指数渐近与 Michaelis-Menten 未执行的原因及其对 researchContract.methodComponents 的完整性影响。',
 '说明 η² 95% CI 所用近似方法与潜在偏差，当前 n≈84。',
 '在 ANCOVA 报告中补充 light_hours×height_day0_cm 交互项的检验结果。',
];
const issues={groups:[{key:'numericIssues',label:'数字',items:['mean_diff 符号定义']},{key:'methodIssues',label:'方法',items:['甲','乙','丙']},{key:'requiredCorrections',label:'必须修正',items:sample}],total:10};
test('six required corrections are not double-counted with four categorized findings',()=>{
 const model=presentation.reviewFeedbackModel(issues);assert.equal(model.items.length,6);assert.equal(model.classifiedCount,4);assert.equal(model.correctionCount,6);assert.match(model.summary,/6 项修改/);assert.doesNotMatch(model.summary,/10/);
 for(let i=0;i<6;i++){assert.equal(model.items[i].raw,sample[i]);assert.doesNotMatch(model.items[i].title+model.items[i].explanation,/researchContract|workspace|workflow|mean_diff|light_hours|ANCOVA|peer-review|delivery gate/);}
});
test('unknown technical issues use cautious category guidance and keep exact evidence',()=>{
 const raw='新工具 model_abc.internal_field 不匹配';const item=presentation.presentReviewIssue(raw,'methodIssues');assert.equal(item.raw,raw);assert.doesNotMatch(item.explanation,/internal_field/);assert.doesNotMatch(item.explanation,/已经修复|可以交付|无需处理/);
});
test('ordinary researcher wording is preserved rather than replaced with generic text',()=>{
 const raw='请说明为何排除 6 名缺失随访记录的参与者。';assert.equal(presentation.presentReviewIssue(raw,'requiredCorrections').explanation,raw);
});
const plainCorrections = [
 '清理报告中的流程用语，保留合法引用。',
 '结论仅覆盖有结果的两类干预，其他方案保持不可估计。',
 '将机制判断改为文献作者的观点，并说明当前证据不足。',
 '删除超出证据范围的实践建议。',
 '说明组间差值 0.81 kg 的比较方向，保留置信区间。',
 '补充候选、导入材料和最终引用的筛选数量。',
 '核对并补全参考文献的出版年份。',
];
test('generic corrections retain each instruction without repetitive headings',()=>{
 const model=presentation.reviewFeedbackModel({groups:[{key:'requiredCorrections',label:'必须修正',items:plainCorrections}]});
 assert.equal(model.correctionCount,7);
 assert.deepEqual(model.items.map(item=>item.title),Array(7).fill(''));
 assert.deepEqual(model.items.map(item=>item.explanation),plainCorrections);
 assert.deepEqual(model.items.map(item=>item.raw),plainCorrections);
 for(const key of ['unsupportedClaims','citationIssues','numericIssues','methodIssues','unknown']){
  const item=presentation.presentReviewIssue(plainCorrections[0],key);
  assert.equal(item.title,'');assert.equal(item.explanation,plainCorrections[0]);
 }
});
test('missing or contradictory review does not invent approval or count',()=>{
 const model=presentation.reviewFeedbackModel({approved:true,groups:[],total:0});assert.equal(model.items.length,0);assert.match(model.summary,/无法确认/);assert.doesNotMatch(model.summary,/已通过/);
});
test('display text decodes numeric space entities without interpreting HTML',()=>{
 assert.equal(presentation.readableReviewText('未交付 &#x20;'),'未交付');assert.equal(presentation.readableReviewText('A&#32;B'),'A B');assert.equal(presentation.readableReviewText('&#x110000;'),'&#x110000;');
});
const require=createRequire(import.meta.url);const src=await readFile(new URL('./ResearchReviewFeedback.tsx',import.meta.url),'utf8');const exports={};vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+src,{compilerOptions:{jsx:ts.JsxEmit.React,module:ts.ModuleKind.CommonJS}}).outputText,{exports,require:id=>id==='./researchReviewPresentation.js'?presentation:require(id)});
test('generic correction cards render numbered content without empty heading elements',()=>{
 const html=renderToStaticMarkup(React.createElement(exports.ResearchReviewFeedback,{issues:{groups:[{key:'requiredCorrections',label:'必须修正',items:plainCorrections}]}}));
 const list=html.match(/<ol class="research-review-fixes">([\s\S]*?)<\/ol>/)?.[1];
 assert.ok(list);assert.equal((list.match(/<li>/g)||[]).length,7);
 assert.doesNotMatch(list,/<b>|这一项还需要修订/);
 for(const correction of plainCorrections) assert.ok(list.includes(correction));
 assert.match(html,/<details class="research-review-technical">/);
});
test('specific actionable headings are still displayed',()=>{
 const html=renderToStaticMarkup(React.createElement(exports.ResearchReviewFeedback,{issues}));
 assert.match(html,/<b>把两组差异的计算方向说明清楚<\/b>/);
});
test('original technical evidence is available but collapsed by default',()=>{
 const html=renderToStaticMarkup(React.createElement(exports.ResearchReviewFeedback,{issues,errorCode:'TOOL_INVOCATION_FAILED',errorMessage:'原始门禁错误'}));
 const details=html.indexOf('<details');assert.ok(details>0);assert.doesNotMatch(html.slice(0,details),/TOOL_INVOCATION_FAILED|researchContract|mean_diff/);assert.match(html.slice(details),/TOOL_INVOCATION_FAILED/);assert.match(html.slice(details),/researchContract/);assert.doesNotMatch(html,/<details[^>]*\bopen/);
});
test('tracked findings keep stable IDs and suggestions do not become blockers',()=>{
 const tracked=presentation.reviewTrackingModel({reviewFindings:[
  {id:'issue-1-1',status:'resolved',summary:'数字已核对',location:'结果',reason:'与计算一致',origin:'existing',changeReason:'已改正'},
  {id:'issue-2-1',status:'open',summary:'缺少方法',location:'方法',reason:'未说明',origin:'new',changeReason:'此前遗漏'},
  {id:'bad',status:'invented'},null],suggestions:['可缩短标题',42]});
 assert.equal(tracked.findings.length,2);assert.deepEqual(tracked.suggestions,['可缩短标题']);
 const data={groups:[{key:'requiredCorrections',label:'必须修正',items:['补方法']}],...tracked};
 assert.equal(presentation.reviewFeedbackModel(data).correctionCount,1);
 const html=renderToStaticMarkup(React.createElement(exports.ResearchReviewFeedback,{issues:data}));
 assert.match(html,/1 项待修正 · 1 项已解决 · 0 项已撤回/);assert.match(html,/issue-1-1/);assert.match(html,/可选建议 · 1/);assert.doesNotMatch(html,/<details[^>]*\bopen/);
});
