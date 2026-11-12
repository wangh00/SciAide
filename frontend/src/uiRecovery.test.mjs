import assert from 'node:assert/strict';
import test from 'node:test';
import {startUIHeartbeat, readUIView, saveUIView} from './uiRecovery.js';

test('heartbeat is read-only, single-flight and cleans all listeners on pagehide',async()=>{
  const listeners=new Map(),calls=[];let tick,complete,cleared=0;
  const doc={hidden:false,addEventListener:(k,v)=>listeners.set(k,v),removeEventListener:k=>listeners.delete(k)};
  const surface={document:doc,go:{wails:{SystemFacade:{UIHeartbeat:hidden=>{calls.push(hidden);return new Promise(r=>complete=r)}}}},setInterval:fn=>(tick=fn,1),clearInterval:()=>cleared++,addEventListener:(k,v)=>listeners.set(k,v),removeEventListener:k=>listeners.delete(k)};
  startUIHeartbeat(surface);tick();tick();assert.deepEqual(calls,[false]);
  complete();await Promise.resolve();doc.hidden=true;listeners.get('visibilitychange')();assert.deepEqual(calls,[false,true]);
  complete();await Promise.resolve();listeners.get('pagehide')();tick();assert.equal(calls.length,2);assert.equal(cleared,1);assert.equal(listeners.size,0);
});

test('page recovery restores navigation and unsent drafts without executing anything',()=>{
  let saved=null;const surface={sessionStorage:{setItem:(k,v)=>saved=v,getItem:()=>saved}};
  const view={projectId:'p',conversationId:'c',mode:'research',draft:'未发送要求',attachments:[{id:'draft:d',originalName:'data.csv',draftPath:'D:/Fixture/data.csv'}]};
  saveUIView(view,surface);assert.deepEqual(readUIView(surface),view);
  saved='not json';assert.equal(readUIView(surface),null);
  saved=JSON.stringify({...view,mode:'execute'});assert.equal(readUIView(surface),null);
  saved=JSON.stringify({...view,attachments:[{id:'untrusted'}]});assert.deepEqual(readUIView(surface).attachments,[]);
  assert.doesNotThrow(()=>saveUIView(view,{sessionStorage:{setItem:()=>{throw Error('full')}}}));
});
