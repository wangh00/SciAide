import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';

const exports = loadTsx(new URL('./ResearchReportCard.tsx',import.meta.url));
const report = {markdown:'# Original report',claimSummary:['Supported claim'],methodSummary:'Source comparison',limitations:['Unverified transfer'],confidence:'low'};
function render(quality, extra={}) {
  return renderToStaticMarkup(React.createElement(exports.ResearchReportCard, {report,quality,label:'证据综述稿',acceptance:'Original review',renderMarkdown:text=>text,document:report.markdown,actions:'Existing action',...extra}));
}

test('a saved limited report preserves its score and exposes separate evidence boundaries', () => {
  const before=structuredClone(report);
  const html=render({coverage:'limited',limitedDraft:true,selectedExcerpts:17,unselectedRecommended:12,confidencePolicy:'legacy_limited'});
  assert.match(html,/范围受限的研究初稿/);
  assert.match(html,/confidence-low[^>]*>低/);
  assert.match(html,/旧版有限证据提示要求低置信/);
  assert.match(html,/采用 17 条摘录.*12 条推荐未采用/);
  for(const text of ['模型置信判断','证据覆盖','应用与外推','局限与未核验内容','Original review','# Original report','Existing action']) assert.ok(html.includes(text));
  assert.doesNotMatch(html,/最终交付|让 AI 整理|重新评估置信度/);
  assert.deepEqual(report,before);
});

test('data-only report uses computation basis and embeds files in the same card without rescoring',()=>{
 const html=render(undefined,{computationOnly:true,files:React.createElement('section',{'aria-label':'本次生成的文件'},'metrics.csv')});
 assert.match(html,/结果依据/);assert.match(html,/绑定数据与计算产物/);assert.match(html,/本次分割/);assert.doesNotMatch(html,/覆盖未单独标注|采用 0 条/);assert.match(html,/confidence-low[^>]*>低/);
 assert.ok(html.indexOf('本次生成的文件')<html.indexOf('# Original report'));
});

test('new or missing policy never invents legacy constraints or scientific approval', () => {
  const html=render({coverage:'adequate',limitedDraft:false,selectedExcerpts:30,unselectedRecommended:0,confidencePolicy:'claim_based'});
  assert.match(html,/confidence-low[^>]*>低/);
  assert.match(html,/筛查覆盖充足/);
  assert.doesNotMatch(html,/旧版有限证据提示|范围受限的研究初稿/);
  assert.match(render(undefined),/覆盖未单独标注/);
  assert.doesNotMatch(render(undefined),/采用 0 条/);
  assert.doesNotMatch(render({coverage:'unknown',limitedDraft:false,selectedExcerpts:0,unselectedRecommended:0,confidencePolicy:'unknown'}),/采用 0 条/);
});
