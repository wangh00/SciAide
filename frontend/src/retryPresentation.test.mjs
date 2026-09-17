import assert from 'node:assert/strict';import test from 'node:test';import {readFile} from 'node:fs/promises';
import {applyRetryEvent,retryStatusLabel} from './retryPresentation.js';
const retry={phase:'request',attempt:2,maxAttempts:5,delayMillis:6000,message:'retry'};
test('retry UI shows exact attempt budget and actual scheduled wait',()=>{
 assert.equal(retryStatusLabel(retry),'请求重试 2/5 · 本次重试间隔 6 秒');
 assert.match(retryStatusLabel({...retry,phase:'stream',attempt:5}),/连接重连 5\/5/);
 assert.equal(retryStatusLabel(null),'');assert.equal(retryStatusLabel({...retry,phase:'planner_skill_validation',message:'补齐 Skill'}),'补齐 Skill');
});
test('retry events remain scoped to their own run and clear on recovery or termination',()=>{
 let current=applyRetryEvent({}, {aggregateId:'a',type:'run.retrying',payload:{retry}});
 current=applyRetryEvent(current,{aggregateId:'b',type:'run.retrying',payload:{retry:{...retry,attempt:1}}});
 assert.equal(current.a.attempt,2);assert.equal(current.b.attempt,1);
 current=applyRetryEvent(current,{aggregateId:'b',type:'run.retry.recovered'});assert.equal(current.b,undefined);assert.equal(current.a.attempt,2);
 current=applyRetryEvent(current,{aggregateId:'a',type:'run.failed'});assert.deepEqual(current,{});
});
test('research and ordinary message cards both receive the run-scoped retry status',async()=>{
 const src=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
 assert.match(src,/retryStatus=\{message.runId \? retryByRun\[message.runId\]/);
 assert.match(src,/WorkflowMessageActivityCard activity=\{workflowActivity\} retryStatus=\{retryStatus\}/);
 assert.match(src,/running && retryStatusLabel\(retryStatus\)/);
 assert.match(src,/const liveDetail = retryStatusLabel\(retryStatus\)/);
});

test('research card visibly renders retry count and interval only while running', async()=>{
 const [{default:ts},{default:React},{renderToStaticMarkup},{createRequire},{default:vm}]=await Promise.all([import('typescript'),import('react'),import('react-dom/server'),import('node:module'),import('node:vm')]);
 const src=await readFile(new URL('./App.tsx',import.meta.url),'utf8');const start=src.indexOf('function WorkflowMessageActivityCard('),end=src.indexOf('function WorkflowTimelineToolCard(',start);assert.ok(start>=0&&end>start);
 const exports={};vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+src.slice(start,end)+'\nexport {WorkflowMessageActivityCard}',{compilerOptions:{module:ts.ModuleKind.CommonJS,jsx:ts.JsxEmit.React}}).outputText,{exports,require:createRequire(import.meta.url),retryStatusLabel,normalizeDisplayText:x=>x,formatResearchActivityDuration:()=>'',RunProcessFrame:props=>React.createElement('div',null,props.liveDetail)});
 const entry={activity:{status:'running',toolCalls:[],elapsedSeconds:0,executionId:'e'},workflowTools:[],approvals:[]};
 const render=()=>renderToStaticMarkup(React.createElement(exports.WorkflowMessageActivityCard,{activity:entry,busy:'',retryStatus:retry}));
 assert.match(render(),/请求重试 2\/5 · 本次重试间隔 6 秒/);entry.activity.status='completed';assert.doesNotMatch(render(),/重试 2\/5/);
});
