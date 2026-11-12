import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {orderTimelineDialogue} from './researchTimelineState.js';
const source=await readFile(new URL('./UserInputCard.tsx',import.meta.url),'utf8');
const compiled=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.CommonJS,jsx:ts.JsxEmit.ReactJSX,target:ts.ScriptTarget.ES2022}}).outputText;
const actualRequire=createRequire(import.meta.url),exports={};let slots=[],refs=[],cursor=0,refCursor=0;
const card=({title,status,children,footer,...props})=>React.createElement('section',props,React.createElement('header',null,title,status),children,React.createElement('footer',null,footer));
vm.runInNewContext(compiled,{exports,require:name=>name==='./ResearchCard'?{ResearchCard:card}:name==='react'?{...actualRequire(name),useState:initial=>{const i=cursor++;if(!(i in slots))slots[i]=initial;return [slots[i],value=>{slots[i]=typeof value==='function'?value(slots[i]):value}];},useRef:initial=>refs[refCursor++]??(refs[refCursor-1]={current:initial}),useId:()=>':fixture:',useEffect:()=>{},useLayoutEffect:()=>{}}:actualRequire(name)});
const question={id:'metrics',question:'想分析哪些指标？',required:true,selectionMode:'multiple',options:[{id:'height',label:'株高',description:'描述整体生长'},{id:'mass',label:'干重'}]};
const plain=value=>JSON.parse(JSON.stringify(value));
function elements(node,type){if(!node||typeof node!=='object')return [];return [...(node.type===type?[node]:[]),...React.Children.toArray(node.props?.children).flatMap(child=>elements(child,type))];}
function harness(questions=[question]){let answers={},saved=null;slots=[];refs=[];return {render:(props={})=>{cursor=0;refCursor=0;const component=exports.UserInputCard({questions,answers,setAnswers:next=>{answers=next},submit:next=>{saved=next},...props});return component?card(component.props):null;},values:()=>({answers,saved}),finishSlide:()=>{slots[1]=null;refs[1].current=false;}};}
const button=(tree,name)=>elements(tree,'button').find(n=>n.props.children===name);
const current=tree=>elements(tree,'div').find(n=>n.props.className==='user-input-panel current');
test('choice and text are drafts; lower-right Confirm commits',()=>{
 const h=harness();assert.equal(elements(h.render(),'input').every(n=>!n.props.checked),true);
 elements(h.render(),'input')[0].props.onChange();elements(h.render(),'input')[1].props.onChange();elements(h.render(),'textarea')[0].props.onChange({target:{value:'只分析近年'}});
 assert.equal(h.values().saved,null);button(h.render(),'确定').props.onClick();assert.deepEqual(plain(h.values().saved),{metrics:{optionIds:['height','mass'],text:'只分析近年'}});
});
test('free text replaces required choice; single selection does not stack',()=>{
 const h=harness([{...question,selectionMode:'single'}]);assert.equal(button(h.render(),'确定').props.disabled,true);
 elements(h.render(),'textarea')[0].props.onChange({target:{value:'用叶片数'}});assert.equal(button(h.render(),'确定').props.disabled,false);
 elements(h.render(),'input')[0].props.onChange();elements(h.render(),'input')[1].props.onChange();assert.deepEqual(plain(h.values().answers.metrics.optionIds),['mass']);
});
test('Confirm slides locally, leaving panel is inert and rapid clicks cannot skip',()=>{
 const h=harness([question,{id:'goal',question:'你的目标？',required:true},{id:'scope',question:'你的范围？',required:true}]);elements(h.render(),'input')[0].props.onChange();
 const confirm=button(h.render(),'确定');confirm.props.onClick();confirm.props.onClick();let tree=h.render();assert.equal(h.values().saved,null);
 assert.equal(elements(current(tree),'h3')[0].props.children,'你的目标？');assert.equal(elements(tree,'div').some(n=>n.props.className==='user-input-slider sliding-forward'),true);
 const leaving=elements(tree,'div').find(n=>n.props.className==='user-input-panel leaving');assert.equal(leaving.props['aria-hidden'],true);assert.equal(leaving.props.inert,'');assert.equal(elements(leaving,'input').every(n=>n.props.disabled),true);assert.equal(button(tree,'确定').props.disabled,true);
 h.finishSlide();assert.equal(button(h.render(),'确定').props.disabled,true);elements(h.render(),'textarea')[0].props.onChange({target:{value:'可复现报告'}});button(h.render(),'确定').props.onClick();h.finishSlide();
 elements(h.render(),'textarea')[0].props.onChange({target:{value:'只看成人'}});button(h.render(),'确定').props.onClick();assert.equal(h.values().saved.goal.text,'可复现报告');assert.equal(h.values().saved.scope.text,'只看成人');
});
test('Previous reverses slide, retaining answers and current index',()=>{
 const h=harness([question,{id:'goal',question:'你的目标？',required:true}]),indexes=[];elements(h.render(),'input')[0].props.onChange();button(h.render({onIndexChange:i=>indexes.push(i)}),'确定').props.onClick();h.finishSlide();
 elements(h.render(),'textarea')[0].props.onChange({target:{value:'报告'}});button(h.render({onIndexChange:i=>indexes.push(i)}),'上一题').props.onClick();const tree=h.render();
 assert.equal(elements(tree,'div').some(n=>n.props.className==='user-input-slider sliding-backward'),true);assert.equal(elements(current(tree),'input')[0].props.checked,true);assert.deepEqual(indexes,[1,0]);assert.equal(h.values().saved,null);
});
test('index clamps, optional answers and invalid-plan gating remain',()=>{
 const h=harness([{id:'optional',question:'可以稍后补充',required:false}]);assert.equal(button(h.render({initialIndex:99,disabled:true}),'确定').props.disabled,true);assert.equal(elements(h.render({disabled:true}),'textarea')[0].props.disabled,true);
 assert.equal(button(h.render(),'确定').props.disabled,false);button(h.render(),'确定').props.onClick();assert.deepEqual(plain(h.values().saved),{});assert.equal(harness([]).render(),null);
});
test('long content stays inline; busy locks edits, retry error stays visible, no modal',()=>{
 const text='很长的问题'.repeat(500),h=harness([{...question,question:text}]),tree=h.render({busy:true,error:'提交失败，请重试'});
 assert.equal(elements(tree,'h3')[0].props.children,text);assert.equal(elements(tree,'textarea')[0].props.disabled,true);assert.equal(button(tree,'正在提交…').props.disabled,true);assert.match(renderToStaticMarkup(elements(tree,'p').find(n=>n.props.role==='alert')),/提交失败，请重试/);
 assert.doesNotMatch(source,/ModalBackdrop|createPortal|aria-modal|role="dialog"/);assert.match(source,/ResearchCard/);
});
test('slide directions, stable panel keys, height and reduced-motion transitions exist',async()=>{
 const css=await readFile(new URL('./styles.css',import.meta.url),'utf8');assert.match(source,/key=\{outgoing \? "outgoing" : "current"\}/);assert.match(source,/ResizeObserver/);
 assert.match(css,/sliding-forward \.current/);assert.match(css,/sliding-forward \.leaving/);assert.match(css,/sliding-backward \.current/);assert.match(css,/transition: height 280ms/);assert.match(css,/prefers-reduced-motion: reduce/);
});
test('same-turn answers precede reserved assistant without changing cursor order',()=>{
 const entry=(id,seq,run,role)=>({id,sequence:String(seq),message:{id,runId:run,role}}),input=[entry('u',1,'r','user'),entry('a',2,'r','assistant'),entry('answer1',3,'r','user'),entry('answer2',4,'r','user'),entry('next-u',5,'next','user')];
 assert.deepEqual(orderTimelineDialogue(input).map(e=>e.id),['u','answer1','answer2','a','next-u']);assert.deepEqual(input.map(e=>e.sequence),['1','2','3','4','5']);
});
test('chat and research share trailing card, keyed drafts and original response adapters',async()=>{
 const app=await readFile(new URL('./App.tsx',import.meta.url),'utf8'),timeline=await readFile(new URL('./ResearchTaskTimeline.tsx',import.meta.url),'utf8');
 assert.match(app,/trailing=\{userInputCard\}/);assert.match(app,/\.map\(renderMessage\)\}\{userInputCard\}/);assert.match(timeline,/\{trailing\}/);assert.match(app,/!userInputCard && messages.length === 0/);
 assert.match(app,/key=\{pendingQuestionKey\}/);assert.match(app,/userInputDraftsRef.current.get\(pendingQuestionKey\)/);assert.match(app,/userInputIndexesRef.current.get\(pendingQuestionKey\)/);assert.match(app,/userInputSubmitRef.current.set\(questionKey, request.runId\)/);
 assert.match(app,/JSON.stringify\(clarification \?\? null\)/);assert.match(app,/disabled=\{starterPlanChecking \|\| Boolean\(starterPlanError\)\}/);assert.match(app,/freeTextAnswers/);assert.match(app,/"ChatFacade", "RespondToUserInput"/);assert.match(app,/setUserInputRequest\(null\)/);assert.match(app,/"WorkflowFacade", "ReplanResearchStarter"/);assert.doesNotMatch(app,/UserInputDialog|userInputOpen|clarificationOpen/);
});
