// Actual React Markdown + shipped CSS/font files in Edge. No model, Wails RPC
// or formal task data; baseline uses the original KaTeX CSS/WOFF2 resources.
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const { createRequire } = require('node:module');
const req = createRequire(path.resolve('frontend/package.json'));
const { build } = req('esbuild');
const { chromium } = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');
const out = path.resolve('.tmp/recovery-size-20261011/math-ui');

(async () => {
  fs.mkdirSync(out, { recursive: true });
  const math = String.raw`# 科研公式字体验证

连续变量 $x_i \sim \mathcal{N}(\mu,\sigma^2)$ 与中文叙述。

$$
\hat\beta=(X^T X)^{-1}X^T y,\quad \sum_{i=1}^n \frac{(x_i-\bar x)^2}{n-1}
$$

$$
\int_{-\infty}^{\infty} e^{-x^2} dx=\sqrt\pi,\quad \mathbb{R} \subseteq \mathbb{C}
$$

$$
\mathbf{A}=\begin{pmatrix}1&2\\3&4\end{pmatrix},\quad\mathfrak{F},\mathscr{L},\boldsymbol{\alpha},\texttt{data}
$$

$$
\left\{\frac{1}{1+\frac{1}{1+\frac{1}{x}}}\right\},\quad\underbrace{a+b+\cdots+z}_{n},\quad \widehat{abcdef}
$$

$$
\mathsf{H},\quad \mathrm{normal},\quad\mathit{italic},\quad\displaystyle \prod_{k=1}^n k
$$
`;
  const bundle = await build({ stdin: { contents: `import React from 'react';import{createRoot}from'react-dom/client';import Markdown from'react-markdown';import{markdownRemarkPlugins,markdownRehypePlugins}from'./markdownPlugins.js';createRoot(document.getElementById('root')).render(<div className="markdown-body"><Markdown remarkPlugins={markdownRemarkPlugins} rehypePlugins={markdownRehypePlugins}>{${JSON.stringify(math)}}</Markdown></div>);`, resolveDir: path.resolve('frontend/src'), loader: 'tsx' }, bundle: true, write: false, format: 'iife', jsx: 'automatic', logLevel: 'silent' });
  const files = fs.readdirSync('frontend/dist/assets');
  assert.equal(files.filter(f => /\.(ttf|woff)$/.test(f)).length, 0, 'legacy font copies returned');
  assert.equal(files.filter(f => f.endsWith('.woff2')).length, 19);
  const css = files.find(f => f.endsWith('.css'));
  const baseline = fs.readFileSync('frontend/node_modules/katex/dist/katex.min.css', 'utf8').replaceAll('url(fonts/', 'url(/baseline/fonts/');
  const server = http.createServer((request, response) => {
    const url = new URL(request.url, 'http://localhost');
    if (url.pathname === '/') {
      response.setHeader('Content-Type', 'text/html; charset=utf-8');
      response.end(`<html lang="zh-CN"><head><link rel="stylesheet" href="/assets/${css}">${url.searchParams.has('baseline') ? `<style>${baseline}</style>` : ''}<style>body{background:white;margin:0}#root{width:720px;margin:30px auto;padding:24px}.markdown-body{font-size:16px}</style></head><body><div id="root"></div><script src="/fixture.js"></script></body></html>`);
      return;
    }
    if (url.pathname === '/fixture.js') { response.setHeader('Content-Type', 'text/javascript'); response.end(bundle.outputFiles[0].text); return; }
    let file;
    if (/^\/assets\/[\w.-]+$/.test(url.pathname)) file = path.join('frontend/dist', url.pathname);
    else if (/^\/baseline\/fonts\/[\w.-]+$/.test(url.pathname)) file = path.join('frontend/node_modules/katex/dist/fonts', path.basename(url.pathname));
    if (!file || !fs.existsSync(file)) { response.writeHead(404); response.end(); return; }
    response.setHeader('Content-Type', file.endsWith('.css') ? 'text/css' : 'font/woff2');
    response.end(fs.readFileSync(file));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  const errors = [], results = {}, fontRequests = [];
  try {
    for (const baseline of [true, false]) {
      const name = baseline ? 'baseline' : 'woff2-only';
      const page = await browser.newPage({ viewport: { width: 1000, height: 950 } });
      page.on('pageerror', err => errors.push(err.message));
      page.on('response', res => { if (/\.(woff2?|ttf)(?:\?|$)/.test(res.url())) { fontRequests.push({ name, url: res.url(), status: res.status() }); } });
      await page.goto(origin + (baseline ? '/?baseline=1' : '/'));
      await page.locator('.katex-display').first().waitFor();
      await page.evaluate(() => document.fonts.ready);
      results[name] = await page.evaluate(() => ({
        errors: document.querySelectorAll('.katex-error').length,
        boxes: [...document.querySelectorAll('.katex-display')].map(el => { const r = el.getBoundingClientRect(); return { width: r.width, height: r.height }; }),
        fonts: [...document.fonts].filter(f => f.status === 'loaded').map(f => ({ family: f.family, status: f.status })),
        overflow: document.documentElement.scrollWidth > innerWidth,
      }));
      assert.equal(results[name].errors, 0);
      assert.equal(results[name].boxes.length, 5);
      assert.ok(results[name].fonts.filter(f => f.family.startsWith('KaTeX_')).length >= 8);
      assert.equal(results[name].overflow, false);
      await page.screenshot({ path: path.join(out, name + '.png'), fullPage: true });
      await page.close();
    }
    assert.deepEqual(results['woff2-only'].boxes, results.baseline.boxes, 'formula layout changed');
    assert.ok(fontRequests.length > 0);
    assert.ok(fontRequests.every(f => f.status === 200 && f.url.endsWith('.woff2')), 'font fallback or failed request');
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(out, 'verification.json'), JSON.stringify({ results, fontRequests, errors }, null, 2));
    console.log('PASS: actual React math, all displayed fonts loaded, original/WOFF2-only metrics identical; production assets have no WOFF/TTF; no model or formal DB.');
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(err => { console.error(err); process.exit(1); });
