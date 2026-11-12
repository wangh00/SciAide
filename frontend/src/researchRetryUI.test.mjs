import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {workflowStepCanRetry,workflowReviewGateRejected} from './researchTimelineState.js';
const source=await readFile(new URL('./ResearchRevisionControls.tsx',import.meta.url),'utf8');
const exports={};vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+source,{compilerOptions:{jsx:ts.JsxEmit.React,module:ts.ModuleKind.CommonJS}}).outputText,{exports,require:createRequire(import.meta.url)});
const target={nodeId:'method_implementation',label:'分析方法实现',repeatsSideEffects:true};
const recommendation={status:'recommended',nodeId:target.nodeId,label:target.label,summary:'计算问题需要从代码修订开始。',reasons:['修改报告不能修正实际计算。'],affectedStages:['分析方法实现','执行计算','报告撰写','二次审查'],repeatsSideEffects:true,reviewOutputSha256:'current-review'};
const defaultProps={resetKey:'run:step:1',targets:[target],recommendation,busy:false,submit:()=>{}};
const render=(props={})=>renderToStaticMarkup(React.createElement(exports.ResearchRevisionControls,{...defaultProps,...props}));
const appSource=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
// Exercise the real component's event handlers with deterministic hook slots.
const actualRequire=createRequire(import.meta.url),eventExports={};let slots=[],cursor=0;
vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+source,{compilerOptions:{jsx:ts.JsxEmit.React,module:ts.ModuleKind.CommonJS}}).outputText,{exports:eventExports,require:name=>name==='react'?{...actualRequire(name),useEffect:()=>{},useState:initial=>{const i=cursor++;if(!(i in slots))slots[i]=initial;return [slots[i],value=>{slots[i]=value}];}}:actualRequire(name)});
function elements(node,type){if(!node||typeof node!=='object')return [];return [...(node.type===type?[node]:[]),...React.Children.toArray(node.props?.children).flatMap(child=>elements(child,type))];}
function harness(props={}){slots=[];const calls=[];return {calls,render:(overrides={})=>{cursor=0;return eventExports.ResearchRevisionControls({...defaultProps,...props,submit:(...args)=>calls.push(args),...overrides});}};}
const primary=tree=>elements(tree,'button').find(node=>node.props.className==='primary');
test('card and feed share recovery eligibility, with an explicit current-stage action',async()=>{
 const app=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
 assert.match(app,/detail.steps.find\(\(step\) => workflowStepCanRetry\(detail.run, step, recovery\)\)/);
 assert.match(app,/"workflow.cancel_requested"\].includes\(entry.eventType \?\? ""\) && workflowStepCanRetry\(runDetail.run, step, runDetail.recovery\)/);
 assert.match(app,/aiStageRecovery \? "恢复当前阶段" : gateTechnicalFailure \? "重试交付核验" : "重试当前阶段"/);
 assert.match(app,/保留仍有效的已完成阶段、原始资料和历史记录/);
 assert.match(app,/不会自动恢复；点击后重新执行当前 AI 阶段/);
 assert.match(app,/onClick=\{\(\) => retryStep\(retryableStep\)\}/);
});
test('interrupted AI recovery reuses the existing card and click, never starts itself',()=>{
 const parsed=ts.createSourceFile('App.tsx',appSource,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
 const component=parsed.statements.find(node=>ts.isFunctionDeclaration(node)&&node.name?.text==='WorkflowInteractionCards');
 assert.ok(component);
 const exports={};const calls=[];
 const card=(props)=>React.createElement('section',null,props.eyebrow,props.children);
 const context={React,useState:initial=>[initial,()=>{}],useEffect:()=>{},workflowStepCanRetry,workflowReviewGateRejected,validResearchClarification:()=>false,
  ResearchCard:card,ResearchDeliveryStatus:()=>null,Icon:()=>null,workflowStepLabels:{interrupted:'已中断',cancelled:'已取消',outcome_unknown:'结果未知'},
  exports};
 vm.createContext(context);vm.runInContext(ts.transpileModule(component.getText(parsed)+'\nexports.Cards=WorkflowInteractionCards;', {compilerOptions:{jsx:ts.JsxEmit.React}}).outputText,context);
 const step={id:'current',nodeId:'review',ordinal:1,nodeKind:'agent_stage',status:'interrupted'};
 const base={detail:{run:{id:'run',status:'interrupted',compilation:{nodes:[{id:'review',kind:'agent_stage',name:'独立审查'}]}},steps:[step]},retryStep:s=>calls.push(s.id)};
 const tree=context.exports.Cards(base),html=renderToStaticMarkup(tree);
 assert.match(html,/生成已中断，可手动恢复/);assert.match(html,/恢复当前阶段/);assert.match(html,/不会自动恢复/);assert.deepEqual(calls,[]);
 elements(tree,'button').find(n=>n.props.className==='primary').props.onClick();assert.deepEqual(calls,['current']);
 // A tool with unknown side effects retains its original explicit consent.
 const tool={...step,nodeKind:'tool',status:'outcome_unknown'};
 const toolTree=context.exports.Cards({...base,detail:{...base.detail,run:{...base.detail.run,compilation:{nodes:[{id:'review',kind:'tool',sideEffect:true}]}},steps:[tool]}});
 const toolHTML=renderToStaticMarkup(toolTree);assert.match(toolHTML,/确认该动作可能已经产生副作用/);assert.doesNotMatch(toolHTML,/生成已中断，可手动恢复/);
 assert.equal(elements(toolTree,'button').find(n=>n.props.className==='primary').props.disabled,true);
});
test('review submission failure keeps its explicit retry action without claiming scientific rejection',()=>{
 const parsed=ts.createSourceFile('App.tsx',appSource,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
 const component=parsed.statements.find(node=>ts.isFunctionDeclaration(node)&&node.name?.text==='WorkflowInteractionCards');
 assert.ok(component);
 const exports={},calls=[];
 const context={React,useState:initial=>[initial,()=>{}],useEffect:()=>{},workflowStepCanRetry,workflowReviewGateRejected,validResearchClarification:()=>false,
  ResearchCard:props=>React.createElement('section',null,props.eyebrow,props.children),ResearchDeliveryStatus:()=>null,Icon:()=>null,workflowStepLabels:{failed:'失败'},exports};
 vm.createContext(context);vm.runInContext(ts.transpileModule(component.getText(parsed)+'\nexports.Cards=WorkflowInteractionCards;', {compilerOptions:{jsx:ts.JsxEmit.React}}).outputText,context);
 const step={id:'review-current',nodeId:'independent_review',ordinal:12,nodeKind:'agent_stage',status:'failed',attempt:1,errorCode:'WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED',errorMessage:'这是审查提交错误，不代表报告已被判定不合格。'};
 const tree=context.exports.Cards({detail:{run:{id:'run',status:'failed',currentStep:12,compilation:{nodes:[{id:'independent_review',kind:'agent_stage',name:'独立审查'}]}},steps:[step]},retryStep:value=>calls.push(value.id)});
 const html=renderToStaticMarkup(tree);
 assert.match(html,/重试当前阶段/);assert.match(html,/不代表报告已被判定不合格/);assert.doesNotMatch(html,/报告需要修改|按建议修改|补充信息|type="checkbox"/);assert.deepEqual(calls,[]);
 const button=elements(tree,'button').find(node=>node.props.className==='primary');assert.equal(Boolean(button.props.disabled),false);button.props.onClick();assert.deepEqual(calls,['review-current']);
});

test('model protocol failures show technical recovery without automatic requests or scientific revision',()=>{
 const parsed=ts.createSourceFile('App.tsx',appSource,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
 const component=parsed.statements.find(node=>ts.isFunctionDeclaration(node)&&node.name?.text==='WorkflowInteractionCards');
 assert.ok(component);
 const exports={},calls=[];
 const context={React,useState:initial=>[initial,()=>{}],useEffect:()=>{},workflowStepCanRetry,workflowReviewGateRejected,validResearchClarification:()=>false,
  ResearchCard:props=>React.createElement('section',null,props.eyebrow,props.children),ResearchDeliveryStatus:()=>null,Icon:()=>null,workflowStepLabels:{failed:'失败'},exports};
 vm.createContext(context);vm.runInContext(ts.transpileModule(component.getText(parsed)+'\nexports.Cards=WorkflowInteractionCards;', {compilerOptions:{jsx:ts.JsxEmit.React}}).outputText,context);
 const step={id:'draft-current',nodeId:'report_drafting',ordinal:11,nodeKind:'agent_stage',status:'failed',attempt:2,errorCode:'MODEL_STREAM_INVALID',errorMessage:'无法解析模型响应。'};
 const recovery={stepId:step.id,attempt:2,kind:'retry_stage',failureKind:'model_protocol',label:'重试当前阶段',summary:'不代表科研审查不通过。保留仍有效的已完成阶段。不会自动恢复；点击后重新执行报告撰写，后续审查可能产生费用。',requiresConfirmation:false,callsModel:true};
 for (const projection of [recovery,undefined]) {
  const props={detail:{recovery:projection,run:{id:'run',status:'failed',currentStep:11,compilation:{nodes:[{id:'report_drafting',kind:'agent_stage',name:'报告撰写'}]}},steps:[step]},retryStep:value=>calls.push(value.id)};
  const tree=context.exports.Cards(props),html=renderToStaticMarkup(tree);
  assert.match(html,/模型响应协议异常，非科研审查结论/);assert.match(html,/重试当前阶段/);assert.match(html,/仍有效/);assert.match(html,/不会自动恢复/);assert.match(html,/后续/);assert.match(html,/费用/);
  assert.doesNotMatch(html,/报告需要修改|按建议修改|type="checkbox"/);
  const count=calls.length,buttons=elements(tree,'button');assert.equal(buttons.length,1);assert.equal(calls.length,count);
  buttons[0].props.onClick();assert.equal(calls.length,count+1);assert.equal(calls.at(-1),'draft-current');
  const busy=context.exports.Cards({...props,busy:'retry:draft-current'});assert.equal(elements(busy,'button')[0].props.disabled,true);
 }
 assert.deepEqual(calls,['draft-current','draft-current']);
});
test('technical gate failure offers one local retry; a business rejection restores original revision controls',()=>{
 const parsed=ts.createSourceFile('App.tsx',appSource,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
 const component=parsed.statements.find(node=>ts.isFunctionDeclaration(node)&&node.name?.text==='WorkflowInteractionCards');
 assert.ok(component);
 const exports={},calls=[];
 const context={React,useState:initial=>[initial,()=>{}],useEffect:()=>{},workflowStepCanRetry,workflowReviewGateRejected,validResearchClarification:()=>false,
  ResearchCard:props=>React.createElement('section',null,props.eyebrow,props.children),ResearchDeliveryStatus:()=>null,Icon:()=>null,workflowStepLabels:{failed:'失败'},
  workflowReviewIssues:()=>null,ResearchReviewFeedback:()=>React.createElement('p',null,'真实审查意见'),ResearchRevisionControls:props=>React.createElement('button',{onClick:()=>props.submit('evidence_screening',true,'review-sha',false)},'按建议修改'),exports};
 vm.createContext(context);vm.runInContext(ts.transpileModule(component.getText(parsed)+'\nexports.Cards=WorkflowInteractionCards;', {compilerOptions:{jsx:ts.JsxEmit.React}}).outputText,context);
 const step={id:'gate-current',nodeId:'gate',ordinal:13,nodeKind:'tool',status:'failed',attempt:3,errorCode:'WORKFLOW_INPUT_BINDING_FAILED',errorMessage:'snapshot error'};
 const props={detail:{run:{id:'run',status:'failed',currentStep:13,compilation:{nodes:[{id:'gate',kind:'tool',name:'交付核验',sideEffect:false,tool:{qualifiedName:'builtin.research.workflow.review.gate'}}]}},steps:[step]},retryStep:(...args)=>calls.push(args)};
 const tree=context.exports.Cards(props),html=renderToStaticMarkup(tree);
 assert.match(html,/交付核验遇到技术问题/);assert.match(html,/重试交付核验/);assert.match(html,/不重新调用模型进行审查/);
 assert.doesNotMatch(html,/报告需要修改|按建议修改|补充信息|type="checkbox"/);assert.deepEqual(calls,[]);
 const buttons=elements(tree,'button');assert.equal(buttons.length,1);assert.equal(Boolean(buttons[0].props.disabled),false);
 buttons[0].props.onClick();assert.deepEqual(calls,[[step]]);
 const busy=context.exports.Cards({...props,busy:'retry:gate-current'});assert.equal(elements(busy,'button')[0].props.disabled,true);
 const rejected={...step,errorCode:'RESEARCH_REVIEW_NOT_APPROVED',errorMessage:'独立审查未通过：仍有缺项'};
 const next=context.exports.Cards({...props,detail:{...props.detail,steps:[rejected]}}),nextHtml=renderToStaticMarkup(next);
 assert.match(nextHtml,/报告需要修改/);assert.match(nextHtml,/按建议修改/);assert.doesNotMatch(nextHtml,/重试交付核验|交付核验遇到技术问题/);
 const controls=elements(next,context.ResearchRevisionControls)[0];controls.props.submit('evidence_screening',true,'review-sha',false);
 assert.deepEqual(calls[1],[rejected,'evidence_screening',true,'review-sha',false]);
});

test('cancelled recovery excludes downstream stages, tools, stale or inconsistent state',()=>{
 const step={id:'current',nodeId:'implementation',ordinal:4,nodeKind:'agent_stage',status:'cancelled',attempt:1};
 const run={status:'cancelled',currentStep:4,compilation:{nodes:[{id:'implementation',kind:'agent_stage'}]}};
 assert.equal(workflowStepCanRetry(run,step),true);
 assert.equal(workflowStepCanRetry({...run,compilation:{nodes:[{id:'implementation',kind:'ai_analysis'}]}},{...step,nodeKind:'ai_analysis'}),true);
 for(const changed of [{ordinal:5},{ordinal:3},{nodeKind:'tool'},{nodeKind:'python'},{status:'running'},{status:'failed'},{nodeId:'old-node'}]) assert.equal(workflowStepCanRetry(run,{...step,...changed}),false,JSON.stringify(changed));
 for(const status of ['running','completed','paused','failed','interrupted']) assert.equal(workflowStepCanRetry({...run,status},step),false,status);
 assert.equal(workflowStepCanRetry({...run,compilation:{nodes:[]}},step),false);
 assert.equal(workflowStepCanRetry({...run,compilation:{nodes:[{id:step.nodeId,kind:'tool'}]}},step),false);
});
test('failed and interrupted recovery still supports tools and uncertain outcomes',()=>{
 const run={status:'failed',currentStep:4,compilation:{nodes:[]}};
 const step={status:'failed',ordinal:4,nodeId:'compute',nodeKind:'tool'};
 for(const status of ['failed','interrupted']) for(const outcome of ['failed','interrupted','outcome_unknown']) assert.equal(workflowStepCanRetry({...run,status},{...step,status:outcome}),true);
 assert.equal(workflowStepCanRetry(run,{...step,status:'completed'}),false);
 assert.equal(workflowStepCanRetry(run,{...step,status:'cancelled'}),false);
});
test('recommendation explains affected stages and costs, manual choices stay collapsed',()=>{
 const html=render();assert.match(html,/建议从「分析方法实现」/);assert.match(html,/执行计算/);assert.match(html,/模型调用可能消耗额度/);assert.match(html,/为什么这样建议/);assert.match(html,/调整修改范围/);assert.doesNotMatch(html,/<select/);assert.match(html,/按建议修改/);
 assert.match(html,/本次会重新计算或执行相关操作/);assert.match(html,/点击“按建议修改”即确认执行上述范围/);
 assert.match(html,/起点之前的结果沿用/);assert.match(html,/尚未开始的后续阶段继续执行/);
});
test('one explicit revision click authorizes its displayed side effects; no checkbox or automatic submission',()=>{
 assert.doesNotMatch(render(),/<button[^>]*class="primary"[^>]*disabled|type="checkbox"|承担额外/);
 assert.match(render({busy:true}),/<button[^>]*class="primary"[^>]*disabled/);
 const h=harness();let tree=h.render();assert.deepEqual(h.calls,[]);primary(tree).props.onClick();
 assert.deepEqual(h.calls,[[target.nodeId,true,'current-review',true]]);
 tree=h.render({busy:true});primary(tree).props.onClick();assert.equal(h.calls.length,1);
});
test('manual scope is displayed before execution and consent follows the selected scope, not the original recommendation',()=>{
 const late={nodeId:'report_drafting',label:'报告撰写',repeatsSideEffects:false};
 const stages=[{nodeId:'prepare',label:'已读资料'},{nodeId:target.nodeId,label:target.label},{nodeId:'python_analysis',label:'执行计算'},{nodeId:late.nodeId,label:late.label},{nodeId:'review',label:'二次审查'},{nodeId:'publish',label:'登记产物'}];
 const h=harness({targets:[target,late],stages});
 elements(h.render(),'button').find(n=>n.props.className==='research-revision-adjust').props.onClick();
 let tree=h.render();assert.equal(primary(tree).props.disabled,true);assert.deepEqual(h.calls,[]);
 elements(tree,'select')[0].props.onChange({target:{value:target.nodeId}});tree=h.render();
 const scope=elements(tree,'ol').find(n=>n.props.className==='research-revision-stage-list');
 assert.deepEqual(elements(scope,'li').map(n=>n.props.children),stages.slice(1).map(s=>s.label));
 primary(tree).props.onClick();assert.deepEqual(h.calls[0],[target.nodeId,false,'current-review',true]);
 elements(tree,'select')[0].props.onChange({target:{value:late.nodeId}});tree=h.render();
 assert.deepEqual(elements(elements(tree,'ol').find(n=>n.props.className==='research-revision-stage-list'),'li').map(n=>n.props.children),stages.slice(3).map(s=>s.label));
 assert.match(renderToStaticMarkup(tree),/从「报告撰写」开始修改/);assert.doesNotMatch(renderToStaticMarkup(tree),/本次会重新计算/);
 primary(tree).props.onClick();assert.deepEqual(h.calls[1],[late.nodeId,false,'current-review',false]);
 elements(tree,'button').find(n=>n.props.className==='research-revision-adjust').props.onClick();tree=h.render();
 primary(tree).props.onClick();assert.deepEqual(h.calls[2],[target.nodeId,true,'current-review',true]);
});
test('legacy and input-blocked reviews never fall back to modifying the report',()=>{
 for(const value of [undefined,{status:'unavailable',summary:'旧审查无建议',repeatsSideEffects:false}]) {
  const html=render({recommendation:value});assert.match(html,/<button[^>]*class="primary"[^>]*disabled/);assert.doesNotMatch(html,/只修改报告内容（默认）/);
 }
});
test('genuinely missing input stays visible, without another plan button or copy step',()=>{
 const html=render({recommendation:{status:'needs_input',summary:'先补充资料',requiredInputs:['核查 bmj.full.pdf 是否进入索引','确认研究人群'],repeatsSideEffects:false}});
 assert.match(html,/核查 bmj.full.pdf 是否进入索引/);assert.match(html,/确认研究人群/);
 assert.match(html,/同一次审查/);assert.match(html,/上传并发送/);assert.match(html,/仍需补充/);
 assert.doesNotMatch(html,/<button|<select|让 AI 核对并继续|让 AI 整理返修方案|复制补充要求|先讨论完整返修方案/);
 assert.doesNotMatch(source,/navigator.clipboard|onDiscuss|discussRepair|copyRequirements/);
});
test('legacy adoption repair has exactly one direct execution action and preserves other missing inputs',()=>{
 const props={targets:[{nodeId:'evidence_extraction',label:'引用选择',repeatsSideEffects:false}],reselection:{nodeId:'evidence_extraction',availableCount:64,recommendedCount:29,selectedRecommendedCount:15,missingRecommendedCount:14,previouslyUnofferedCount:14,reviewOutputSha256:'review'},recommendation:{status:'needs_input',summary:'confirm',requiredInputs:['仍需原始数据'],reviewOutputSha256:'review',repeatsSideEffects:false}};
 const html=render(props);assert.match(html,/打开引用补选/);assert.match(html,/此按钮只打开引用补选/);assert.match(html,/14 条没有出现在旧选择列表/);assert.match(html,/程序遗漏，不是你主动排除/);assert.match(html,/不重跑导入、核读或综合/);assert.match(html,/不保证其他科学缺口已解决/);assert.match(html,/仍需原始数据/);
 assert.equal((html.match(/class="primary"/g)||[]).length,1);
 assert.doesNotMatch(html,/重新选择已有证据<|让 AI 整理返修方案|复制补充要求/);
 assert.match(render({...props,busy:true}),/<button[^>]*class="primary"[^>]*disabled/);
 assert.doesNotMatch(render({...props,reselection:{...props.reselection,reviewOutputSha256:''}}),/<button/);
 assert.doesNotMatch(render({...props,reselection:{...props.reselection,reviewOutputSha256:'stale'}}),/<button/);
 // Unchosen citations by themselves do not turn genuine missing input into
 // a default executable repair; only a verified old-list omission gets it.
 assert.doesNotMatch(render({...props,reselection:{...props.reselection,previouslyUnofferedCount:0}}),/<button/);
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
test('new review identity resets prior selection; consent is not kept in component state',()=>{
 assert.match(source,/recommendation\?\.reviewOutputSha256/);assert.match(source,/setManual\(false\); setSelected\(''\)/);
 assert.doesNotMatch(source,/setConfirmed|confirmed: boolean/);
});

test('mixed review keeps AI-owned corrections visible without authorizing a replay',()=>{
 const html=render({recommendation:{status:'needs_input',summary:'核对已有数据',requiredInputs:['确认测量选择'],repairableCorrections:['交付真实结果和脚本'],pendingStartLabel:'分析方法实现',pendingAffectedStages:['分析方法实现','执行计算'],reviewOutputSha256:'review',repeatsSideEffects:false}});
 assert.match(html,/已有修正建议/);assert.match(html,/交付真实结果和脚本/);
 assert.match(html,/已定位的候选起点：分析方法实现/);assert.match(html,/当前不会执行/);
 assert.match(html,/仍需补充/);assert.match(html,/确认测量选择/);
 assert.doesNotMatch(html,/<button|<select|让 AI 核对并继续|让 AI 整理返修方案/);
});

test('pending attachment drafts do not disable direct repair, actual question waits do',async()=>{
 const app=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
 // Attachment drafts still delay candidate auto-navigation; only actual
 // conversation work/imports/question cards disable terminal repair actions.
 assert.match(app,/conversationBusy=\{busy \|\| sending \|\| importingAttachments \|\| pendingAttachments.length > 0\}/);
 assert.match(app,/discussionBusy=\{busy \|\| sending \|\| importingAttachments \|\| Boolean\(pendingQuestionKey\)\}/);
 assert.match(app,/<WorkflowInteractionCards detail=\{runDetail\} conversationBusy=\{discussionBusy\}/);
});

test('the App forwards click consent once to Retry, while ordinary tool retries retain their existing confirmation',async()=>{
 const start=appSource.indexOf('  async function retryStep('),end=appSource.indexOf('  async function confirmResearchRevision(',start);
 assert.ok(start>=0 && end>start);
 const script=ts.transpileModule(appSource.slice(start,end)+'\nglobalThis.retry=retryStep;',{}).outputText;
 const fixture=(options={})=>{
  let resolve;const pending=new Promise(value=>{resolve=value});
  const gate={id:'gate-step',nodeId:'gate'};
  const detail={run:{id:'run',compilation:{nodes:[{id:'gate',tool:{qualifiedName:'builtin.research.workflow.review.gate'}},{id:'python_analysis',sideEffect:true}]}},reviewRevisionTargets:[target],reviewRevisionRecommendation:recommendation};
  const calls=[],feedback=[];
  const context={runDetail:detail,busy:'',mutationRef:{current:''},confirmRetry:false,project:{id:'project'},runRequestRef:{current:0},setBusy:()=>{},setFeedback:v=>feedback.push(v),backend:(...args)=>{calls.push(args);return pending},normalizeWorkflowRunDetail:v=>v,setRunDetail:()=>{},setConfirmRetry:()=>{},loadProjectRuns:async()=>{},loadRun:async()=>{},errorText:String,...options};
  vm.createContext(context);vm.runInContext(script,context);
  return {context,gate,calls,feedback,finish:()=>resolve(detail)};
 };
 const f=fixture();assert.deepEqual(f.calls,[]);
 await f.context.retry(f.gate,target.nodeId,true,'current-review');assert.equal(f.calls.length,0);
 const work=f.context.retry(f.gate,target.nodeId,true,'current-review',true);
 await f.context.retry(f.gate,target.nodeId,true,'current-review',true);assert.equal(f.calls.length,1);
 assert.equal(f.calls[0][0],'WorkflowFacade');assert.equal(f.calls[0][1],'Retry');
 assert.equal(f.calls[0][2].confirmSideEffect,true);assert.equal(f.calls[0][2].expectedReviewSha256,'current-review');
 assert.match(f.calls[0][2].note,/用户点击返修按钮/);f.finish();await work;assert.equal(f.context.mutationRef.current,'');
 // A leftover ordinary-retry checkbox is never consent for a revision click.
 const leftover=fixture({confirmRetry:true});await leftover.context.retry(leftover.gate,target.nodeId,true,'current-review');assert.equal(leftover.calls.length,0);
 const ordinary=fixture();await ordinary.context.retry({id:'python-step',nodeId:'python_analysis'},'',false,'',true);assert.equal(ordinary.calls.length,0);
 const allowed=fixture({confirmRetry:true});const ordinaryWork=allowed.context.retry({id:'python-step',nodeId:'python_analysis'});assert.equal(allowed.calls[0][2].confirmSideEffect,true);allowed.finish();await ordinaryWork;
 const reportOnly=fixture({runDetail:{run:{id:'run',compilation:{nodes:[{id:'gate',tool:{qualifiedName:'builtin.research.workflow.review.gate'}}]}},reviewRevisionTargets:[{nodeId:'report_drafting',repeatsSideEffects:false}],reviewRevisionRecommendation:{...recommendation,repeatsSideEffects:false}}});
 const reportWork=reportOnly.context.retry(reportOnly.gate,'report_drafting',true,'current-review',false);assert.equal(reportOnly.calls[0][2].confirmSideEffect,false);reportOnly.finish();await reportWork;
});


test('host recovery action controls publication label, scope and current attempt',()=>{
 const parsed=ts.createSourceFile('App.tsx',appSource,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
 const component=parsed.statements.find(node=>ts.isFunctionDeclaration(node)&&node.name?.text==='WorkflowInteractionCards');
 const exports={},calls=[];
 const context={React,useState:initial=>[initial,()=>{}],useEffect:()=>{},workflowStepCanRetry,workflowReviewGateRejected,validResearchClarification:()=>false,
  ResearchCard:props=>React.createElement('section',null,props.eyebrow,props.children),ResearchDeliveryStatus:()=>null,Icon:()=>null,workflowStepLabels:{failed:'失败'},exports};
 vm.createContext(context);vm.runInContext(ts.transpileModule(component.getText(parsed)+'\nexports.Cards=WorkflowInteractionCards;', {compilerOptions:{jsx:ts.JsxEmit.React}}).outputText,context);
 const step={id:'publish',nodeId:'publish-domain-result',ordinal:8,nodeKind:'tool',status:'failed',attempt:2,errorCode:'TOOL_INVOCATION_FAILED',errorMessage:'保存失败'};
 const detail={run:{id:'run',status:'failed',currentStep:8,compilation:{nodes:[{id:step.nodeId,kind:'tool',name:'发布',sideEffect:false,tool:{qualifiedName:'builtin.research.workflow.report'}}]}},steps:[step],recovery:{stepId:step.id,attempt:2,kind:'retry_publication',label:'重试发布报告',summary:'只恢复报告发布及导出，不重新调用模型审查。',requiresConfirmation:false,callsModel:false}};
 const tree=context.exports.Cards({detail,retryStep:s=>calls.push(s.id)}),html=renderToStaticMarkup(tree);
 assert.match(html,/报告发布遇到技术问题/);assert.match(html,/重试发布报告/);assert.match(html,/不重新调用模型审查/);
 assert.doesNotMatch(html,/按建议修改|补充信息/);assert.deepEqual(calls,[]);
 primary(tree).props.onClick();assert.deepEqual(calls,['publish']);
 assert.equal(workflowStepCanRetry(detail.run,step,{...detail.recovery,attempt:1}),false);
 assert.equal(workflowStepCanRetry(detail.run,step,{...detail.recovery,stepId:'another'}),false);
 assert.equal(workflowStepCanRetry(detail.run,step,null),false);
 const empty=context.exports.Cards({detail:{...detail,recovery:null},retryStep:()=>{throw new Error('unexpected replay')}});
 assert.equal(elements(empty,'button').length,0);
});
