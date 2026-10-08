import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';

const {ResearchCard,ResearchCardFrame}=loadTsx(new URL('./ResearchCard.tsx',import.meta.url));
test('the common card owns presentation, never adds execution or confirmation',()=>{
 const render=props=>renderToStaticMarkup(React.createElement(ResearchCard,{title:'标题',...props}));
 const html=render({eyebrow:'上下文',status:'待确认',tone:'pending',description:'保留说明',children:'原始正文',footer:React.createElement('button',{type:'button',disabled:true},'唯一业务动作')});
 for(const text of ['research-card--pending','research-card-heading','research-card-body','research-card-footer','原始正文','保留说明']) assert.ok(html.includes(text));
 assert.equal((html.match(/<button/g)||[]).length,1);assert.match(html,/<button[^>]*disabled/);
 assert.doesNotMatch(render({children:'正文'}),/<button|<footer/);
 assert.match(render({titleLevel:2}),/<h2>标题<\/h2>/);
 assert.match(renderToStaticMarkup(React.createElement(ResearchCardFrame,{'aria-label':'历史',children:'冻结文本'})),/aria-label="历史"/);
});
test('compound checkpoint content is not wrapped or rearranged by the frame',()=>{
 const body=React.createElement('form',null,React.createElement('input',{name:'original'}));
 const html=renderToStaticMarkup(React.createElement(ResearchCard,{title:'检查点',bodyMode:'contents',children:body}));
 assert.match(html,/<\/header><form>/);assert.doesNotMatch(html,/research-card-body/);
});
test('deliverables and interactive checkpoints actually use the common component',async()=>{
 for(const file of ['ResearchRevisionCards.tsx','ResearchReportCard.tsx','ResearchAnalysisResult.tsx']) {
  const source=await readFile(new URL(file,import.meta.url),'utf8');assert.match(source,/from ['"]\.\/ResearchCard['"]/);assert.match(source,/<ResearchCard\b/);
 }
 const app=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
 for(const name of ['research-deliverable-card','research-interaction-card decision','research-interaction-card error','research-interaction-card research-route-choice','research-interaction-card research-replan-card','research-interaction-card research-delivery-assessment']) assert.ok(app.includes(`<ResearchCard className="${name}"`));
});

test('design delivery retains registration actions and frozen scientific content',async()=>{
 const source=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
 const begin=source.indexOf('function WorkflowResearchDesignCard('), end=source.indexOf('function WorkflowResearchReportCard(',begin);
 const exports={};
 const code=ts.transpileModule(source.slice(begin,end)+'\nexport { WorkflowResearchDesignCard };',{compilerOptions:{module:ts.ModuleKind.CommonJS,jsx:ts.JsxEmit.ReactJSX,target:ts.ScriptTarget.ES2022}}).outputText;
 vm.runInNewContext(code,{exports,require:createRequire(import.meta.url),ResearchCard,Icon:()=>null,ResearchAcceptanceDetails:()=>null,workflowResearchDesignSections:design=>[['研究目标',design.objectives]]});
 const design={title:'原始设计标题',researchQuestion:'原始研究问题',objectives:['完整目标'],hypotheses:[],milestones:[]};
 const render=extra=>renderToStaticMarkup(React.createElement(exports.WorkflowResearchDesignCard,{design,registered:false,busy:'',saveDeliverable(){},openArtifacts(){},...extra}));
 const html=render({busy:'save-deliverable'});
 assert.match(html,/research-card--success/);assert.match(html,/原始设计标题/);assert.match(html,/完整目标/);assert.match(html,/<button[^>]*disabled/);assert.match(html,/正在登记/);
 const registered=render({registered:true});assert.match(registered,/已登记 · 查看科研产物/);assert.doesNotMatch(registered,/登记为科研产物/);
 assert.deepEqual(design,{title:'原始设计标题',researchQuestion:'原始研究问题',objectives:['完整目标'],hypotheses:[],milestones:[]});
});
