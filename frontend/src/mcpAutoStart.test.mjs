import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import ts from 'typescript';

const source=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
const settings=source.slice(source.indexOf('function MCPSettings()'));
const save=settings.slice(settings.indexOf('  async function save('),settings.indexOf('  async function importServers('));
test('MCP saving preserves the selected startup preference without connecting',async()=>{
  for(const autoStart of [true,false]) {
    const calls=[];
    const context={autoStart,id:'builtin-exa-search',name:'Exa',namespace:'exa',transport:'streamable_http',url:'https://mcp.exa.ai/mcp?tools=web_search_exa',command:'',args:'',workingDir:'',headers:'',env:'',secretValues:'',clearSecrets:[],enabled:true,trusted:true,current:{timeoutSeconds:30},object:()=>({}),backend:async(...a)=>{calls.push(a);return{id:'builtin-exa-search'};},refresh:async()=>{},setBusy:()=>{},setFeedback:()=>{},setId:()=>{},setSecretValues:()=>{},setClearSecrets:()=>{},setToast:()=>{},errorText:String};
    vm.createContext(context);vm.runInContext(ts.transpileModule(save+'\nglobalThis.saveMCP=save;',{}).outputText,context);
    await context.saveMCP({preventDefault(){}});
    assert.equal(calls.length,1);assert.equal(calls[0][1],'SaveMCPServer');assert.equal(calls[0][2].autoStart,autoStart);
    assert.equal(calls[0][2].args.length,0);
  }
});
