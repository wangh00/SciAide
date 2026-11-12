import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {loadTsx} from './testTsxLoader.mjs';
import {routeDataOmitEmpty, routeDataInputReady, routeDataAutofillTarget, routeDataDraftWithFile, routeDataPaths} from './routeDataDraft.js';

const port={name:'input_paths',type:'array',fileKind:'tabular',maxItems:16};
const first={attachmentId:'first',name:'wine.csv',sizeBytes:1000,sha256:'a'.repeat(64),portNames:['input_paths']};
const second={...first,attachmentId:'second',name:'other.csv'};
test('route data autofill only unambiguous single table and empty destination',()=>{
  assert.deepEqual(routeDataAutofillTarget([port],[first],{}),{port,candidate:first});
  for(const candidates of [[],[first,second],[{...first,portNames:['input_paths','mapping']}],[{...first,portNames:[]}]]) assert.equal(routeDataAutofillTarget([port],candidates,{}),null);
  assert.equal(routeDataAutofillTarget([port],[first],{input_paths:'["selected.csv"]'}),null);
  assert.equal(routeDataAutofillTarget([{...port,fileKind:undefined}],[first],{}),null);
});
test('route data draft preserves other inputs and avoids duplicate/overlimit additions',()=>{
  const values={input_paths:'["existing.csv"]',research_goal:'my question'};
  assert.deepEqual(routeDataDraftWithFile(values,port,{relativePath:'new.csv'}),{...values,input_paths:'["existing.csv","new.csv"]'});
  assert.equal(routeDataDraftWithFile(values,port,{relativePath:'existing.csv'}),values);
  assert.equal(routeDataDraftWithFile(values,{...port,maxItems:1},{relativePath:'new.csv'}),values);
  assert.deepEqual(routeDataDraftWithFile({}, {...port,type:'string'}, {relativePath:'new.csv'}), {input_paths:'new.csv'});
  assert.deepEqual(routeDataPaths('bad json','array'),[]);
});
const choices=loadTsx(new URL('./ResearchRouteDataChoices.tsx',import.meta.url));
test('uploaded data stays in confirmation card with explicit source and removable selection',()=>{
  const calls=[];
  const file={...first,relativePath:'research-inputs/snapshot.csv'};
  const tree=choices.ResearchRouteDataChoices({candidates:[first,second],files:{first:file},portName:'input_paths',paths:[file.relativePath],disabled:false,maxItems:16,onToggle:(...args)=>calls.push(args)});
  const html=renderToStaticMarkup(tree);
  for(const text of ['wine.csv','other.csv','本任务对话','不会自动合并','开始执行后才采用']) assert.ok(html.includes(text));
  const labels=React.Children.toArray(tree.props.children[1]);
  assert.equal(labels[0].props.children[0].props.checked,true);
  assert.equal(labels[1].props.children[0].props.checked,false);
  labels[0].props.children[0].props.onChange({target:{checked:false}});
  assert.deepEqual(calls,[['first',false]]);
  assert.equal(html.includes('<button'),false,'no extra execution button');
});

test('optional file ports omit empty selections without accepting invalid shapes',()=>{
  for(const type of ['array','string']) {
    const optional={...port,type,required:false,minItems:1,maxItems:2};
    assert.equal(routeDataInputReady(optional,undefined),true);
    assert.equal(routeDataInputReady({...optional,required:true},undefined),false);
    for(const empty of [undefined,type==='array'?[]:'']) assert.equal(routeDataOmitEmpty(optional,empty),true);
    for(const invalid of [null,{},[''],[3],type==='array'?'':[]]) {
      assert.equal(routeDataOmitEmpty(optional,invalid),false);
      assert.equal(routeDataInputReady(optional,invalid),false);
    }
    assert.equal(routeDataInputReady(optional,type==='array'?['data.csv']:'data.csv'),true);
    assert.equal(routeDataInputReady(optional,type==='array'?'data.csv':['data.csv']),false);
  }
  assert.equal(routeDataInputReady({...port,minItems:2},['one.csv']),false);
  assert.equal(routeDataInputReady({...port,maxItems:1},['one.csv','two.csv']),false);
  assert.equal(routeDataOmitEmpty({...port,required:true},[]),false);
});
