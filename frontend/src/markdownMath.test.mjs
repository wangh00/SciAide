import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import ReactMarkdown from 'react-markdown';
import {markdownRemarkPlugins, markdownRehypePlugins} from './markdownPlugins.js';
import {safeMarkdownURL} from './markdownRender.js';

const render = text => renderToStaticMarkup(React.createElement(ReactMarkdown, {
  remarkPlugins: markdownRemarkPlugins,
  rehypePlugins: markdownRehypePlugins,
  skipHtml: true, urlTransform: safeMarkdownURL,
}, text));

test('instrumental-variable screenshot renders inline and display TeX, not literal delimiters', () => {
  const html = render(String.raw`## 第一阶段
用工具变量 $Z$ 对内生变量 $X$ 进行回归： $$X = \pi_0 + \pi_1 Z + v$$ 得到 $X$ 的拟合值 $\hat{X}$。

## 第二阶段
$$
Y = \beta_0 + \beta_1 \hat{X} + \varepsilon
$$

相关性：$$Cov(Z, X) \neq 0$$`);
  assert.match(html, /class="katex"/);
  assert.match(html, /class="katex-display"/);
  assert.match(html, /<math/);
  assert.doesNotMatch(html, /katex-error/);
  assert.doesNotMatch(html, /\$\$/);
  assert.match(html, /<h2>第二阶段/);
});

test('math coexists with tables, citations, currency escaping and code', () => {
  const html = render('价格 \\$5，另付 \\$10。\n\n`$x$`\n\n```python\ns = "$x$"\n```\n\n| 符号 | 值 |\n|---|---|\n| $X$ | $2$ |\n\n依据 [K-0123456789AB]');
  assert.match(html, /价格 \$5，另付 \$10/);
  assert.match(html, /<code>\$x\$<\/code>/);
  assert.match(html, /language-python/);
  assert.match(html, /<table>/);
  assert.match(html, /href="sciaide-citation:K-0123456789AB"/);
});

test('incomplete, invalid and expansion-heavy TeX cannot crash the message', () => {
  for (const value of [String.raw`前文 $\frac{`, String.raw`$\unknowncommand{x}$ 后文`, String.raw`$\def\a{\a}\a$`]) {
    assert.doesNotThrow(() => render(value));
  }
  assert.match(render(String.raw`$\unknowncommand{x}$ 后文`), /后文/);
});

test('TeX cannot inject trusted HTML, external images, active links or cross-message macros', () => {
  const html = render(String.raw`$\href{javascript:alert(1)}{click}$ $\includegraphics{https://example.org/tracker.png}$ $\htmlClass{injected}{X}$

<script>alert(1)</script>`);
  assert.doesNotMatch(html, /<script|<img|href="javascript:|class="injected"/);
  render(String.raw`$\gdef\secretmacro{SECRET}$`);
  assert.doesNotMatch(render(String.raw`$\secretmacro$`), />SECRET</);
});
