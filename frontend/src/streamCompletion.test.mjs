import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import ts from 'typescript';

const source=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
function fixture(text=''){
  let messages=[{id:'m',text}];const frames=new Map();let id=0;
  const context={useCallback:f=>f,pendingContentDeltasRef:{current:new Map()},completedContentIdsRef:{current:new Set()},contentFrameRef:{current:null},textOf:m=>m.text,normalizeDisplayText:x=>x,replaceMessageText:(m,text)=>({...m,text}),setMessages:f=>messages=f(messages),window:{requestAnimationFrame:f=>{frames.set(++id,f);return id},cancelAnimationFrame:i=>frames.delete(i)}};
  vm.createContext(context);vm.runInContext(ts.transpileModule(source.slice(source.indexOf('  const flushContentDeltas ='),source.indexOf('  const applySnapshot ='))+'\nglobalThis.handlers={queueContentDelta,completeStreamContent,resetContentAttempt};',{}).outputText,context);
  return {...context.handlers,get:()=>messages[0],flush:()=>{const jobs=[...frames.values()];frames.clear();jobs.forEach(f=>f())}};
}
test('completed text never clears or replays even when snapshot arrived first',()=>{
  const f=fixture('full answer'),before=f.get();f.completeStreamContent('m','full answer');assert.equal(f.get(),before);f.flush();assert.equal(f.get().text,'full answer');
});
test('completion replaces pending delta atomically and ignores late deltas',()=>{
  const f=fixture('first');f.queueContentDelta('m',' second');f.completeStreamContent('m','first second final');f.flush();assert.equal(f.get().text,'first second final');f.queueContentDelta('m',' late');f.flush();assert.equal(f.get().text,'first second final');
});
test('real retry resets once and can stream again',()=>{
  const f=fixture();f.completeStreamContent('m','old');f.resetContentAttempt('m');assert.equal(f.get().text,'');f.queueContentDelta('m','new');f.flush();assert.equal(f.get().text,'new');f.completeStreamContent('m','new final');assert.equal(f.get().text,'new final');
});
test('process is before answer; artificial typewriter timer removed',()=>{
  const row=source.slice(source.indexOf('const MessageRow ='),source.indexOf('function CitationMarker('));
  assert.ok(row.indexOf('<RunProcess ')<row.indexOf('<CitedAnswer '));
  assert.ok(row.indexOf('<WorkflowMessageActivityCard ')<row.indexOf('<CitedAnswer '));
  assert.doesNotMatch(source,/beginContentReveal|ContentRevealJob|pauseUntil|targetSeconds/);
});

function interruptedText(message){
  const context={};vm.createContext(context);
  const start=source.indexOf('const normalizeDisplayText =');
  const end=source.indexOf('const visibleMessageText =');
  vm.runInContext(ts.transpileModule(source.slice(start,end)+'\nglobalThis.read=interruptedMessageText;',{}).outputText,context);
  return context.read(message);
}
test('cancelled commentary remains visible in paragraphs without exposing internal reasoning or tools',()=>{
  const message={role:'assistant',status:'incomplete',parts:[{type:'text',text:''},{type:'tool_result',payload:{kind:'run_termination_context',activities:[{commentary:'只查 MCP。',reasoningSummary:'hidden'},{commentary:''},{commentary:'发现配置目录。'}],tools:[{result:'hidden result'}]}}]};
  assert.equal(interruptedText(message),'只查 MCP。\n\n发现配置目录。');
  message.parts[1].payload.draft='发现配置目录。';
  assert.equal(interruptedText(message),'只查 MCP。\n\n发现配置目录。');
  message.parts[0].text='尚未完成的回答';
  assert.equal(interruptedText(message),'只查 MCP。\n\n发现配置目录。\n\n尚未完成的回答');
  assert.equal(interruptedText({...message,internal:true}),'尚未完成的回答');
  assert.equal(interruptedText({...message,status:'complete'}),'尚未完成的回答');
});
test('composer does not render inline token accounting; dashboard remains available',()=>{
  assert.doesNotMatch(source,/const usage = useMemo|usage \|\| <><kbd>/);
  assert.match(source,/<UsageDashboard/);
});
