import assert from "node:assert/strict";
import test from "node:test";
import {readFile} from "node:fs/promises";
import vm from "node:vm";
import ts from "typescript";
import React from "react";
import {mergeResearchTimeline,compareRunCreatedAt} from "./researchTimelineState.js";
const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
const pager = await readFile(new URL("./ResearchTaskTimeline.tsx", import.meta.url), "utf8");
const row = (id, sequence, extra={}) => ({id, sequence:String(sequence), ...extra});

test("saved full text explains local refresh without failure or retry controls", () => {
  const history=source.slice(source.indexOf("const researchTimelineLabels:"),source.indexOf("function WorkflowResearchDesignCard("));
  const exports={};
  vm.runInNewContext(ts.transpileModule(history+"\nexports.render=ResearchTimelineHistory;",{compilerOptions:{jsx:ts.JsxEmit.React,target:ts.ScriptTarget.ES2022}}).outputText,{exports,React});
  const element=exports.render({entry:{kind:"event",eventType:"workflow.evidence_refreshed",snapshot:{}}});
  assert.equal(element.props["aria-label"],"补充全文已保存");
  assert.match(element.props.children.props.children,/不会重新检索或初筛/);
});

test("snapshot ordering preserves nanoseconds and variable precision fractions", () => {
  assert.equal(compareRunCreatedAt("2026-09-10T12:00:00.1Z","2026-09-10T12:00:00.11Z"),-1);
  assert.equal(compareRunCreatedAt("2026-09-10T12:00:00.123400Z","2026-09-10T12:00:00.123100Z"),1);
  assert.equal(compareRunCreatedAt("2026-09-10T12:00:00Z","2026-09-10T12:00:00.000Z"),0);
});

