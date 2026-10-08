import {readFileSync} from 'node:fs';
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
    vm.runInNewContext(code, {exports, require: name => name.startsWith('.') ? load(new URL(`${name}.tsx`,url)) : require(name)});
    return exports;
  }
  return load(file);
}
