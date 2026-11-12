import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {readFile} from 'node:fs/promises';
import {loadTsx} from './testTsxLoader.mjs';

const {reportDisplayLayers, ResearchReportLayers} = loadTsx(new URL('./ResearchReportLayers.tsx', import.meta.url));
const markdown = '# Report\n\n## 普通人说明\nBrief [K-000000000001].\n\n## 非诊疗声明\nNot medical advice.\n\n## Detailed science\nDetails remain.\n\n## 附录：候选筛选记录\nTrace-only large record.\n\n## 后续科学讨论\nStill scientific body.\n\n## 附录：引用来源书目\nBibliography.\n';
const report = {markdown, claimSummary: [], limitations: []};

test('report display separates only known host appendices, preserves later scientific sections and original text', () => {
  const before = structuredClone(report);
  const layers = reportDisplayLayers(markdown);
  assert.match(layers.brief, /Brief \[K-000000000001\]/);
  assert.match(layers.brief, /Not medical advice/);
  assert.doesNotMatch(layers.brief, /Details remain|Trace-only/);
  assert.match(layers.body, /Details remain/);
  assert.match(layers.body, /Still scientific body/);
  assert.doesNotMatch(layers.body, /Trace-only|Bibliography/);
  assert.match(layers.provenance, /Trace-only/);
  assert.match(layers.provenance, /Bibliography/);
  assert.deepEqual(report, before);
  const ordinary = '# Result\n\n## 附录：原始数据\nImportant scientific data.\n';
  assert.equal(reportDisplayLayers(ordinary).body, ordinary);
  assert.equal(reportDisplayLayers(ordinary).provenance, '');
});

test('fenced code and nested headings cannot masquerade as host appendix or brief sections', () => {
  for (const fence of ['```', '~~~~']) {
    const text = `# Result\r\n\r\n${fence}markdown\r\n## 普通人说明\r\nFake summary\r\n## 附录：候选筛选记录\r\nFake trace\r\n${fence}\r\n\r\n### 附录：检索方法记录\r\nScientific nested appendix\r\n`;
    const layers = reportDisplayLayers(text);
    assert.equal(layers.body, text);
    assert.equal(layers.brief, '');
    assert.equal(layers.provenance, '');
  }
});

function renderView(value, view, observed) {
  const original = React.useState;
  React.useState = initial => original(initial === 'brief' ? view : initial);
  try {
    return renderToStaticMarkup(React.createElement(ResearchReportLayers, {report: value, renderMarkdown: text => { observed.push(text); return React.createElement('div', null, text); }}));
  } finally { React.useState = original; }
}

test('only the selected report layer is rendered; switching retains exact citation markers and saved Markdown', () => {
  const before = JSON.stringify(report);
  for (const view of ['brief', 'body', 'provenance']) {
    const observed = [];
    const html = renderView(report, view, observed);
    assert.equal(observed.length, 1);
    assert.equal(observed[0], reportDisplayLayers(markdown)[view]);
    assert.match(html, /aria-pressed="true"/);
    if (view === 'brief') assert.doesNotMatch(html, /Trace-only|Details remain/);
  }
  assert.equal(JSON.stringify(report), before);
});

test('different research directions need no fixed plain-language schema or extra model call', () => {
  const small = {markdown: '# Method comparison\nResult and limits.', claimSummary: [], limitations: []};
  const observed = [];
  renderView(small, 'brief', observed);
  assert.equal(observed[0], small.markdown);
  const large = {markdown: '# Data analysis\n' + 'Detailed record. '.repeat(600), claimSummary: ['Saved conclusion [K-000000000001]'], limitations: ['No external validation']};
  const values = [];
  const html = renderView(large, 'brief', values);
  assert.match(html, /Saved conclusion/);
  assert.match(html, /No external validation/);
  assert.doesNotMatch(html, /Detailed record/);
  assert.equal(values.join('\n'), large.claimSummary.concat(large.limitations).join('\n'));
});

test('both completed report and history use the same layer component, registration still receives complete report', async () => {
  const source = await readFile(new URL('./App.tsx', import.meta.url), 'utf8');
  assert.match(source, /report\?\.markdown && <ResearchReportLayers/);
  assert.match(source, /document=\{<ResearchReportLayers report=\{report\}/);
  assert.doesNotMatch(source, /saveDeliverable.*reportDisplayLayers/);
});