test("database order remains exact beyond JS safe integers and within a millisecond", () => {
  const values=[row("question","9007199254740994",{createdAt:"2026-09-10T12:00:00.123400Z"}),row("report","9007199254740993",{createdAt:"2026-09-10T12:00:00.123100Z"})];
  assert.deepEqual(mergeResearchTimeline([],values).map(e=>e.id),["report","question"]);
});
test("pages deduplicate without deleting previously loaded history", () => {
  const current=[row("plan",1),row("report",2),row("answer",4,{status:"streaming"})];
  const next=mergeResearchTimeline(current,[row("question",3),row("answer",4,{status:"complete"}),row("proposal",5)]);
  assert.deepEqual(next.map(e=>e.id),["plan","report","question","answer","proposal"]);
  assert.equal(next[3].status,"complete");
  assert.equal(current[2].status,"streaming");
});
test("returning to an earlier stage appends a new attempt after the failure", () => {
  const next=mergeResearchTimeline([row("python-failed",8),row("old-plan",1)],[row("method-revision",10),row("retry-confirmed",9)]);
  assert.deepEqual(next.map(e=>e.id),["old-plan","python-failed","retry-confirmed","method-revision"]);
});
test("new delivery never replaces an older delivery or intervening discussion", () => {
  const next=mergeResearchTimeline([row("delivery-1",1,{snapshot:{text:"old"}}),row("question",2),row("proposal",3)],[row("delivery-2",5),row("revision",4)]);
  assert.deepEqual(next.map(e=>e.id),["delivery-1","question","proposal","revision","delivery-2"]);
  assert.equal(next[0].snapshot.text,"old");
});
test("all research content uses a single task-owned renderer", () => {
  assert.match(source, /<ResearchTaskTimeline key=/);
  assert.match(source, /entry.conversationId === conversationId/);
  assert.doesNotMatch(source,/research-interaction-stream|interactionHost|workflowConversationEntries|workflowConversationCards|function workflowTimeline\(/);
  assert.match(pager,/visibleEntries.map\(\(entry, index\) => <TimelineWindow id=\{entry.id\} key=\{entry.id\}/);
});
test("paging has scope disposal, single-flight, forward catchup and reading anchor", () => {
  assert.match(pager,/current.inFlight \|\| current.disposed/);
  assert.match(pager,/if \(current.disposed\) return/);
  assert.match(pager,/after: last/);
  assert.match(pager,/known.has\(entry.id\)/);
  assert.match(pager,/before: current.entries\[0\]\?\.sequence/);
  assert.match(pager,/saved.element.getBoundingClientRect\(\).top - saved.top/);
});
test("historical cards have no mutation controls and do not claim current approval", () => {
  const history=source.slice(source.indexOf("function ResearchTimelineHistory("),source.indexOf("function WorkflowResearchDesignCard("));
  assert.doesNotMatch(history,/saveDeliverable|confirmRevision|adoptResearchRoute|onClick|已通过独立审查/);
  assert.match(history,/snapshot.outputs/);
  assert.match(history,/entry.step.input/);
});
test("failure and retry history are quiet without dropping stored entries or recovery controls", () => {
  const history=source.slice(source.indexOf("function ResearchTimelineHistory("),source.indexOf("function WorkflowResearchDesignCard("));
  const exports={};
  vm.runInNewContext(ts.transpileModule(history+"\nexports.render=ResearchTimelineHistory;",{compilerOptions:{jsx:ts.JsxEmit.React,target:ts.ScriptTarget.ES2022}}).outputText,{exports});
  const rows=[row("failure",1,{kind:"event",eventType:"workflow.failed"}),row("retry",2,{kind:"event",eventType:"workflow.step_retry_queued"})];
  for (const entry of rows) assert.equal(exports.render({entry}),null);
  assert.equal(mergeResearchTimeline([],rows).length,2);
  const renderer=source.slice(source.indexOf("  renderTimelineRef.current ="),source.indexOf("  const timelineTaskId ="));
  assert.ok(renderer.indexOf("return interaction") < renderer.lastIndexOf("return <ResearchTimelineHistory"));
  assert.match(source,/重试当前阶段/);
});
test("initial start notices are hidden while clarification and replan history remain visible", () => {
  const history=source.slice(source.indexOf("const researchTimelineLabels:"),source.indexOf("function WorkflowResearchDesignCard("));
  const exports={};
  vm.runInNewContext(ts.transpileModule(history+"\nexports.render=ResearchTimelineHistory;",{compilerOptions:{jsx:ts.JsxEmit.React,target:ts.ScriptTarget.ES2022}}).outputText,{exports,React});
  const render=inputs=>exports.render({entry:{kind:"event",eventType:"workflow.created",createdAt:"2026-09-11T01:00:00Z",snapshot:{inputs}}});
  assert.equal(render({}),null);
  assert.equal(render({starter_context:{researchIdea:"Initial research"}}),null);
  assert.equal(render({research_goal:"Adopted route",route_context:{routeId:"data"}}),null);
  assert.equal(render({starter_context:{priorStarterRunId:"prior"}}).props["aria-label"],"重新规划研究路线");
  assert.equal(render({starter_context:{priorStarterRunId:"prior",clarificationAnswers:{scope:["selected"]}}}).props["aria-label"],"已确认研究需求，继续规划");
});
test("tools retain their work title without route step numbers", () => {
  const renderer=source.slice(source.indexOf("function WorkflowTimelineToolCard("),source.indexOf("function observeChatAutoFollow("));
  assert.match(renderer,/UnifiedToolActivityCard/);
  assert.match(renderer,/item.label/);
  assert.doesNotMatch(renderer,/ordinal|第.*步/);
});
test("adopted upload checkpoint restores without creating another route or run", () => {
  const open=source.slice(source.indexOf("async function openProjectRun("),source.indexOf("const startResearchDiscussion ="));
  assert.match(open,/GetPendingResearchAdoption/);
  assert.match(open,/setPendingAdoptedRoute/);
  assert.doesNotMatch(open,/"AdoptResearchRoute"|"StartResearch"/);
});
function autoFollowHarness() {
  const callbacks = new Map();
  let changed, follows = true, disconnected = false, sequence = 0;
  const scrolls = [];
  const listeners = new Map();
  const chat = {scrollHeight:1200, clientHeight:400, scrollTop:800, scrollTo:value=>{scrolls.push(value.top);chat.scrollTop=Math.max(0,value.top-chat.clientHeight)}, addEventListener:(name,fn)=>listeners.set(name,fn),removeEventListener:name=>listeners.delete(name)};
  const exports = {};
  const observerCode = source.slice(source.indexOf("function observeChatAutoFollow("),source.indexOf("function workflowMessageActivities("));
  class Element {constructor(editing=false){this.editing=editing}closest(){return this.editing ? this : null}}
  vm.runInNewContext(ts.transpileModule(observerCode+"\nexports.observeChatAutoFollow=observeChatAutoFollow;",{}).outputText, {exports, Element, MutationObserver:class {constructor(callback){changed=callback}observe(){}disconnect(){disconnected=true}}, window:{requestAnimationFrame:fn=>{callbacks.set(++sequence,fn);return sequence},cancelAnimationFrame:id=>callbacks.delete(id)}});
  const flush = () => {const queued=[...callbacks.values()];callbacks.clear();queued.forEach(fn=>fn())};
  const stop=exports.observeChatAutoFollow(chat,()=>follows,value=>follows=value);
  return {chat,callbacks,scrolls,listeners,flush,stop,Element,changed:()=>changed(),setFollow:value=>follows=value,following:()=>follows,disconnected:()=>disconnected};
}

test("late card and streaming content follow only while the reader remains at the bottom", () => {
  const h=autoFollowHarness();
  h.changed();h.changed();assert.equal(h.callbacks.size,1);h.flush();assert.deepEqual(h.scrolls,[1200]);
  h.setFollow(false);h.chat.scrollHeight=1500;h.changed();h.flush();assert.deepEqual(h.scrolls,[1200]);
  h.setFollow(true);h.changed();h.setFollow(false);h.flush();assert.deepEqual(h.scrolls,[1200]);
  h.setFollow(true);h.listeners.get("load")();h.flush();assert.deepEqual(h.scrolls,[1200,1500]);
  h.changed();h.stop();assert.equal(h.callbacks.size,0);assert.equal(h.disconnected(),true);assert.equal(h.listeners.size,0);
});

test("one tiny upward wheel cancels even an already queued follow without consuming the event", () => {
  const h=autoFollowHarness();let consumed=false;
  h.changed();assert.equal(h.callbacks.size,1);
  h.listeners.get("wheel")({deltaY:-1,ctrlKey:false,preventDefault:()=>consumed=true});
  assert.equal(h.following(),false);assert.equal(h.callbacks.size,0);
  h.chat.scrollTop=799;h.listeners.get("scroll")();h.changed();h.flush();
  assert.deepEqual(h.scrolls,[]);assert.equal(consumed,false);
  h.listeners.get("wheel")({deltaY:1});h.chat.scrollTop=800;h.listeners.get("scroll")();
  assert.equal(h.following(),true);h.changed();h.flush();assert.deepEqual(h.scrolls,[1200]);h.stop();
});

test("scrollbar reading does not re-enable follow inside the old 96px band", () => {
  const h=autoFollowHarness();h.chat.scrollTop=799;h.listeners.get("scroll")();
  assert.equal(h.following(),false);
  h.chat.scrollTop=780;h.listeners.get("scroll")();h.chat.scrollTop=797;h.listeners.get("scroll")();
  h.changed();h.flush();assert.equal(h.following(),false);assert.deepEqual(h.scrolls,[]);
  h.chat.scrollTop=800;h.listeners.get("scroll")();assert.equal(h.following(),true);h.stop();
});

test("layout-only scroll events cannot undo a reader pause; explicit send can restore follow", () => {
  const h=autoFollowHarness();h.listeners.get("wheel")({deltaY:-40});
  h.listeners.get("scroll")();assert.equal(h.following(),false);
  h.listeners.get("load")();h.changed();h.flush();assert.deepEqual(h.scrolls,[]);
  h.setFollow(true);h.chat.scrollHeight=1600;h.changed();h.flush();assert.deepEqual(h.scrolls,[1600]);h.stop();
});

test("keyboard and touch reading pause follow while text editing and ctrl zoom do not", () => {
  const h=autoFollowHarness();
  h.listeners.get("wheel")({deltaY:-30,ctrlKey:true});assert.equal(h.following(),true);
  h.listeners.get("keydown")({key:"ArrowUp",target:new h.Element(true)});assert.equal(h.following(),true);
  for (const key of ["ArrowUp","PageUp","Home"," "]) {
    h.setFollow(true);h.listeners.get("keydown")({key,shiftKey:key===" ",target:new h.Element()});assert.equal(h.following(),false);
  }
  h.setFollow(true);h.listeners.get("touchstart")({touches:[{clientY:100}]});
  h.listeners.get("touchmove")({touches:[{clientY:130}]});assert.equal(h.following(),false);h.stop();
});
