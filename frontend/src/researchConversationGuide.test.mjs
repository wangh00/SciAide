import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';

async function load(file) {
  return loadTsx(new URL(file, import.meta.url));
}
const guide = await load('./ResearchConversationGuide.tsx');
const result = await load('./ResearchAnalysisResult.tsx');
function nodes(node, type) {
  return !node || typeof node !== 'object' ? [] : [...(node.type === type ? [node] : []),...React.Children.toArray(node.props?.children).flatMap(child => nodes(child,type))];
}

test('start examples only fill editable drafts and describe actual file controls', () => {
  const drafts = [];
  const tree = guide.ResearchGettingStarted({onDraft: prompt => drafts.push(prompt)});
  const html = renderToStaticMarkup(tree);
  for (const text of ['文献比较','数据分析','研究设计','不会启动','添加本地文献','添加数据文件']) assert.ok(html.includes(text));
  nodes(tree,'button').forEach(button => {assert.equal(button.props.type,'button');button.props.onClick();});
  assert.equal(drafts.length,3);
  assert.match(drafts[0],/原文定位/);
  assert.match(drafts[1],/采用路线后/);
  assert.ok(nodes(guide.ResearchGettingStarted({disabled:true,onDraft(){}}),'button').every(button => button.props.disabled));
});

test('example insertion preserves user drafts and avoids duplicating the same example', () => {
  assert.equal(guide.appendResearchExample('','example'),'example');
  assert.equal(guide.appendResearchExample('My actual question','example'),'My actual question\n\nexample');
  assert.equal(guide.appendResearchExample('My actual question\n\nexample','example'),'My actual question\n\nexample');
});

test('followup examples follow existing route capabilities rather than promise new stages', () => {
  const drafts = [];
  const render = (evidence,computation) => guide.ResearchFollowupGuide({evidence,computation,onDraft: prompt => drafts.push(prompt)});
  assert.equal(nodes(render(false,false),'button').length,1);
  assert.equal(nodes(render(true,false),'button').length,2);
  const tree = render(true,true);
  assert.equal(nodes(tree,'button').length,5);
  nodes(tree,'button').forEach(button => {assert.equal(button.props.type,'button');button.props.onClick();});
  assert.equal(drafts.length,5);
  const html = renderToStaticMarkup(tree);
  for (const text of ['回形针','不发送、不计算','先不要登记','文件','重算范围']) assert.ok(html.includes(text));
  assert.match(drafts[2],/当前实际采用/);
  assert.match(drafts[3],/完整文件集合/);
  assert.match(drafts[4],/不要原样执行/);
});

test('analysis-only delivery separates computed facts from inference and renders untrusted text inertly', () => {
  const html = renderToStaticMarkup(React.createElement(result.ResearchAnalysisResult,{result:{summary:'<script>alert(1)</script>',computedFacts:['Sum=3'],inferences:['Association only'],alternativeExplanations:[],methodChecks:['Check units'],limitations:['Small sample'],confidence:'low'}}));
  for (const text of ['本次数据分析结果','计算事实','推断与解释','Sum=3','Small sample','不等于科学认证']) assert.ok(html.includes(text));
  assert.ok(!html.includes('<script>'));
  assert.ok(!html.includes('其他可能解释'));
});

test('guides and report-free results are wired to original planning and chat surfaces', async () => {
  const app = await readFile(new URL('./App.tsx',import.meta.url),'utf8');
  assert.match(app,/<ResearchGettingStarted disabled=\{Boolean\(busy\)\}/);
  assert.match(app,/const next = appendResearchExample\(researchIdea, prompt\)/);
  assert.match(app,/if \(next.length > 8000\)/);
  assert.match(app,/onDraft=\{fillResearchDraft\}/);
  assert.match(app,/setInput\(current => appendResearchExample/);
  assert.match(app,/workflowRevisionConversationId\(runDetail\) \? <ResearchFollowupGuide/);
  assert.match(app,/if \(interpretation\) return <div className="research-completion-stack"><ResearchAnalysisResult/);
});
