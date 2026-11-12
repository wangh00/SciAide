import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { researchSourceSelection, sourceQueryProjectionView } from "./researchSources.js";
const catalog = [{id:"openalex"},{id:"eric",capabilities:{explicitSelection:false}},{id:"core",capabilities:{explicitSelection:true}}];
test("initial and refreshed source selection never implicitly enables CORE", () => {
 assert.deepEqual(researchSourceSelection(catalog), ["openalex","eric"]);
 assert.deepEqual(researchSourceSelection(catalog,["missing"]), ["openalex","eric"]);
});

test("source projection UI separates planned origin, endpoint syntax and lost constraints", () => {
 const source = Object.freeze({queryProjectionVersion:"source-query-v1",queryOrigin:"planned_provider_query",queryMode:"ranked_bibliographic",inputQuery:"writing NOT school-aged",providerQuery:"writing",queryLimitations:Object.freeze(["negation_not_enforced","field_scope_not_enforced","phrase_matching_not_enforced","boolean_logic_not_enforced"])});
 const view = sourceQueryProjectionView(source);
 assert.equal(view.input,"writing NOT school-aged");
 assert.equal(view.query,"writing");
 assert.equal(view.mode,"相关性排序关键词");
 assert.equal(view.origin,"规划中的来源表达式");
 assert.equal(view.limitations.length,4);
 assert.ok(view.limitations.some(text=>text.includes("否定约束")));
 assert.ok(view.statement.includes("不等价于布尔检索"));
 assert.equal(view.error,"");
});

test("local query rejection is not shown as empty results or provider outage", () => {
 const view = sourceQueryProjectionView({queryProjectionVersion:"source-query-v1",queryMode:"ranked_bibliographic",inputQuery:"NOT writing",errorCode:"invalid_query"});
 assert.equal(view.query,"");
 assert.ok(view.error.includes("未向该来源发送请求"));
});

test("a warning-free planned query does not prove logical equivalence; years remain explicit", () => {
 const view=sourceQueryProjectionView({queryProjectionVersion:"source-query-v1",queryOrigin:"planned_provider_query",queryMode:"ranked_bibliographic",logicalQuery:"writing NOT adults",inputQuery:"writing",providerQuery:"writing",publicationYears:{from:2023,to:2026},yearFilterApplied:true});
 assert.equal(view.logical,"writing NOT adults");
 assert.deepEqual(view.limitations,[]);
 assert.ok(view.statement.includes("语义一致性仍须核对"));
 assert.equal(view.years,"出版年：2023–2026（拟应用）");
});

test("old records are not silently upgraded and unknown limitations are disclosed", () => {
 assert.equal(sourceQueryProjectionView({queryMode:"planned_provider_query"}),null);
 const view=sourceQueryProjectionView({queryProjectionVersion:"source-query-v1",queryMode:"native_query",queryLimitations:["future-limitation"]});
 assert.ok(view.limitations[0].includes("future-limitation"));
 assert.equal(view.statement,"");
 const app=readFileSync(new URL("./App.tsx",import.meta.url),"utf8");
 assert.ok(app.includes("<ResearchSourceQuery source={item}/>") );
 const component=readFileSync(new URL("./ResearchSourceQuery.tsx",import.meta.url),"utf8");
 assert.ok(component.includes("不证明本次请求已发送"));
 assert.ok(component.includes("编译后的来源查询表达式"));
});
test("explicit CORE selection is retained, while stale IDs are removed", () => {
 assert.deepEqual(researchSourceSelection(catalog,["eric","core","missing"]), ["eric","core"]);
});
test("the actual search panel initializes via opt-in-aware selection", () => {
 const source=readFileSync(new URL("./App.tsx",import.meta.url),"utf8");
 assert.ok(source.includes("researchSourceSelection(catalog, current)"));
 assert.ok(source.includes("sourceIds: selectedSources"));
 assert.ok(!source.includes("current.length ? current : catalog.map((item) => item.id)"));
});
