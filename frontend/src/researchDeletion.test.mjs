import assert from 'node:assert/strict';
import test from 'node:test';
import {readFile} from 'node:fs/promises';
import vm from 'node:vm';
import ts from 'typescript';

const app=await readFile(new URL('./App.tsx',import.meta.url),'utf8');
const start=app.indexOf('  async function deleteRun(run: WorkflowRun)'),end=app.indexOf('\n  const startPanel',start);
assert.ok(start>0&&end>start);
const script=ts.transpileModule(app.slice(start,end)+'\nglobalThis.remove=deleteRun;',{}).outputText;
function fixture({refreshFails=false}={}){
 let resolve;const pending=new Promise(r=>{resolve=r});const calls=[],feedback=[];
 const context={busy:'',mutationRef:{current:''},workflowTerminal:s=>s==='completed',appConfirm:async()=>true,project:{id:'p'},setBusy:()=>{},setFeedback:s=>feedback.push(s),backend:async(...args)=>{calls.push(args);return pending},activeRunIdRef:{current:'other'},loadProjectRuns:async()=>{if(refreshFails)throw Error('refresh failed')},errorText:String};
 vm.createContext(context);vm.runInContext(script,context);return {context,calls,feedback,finish:resolve};
}
test('research deletion is single-flight even if two confirmation dialogs resolve',async()=>{
 const f=fixture(),run={id:'old',status:'completed'};
 const one=f.context.remove(run),two=f.context.remove(run);await new Promise(resolve=>setImmediate(resolve));
 assert.equal(f.calls.length,1);assert.deepEqual(f.calls[0],['WorkflowFacade','DeleteRun','p','old']);
 assert.match(f.feedback.join(' '),/正在删除任务及关联历史/);f.finish();await Promise.all([one,two]);
 assert.match(f.feedback.join(' '),/任务已删除，正在刷新列表/);assert.equal(f.context.mutationRef.current,'');
});
test('post-delete refresh failure never claims deletion failed',async()=>{
 const f=fixture({refreshFails:true});const work=f.context.remove({id:'old',status:'completed'});await Promise.resolve();f.finish();await work;
 assert.match(f.feedback.at(-1),/任务已删除，但列表刷新失败/);assert.equal(f.calls.length,1);
});
