// Load the built App in a fresh headless browser; all Facade calls stay in memory.
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');
const { chromium } = require(process.env.SCIAIDE_PLAYWRIGHT_PATH || 'playwright');

const dist = path.resolve(__dirname, '../frontend/dist');
const origin = 'http://sciaide-fixture.localhost';

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

async function openResearchBrowser(call) {
  assert.ok(fs.existsSync(path.join(dist, 'index.html')), 'build the frontend before running this fixture');
  const executablePath = process.env.SCIAIDE_BROWSER_EXE;
  const browser = await chromium.launch({ headless: true, ...(executablePath ? { executablePath } : { channel: 'msedge' }) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    page.setDefaultTimeout(7000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const url = new URL(route.request().url());
      const local = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
      if (url.origin !== origin || (local !== 'index.html' && !/^assets\/[\w.-]+$/.test(local))) return route.abort();
      return route.fulfill({ path: path.join(dist, local) });
    });
    await page.exposeFunction('fixtureFacade', call);
    await page.addInitScript(() => {
      window.go = { wails: new Proxy({}, {
        get: (_, facade) => new Proxy({}, { get: (_, method) => (...args) => window.fixtureFacade(facade, method, args) }),
      }) };
      const subscribers = new Map();
      window.runtime = {
        EventsOnMultiple(channel, handler) {
          if (!subscribers.has(channel)) subscribers.set(channel, new Set());
          const group = subscribers.get(channel);
          group.add(handler);
          return () => group.delete(handler);
        },
        OnFileDrop() {}, OnFileDropOff() {},
      };
      window.fixtureRunEvent = event => {
        for (const handler of subscribers.get('sciaide:run-event') ?? []) handler(event);
      };
    });
    return { page, browser, errors, open: () => page.goto(origin) };
  } catch (error) {
    await browser.close();
    throw error;
  }
}

module.exports = { deferred, openResearchBrowser };
