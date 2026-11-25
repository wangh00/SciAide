import {existsSync, readFileSync} from 'node:fs';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';

// Execute the real local TSX components (including their shared presentation).
export function loadTsx(file) {
  const cache = new Map();
  function load(url) {
    if (cache.has(url.href)) return cache.get(url.href);
    const exports = {};
    cache.set(url.href, exports);
    const require = createRequire(url);
    const source = readFileSync(url, 'utf8');
    const code = ts.transpileModule(source, {compilerOptions: {
      module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022,
    }}).outputText;
    const local = name => {
      const base = name.endsWith('.js') ? name.slice(0,-3) : name;
      for (const suffix of ['.tsx','.ts','']) {
        const candidate = new URL(`${base}${suffix}`,url);
        if (existsSync(candidate)) return load(candidate);
      }
      throw new Error(`Cannot resolve ${name} from ${url.href}`);
    };
    vm.runInNewContext(code, {exports, require: name => name.startsWith('.') ? local(name) : require(name)});
    return exports;
  }
  return load(file);
}
