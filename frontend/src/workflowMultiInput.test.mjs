import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import vm from "node:vm";
import ts from "typescript";
import { routeDataPaths } from "./routeDataDraft.js";
import { loadTsx } from "./testTsxLoader.mjs";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
const body = source.slice(source.indexOf("function WorkflowInputFields("), source.indexOf("function ", source.indexOf("function WorkflowInputFields(")+10));
const exports = {};
const {ResearchRouteDataChoices} = loadTsx(new URL("./ResearchRouteDataChoices.tsx", import.meta.url));
vm.runInNewContext(ts.transpileModule('import * as React from "react";\n'+body+'\nexport {WorkflowInputFields}', {compilerOptions:{module:ts.ModuleKind.CommonJS,jsx:ts.JsxEmit.React}}).outputText, {exports,require:createRequire(import.meta.url),workflowInputFilePath:()=>"",routeDataPaths,ResearchRouteDataChoices,Icon:()=>null});
function nodes(node,type) { return !node || typeof node!=="object" ? [] : [...(node.type===type?[node]:[]),...React.Children.toArray(node.props?.children).flatMap(n=>nodes(n,type))]; }
test("multiple input files stay visible and can be removed independently",()=>{
 let updated;
 const tree=exports.WorkflowInputFields({definition:{inputs:[{name:"input_paths",type:"array",fileKind:"tabular",maxItems:16}]},values:{input_paths:JSON.stringify(["data.csv","groups.xlsx"])},disabled:false,chooseFile(){},update:(key,value)=>{updated=JSON.parse(value);}});
 const html=renderToStaticMarkup(tree);
 assert.match(html,/data.csv/);assert.match(html,/groups.xlsx/);
 nodes(tree,"button").find(n=>n.props["aria-label"]==="移除 data.csv").props.onClick();
 assert.deepEqual(updated,["groups.xlsx"]);
});
test("adopted route hides canonical fields without deleting input values",()=>{
 const values={research_goal:"frozen goal",route_context:"{}"};
 const definition={inputs:[{name:"research_goal",type:"string"},{name:"route_context",type:"object"}]};
 const html=renderToStaticMarkup(exports.WorkflowInputFields({definition,values,disabled:false,frozenRoute:true,chooseFile(){},update(){}}));
 assert.equal(html,""); assert.equal(values.research_goal,"frozen goal");
});
test("uploaded selection replaces only its duplicate path row and keeps manually selected files",()=>{
 const candidate={attachmentId:"uploaded",name:"wine.csv",sizeBytes:1024,sha256:"a".repeat(64),portNames:["input_paths"]};
 const file={...candidate,relativePath:"research-inputs/uploaded.csv"};
 const tree=exports.WorkflowInputFields({definition:{inputs:[{name:"input_paths",type:"array",fileKind:"tabular",maxItems:16}]},values:{input_paths:JSON.stringify([file.relativePath,"manual.csv"])},disabled:false,uploadedData:[candidate],uploadedFiles:{uploaded:file},toggleUploadedData(){},chooseFile(){},update(){}});
 const html=renderToStaticMarkup(tree);
 assert.match(html,/wine.csv/);assert.match(html,/manual.csv/);assert.match(html,/checked/);
 assert.equal(html.includes(`title="${file.relativePath}"`),false);
 assert.equal(nodes(tree,"button").filter(n=>n.props["aria-label"]==="移除 manual.csv").length,1);
});
