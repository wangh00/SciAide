import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import * as presentation from './citationRepairPresentation.js';

const candidates=Array.from({length:64},(_,i)=>({id:`source-${i}`,reference:`[K-${i}]`,quote:`Exact snapshot ${i}`,locator:`page:${i}`}));
const step={nodeKind:'citation_selection',input:{candidates,previousSelectedReferences:candidates.slice(0,15).map(ref=>ref.reference),screening:{recommendedReferences:candidates.slice(0,29).map(ref=>ref.reference)},citationRepairContext:{reviewOutputSha256:'review',requiredCorrections:['补入两段原文后按原目标比较'],mentionedReferences:['[K-18]','[K-19]','[K-UNKNOWN]'],repeatedIssueCount:1}}};
const keys=candidates.slice(0,15).map(ref=>JSON.stringify(ref));

test('unchanged repair identifies exact unchosen review passages, without selecting them',()=>{
 const before=JSON.stringify(step);const model=presentation.citationRepairModel(step,keys);
 assert.equal(model.previousCount,15);assert.equal(model.added,0);assert.equal(model.removed,0);assert.equal(model.unchanged,true);
 assert.deepEqual(model.missingMentioned,[candidates[18],candidates[19]]);assert.equal(model.missingRecommended.length,14);assert.equal(model.mentioned.size,2);
 assert.equal(JSON.stringify(step),before);assert.equal(keys.length,15);
});
test('explicit supplementation preserves non-recommended choices and exact snapshots',()=>{
 const custom=[...keys,JSON.stringify(candidates[50])];
 const supplemented=presentation.supplementCitationKeys(custom,presentation.citationRepairModel(step,custom).missingRecommended);
 assert.equal(supplemented.length,30);assert.ok(supplemented.includes(custom.at(-1)));assert.equal(custom.length,16);
 assert.equal(new Set(supplemented).size,30);
 const model=presentation.citationRepairModel(step,supplemented);assert.equal(model.added,15);assert.equal(model.removed,0);assert.equal(model.missingMentioned.length,0);
 const removed=presentation.citationRepairModel(step,keys.slice(1));assert.equal(removed.removed,1);assert.equal(removed.added,0);
});
test('edited quotes do not masquerade as selected offered snapshots; malformed context is safe',()=>{
 const bad=JSON.stringify({...candidates[18],quote:'tampered'});
 assert.equal(presentation.citationRepairModel(step,[...keys,bad]).missingMentioned.length,2);
 assert.equal(presentation.citationRepairModel({nodeKind:'candidate_selection',input:step.input},keys),null);
 assert.equal(presentation.citationRepairModel({nodeKind:'citation_selection',input:{}},keys),null);
 const malformed={...step,input:{...step.input,previousSelectedReferences:42,citationRepairContext:{reviewOutputSha256:'r',mentionedReferences:42}}};
 assert.equal(presentation.citationRepairModel(malformed,keys).mentioned.size,0);
});
const source=await readFile(new URL('./ResearchCitationRepair.tsx',import.meta.url),'utf8');
const exports={};const require=createRequire(import.meta.url);
vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+source,{compilerOptions:{jsx:ts.JsxEmit.React,module:ts.ModuleKind.CommonJS}}).outputText,{exports,require:id=>id==='./citationRepairPresentation.js'?presentation:require(id)});
test('visible repair explains repeat risk and has selection actions, not a second plan/execute button',()=>{
 const props={model:presentation.citationRepairModel(step,keys),selectedKeys:keys,update:()=>{},busy:false};
 const html=renderToStaticMarkup(React.createElement(React.Fragment,null,React.createElement(exports.ResearchCitationRepair,props),React.createElement(exports.ResearchCitationRepairActions,props),React.createElement(exports.ResearchCitationRepairStatus,props)));
 assert.match(html,/引用选择尚未变化/);assert.match(html,/你仍可按当前选择继续/);assert.match(html,/补入审查提及引用（2）/);assert.match(html,/补入未选推荐（14）/);assert.match(html,/不等于科学充分性/);assert.match(html,/补入两段原文后按原目标比较/);
 assert.doesNotMatch(html,/让 AI 整理返修方案|确认.*并开始返修|class="primary"/);
 const busy=renderToStaticMarkup(React.createElement(exports.ResearchCitationRepairActions,{...props,busy:true}));
 assert.equal((busy.match(/disabled=""/g)||[]).length,2);
});

test('supplement controls live beside the count and warning beside confirmation, not duplicate buttons',async()=>{
 const app=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
 assert.match(app,/workflow-selection-toolbar[\s\S]*?<span>已选[\s\S]*?<nav>\{citationRepair && <ResearchCitationRepairActions/);
 assert.match(app,/citationRepair && <ResearchCitationRepairStatus[\s\S]*?<footer>/);
 assert.match(app,/仍按原 \$\{selectedCount\} 条引用/);assert.match(app,/确认所选 \$\{selectedCount\} 条引用/);
 assert.equal((app.match(/<ResearchCitationRepairActions /g)||[]).length,1);
});
