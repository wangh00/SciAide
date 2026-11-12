import assert from "node:assert/strict";
import test from "node:test";
import {readFile} from "node:fs/promises";
import {createRequire} from "node:module";
import vm from "node:vm";
import ts from "typescript";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import {runtimeActivityForRun, runtimeActivityLabel} from "./runtimeActivity.js";

const run = {id:"run", status:"running", modelTurns:4, createdAt:"2026-10-09T12:00:00Z"};
const activity = {runId:"run", phase:"compaction", turnIndex:4, startedAt:"2026-10-09T12:01:00Z"};
test("runtime phases distinguish actual compaction from provider wait without claiming hidden reasoning",()=>{
  const now = Date.parse("2026-10-09T12:03:07Z");
  assert.equal(runtimeActivityLabel(activity,run,now),"第4轮 · 正在压缩会话上下文，原始历史保留 · 2分7秒");
  assert.match(runtimeActivityLabel({...activity,phase:"model_wait"},run,now),/等待模型响应/);
  assert.match(runtimeActivityLabel({...activity,phase:"model_processing"},run,now),/模型已响应/);
  assert.match(runtimeActivityLabel({...activity,phase:"model_processing"},run,now),/尚未完成/);
  assert.match(runtimeActivityLabel({...activity,phase:"implementation_wait"},run,now),/已保存实现片段.*尚未执行/);
  assert.match(runtimeActivityLabel({...activity,phase:"implementation_validation"},run,now),/源码已组装.*尚未执行/);
  assert.match(runtimeActivityLabel({...activity,phase:"compaction_completed"},run,now),/摘要已保存/);
  for (const value of [null,{}, {...activity,runId:"other"},{...activity,turnIndex:3},{...activity,phase:"unknown"},{...activity,startedAt:"bad"}]) assert.equal(runtimeActivityForRun(value,run),null);
  for(const status of ["completed","failed","cancelled","interrupted","waiting_user_input","waiting_approval"]) assert.equal(runtimeActivityLabel(activity,{...run,status},now),"");
  assert.match(runtimeActivityLabel(activity,run,Date.parse("2026-10-09T12:00:00Z")),/0秒$/);
});

const source = await readFile(new URL("./App.tsx", import.meta.url),"utf8");
const from=source.indexOf("function RunProcess("), to=source.indexOf("function HistoricalRunProcess(",from);
const code=ts.transpileModule('import React,{useState,useEffect} from "react";\n'+source.slice(from,to)+'\nexport {RunProcess};',{compilerOptions:{module:ts.ModuleKind.CommonJS,jsx:ts.JsxEmit.React,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText;
const exports={};
vm.runInNewContext(code,{exports,require:createRequire(import.meta.url),runtimeActivityLabel,retryStatusLabel:s=>s?.message||"",runDuration:()=>"3m",toolPresentation:()=>({title:"读取指定材料"}),safeToolArguments:v=>v,Icon:()=>null,RunTimeline:()=>null,ReasoningPrelude:()=>null,ToolActivityCard:()=>null});
const props={run,active:true,retryStatus:null,runtimeActivity:activity,steps:[],toolCalls:[],approvals:[],resolvingApprovalId:"",resolveApproval:async()=>{}};
const render=p=>renderToStaticMarkup(React.createElement(exports.RunProcess,{...props,...p}));
test("actual RunProcess renders status in its existing waterfall bar and honors wait/tool/retry priorities",()=>{
  assert.match(render({}),/正在压缩会话上下文/);
  assert.match(render({retryStatus:{message:"正在自动重试 1\/5"}}),/正在自动重试/);
  assert.doesNotMatch(render({retryStatus:{message:"正在自动重试"}}),/正在压缩/);
  assert.match(render({run:{...run,status:"waiting_user_input"}}),/等待你的回答/);
  assert.doesNotMatch(render({run:{...run,status:"waiting_user_input"}}),/正在压缩/);
  assert.match(render({toolCalls:[{id:"call",status:"running",toolName:"builtin.document.read"}]}),/读取指定材料/);
  assert.doesNotMatch(render({run:{...run,status:"completed"},active:false}),/正在压缩/);
  assert.match(source,/runtimeActivityForRun\(normalized.activity, normalized.run\)/);
  assert.match(source,/runtimeActivity=\{messageRun \? runtimeActivity : null\}/);
  assert.doesNotMatch(source,/已有 checkpoint/);
});
