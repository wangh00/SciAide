import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createRequire} from 'node:module';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';

const require = createRequire(import.meta.url);
const module = {exports:{}};
const js = ts.transpileModule(readFileSync(new URL('./RecordIdentityHints.tsx',import.meta.url),'utf8'),{compilerOptions:{module:ts.ModuleKind.CommonJS,jsx:ts.JsxEmit.ReactJSX}}).outputText;
vm.runInNewContext(js,{module,exports:module.exports,require:name => name.endsWith('.css') ? {} : require(name)});
const render = identity => renderToStaticMarkup(React.createElement(module.exports.RecordIdentityHints,{identity}));
const ref = {sourceId:'core',sourceRecordId:'322057997',workSha256:'a'.repeat(64)};

test('identity hints display unverified provenance without changing selection',()=>{
 const html=render({version:'record-identity-hints-v1',roles:[{kind:'companion_material',field:'title',record:ref}],relations:[{kind:'possible_same_work',targetCandidateId:'other',records:[ref],basis:[]}]});
 for(const text of ['待核实','补充/伴随材料线索','疑似同一成果','core:322057997','文档数不等于独立研究数','未自动合并或排除']) assert.ok(html.includes(text),text);
 assert.ok(!html.includes('<input')&&!html.includes('<button'));
});
test('omitted hints, legacy records and unknown versions are not confirmations',()=>{
 assert.equal(render(undefined),'');
 assert.equal(render({version:'record-identity-hints-v1'}),'');
 assert.match(render({version:'future'}),/线索版本未知/);
 const html=render({version:'record-identity-hints-v1',roles:[{kind:'dataset',field:'workType',record:ref}],preview:{truncated:true,roleCount:8,relationCount:9,sha256:'b'.repeat(64)}});
 assert.match(html,/视图有省略/);assert.match(html,/8 条类型/);assert.match(html,/9 条关系/);assert.match(html,/SHA256 bbbb/);
});
test('source-supplied strings are escaped and both discovery and workflow use hints',()=>{
 const html=render({version:'record-identity-hints-v1',roles:[{kind:'<script>bad</script>',field:'title',record:{...ref,sourceRecordId:'<img onerror=x>'}}]});
 assert.ok(!html.includes('<script>')&&!html.includes('<img'));
 const app=readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
 assert.match(app,/<RecordIdentityHints identity=\{selected\.recordIdentity\}/);
 assert.match(app,/<RecordIdentityHints identity=\{value\.recordIdentity\}/);
});

test('full scoped inventory displays detected totals, not just retained witnesses',()=>{
 const html=render({version:'record-identity-hints-v1',roles:[],relations:[{kind:'identifier_conflict',targetCandidateId:'other',records:[ref],basis:[]}],scopes:[{roleCount:0,relationCount:399,omittedRoles:0,omittedRelations:397,relationKinds:{identifier_conflict:1,possible_same_work:398}}]});
 assert.match(html,/399 条关系/);assert.match(html,/标识冲突待核对/);assert.match(html,/视图有省略/);
});
