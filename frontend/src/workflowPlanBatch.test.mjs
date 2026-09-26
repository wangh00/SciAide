import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const source=readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
test('saved-plan batch deletion retains confirmation and backend safety boundary',()=>{
 const body=source.slice(source.indexOf('async function deleteSelectedPlans()'),source.indexOf('async function deleteWorkflow('));
 assert.match(body,/visibleWorkflows\.filter/);
 assert.ok(body.indexOf('appConfirm')<body.indexOf('"WorkflowFacade","Delete"'));
 assert.match(body,/mutationRef\.current = "delete-plans"/);
 assert.match(body,/catch\(error\) \{failures\.push/);
 assert.match(body,/ids\.filter\(id=>!deleted\.includes\(id\)\)/);
 assert.match(body,/await loadProjectRuns\(\)/);
 assert.match(body,/有未结束任务的方案不会删除/);
});
test('saved-plan selection is scoped to visible project plans',()=>{
 assert.match(source,/setPlanSelection\(\[\]\);setSelectingPlans\(false\);\},\[project.id\]/);
 assert.match(source,/aria-label="全选研究方案"/);
 assert.match(source,/visibleWorkflows.map\(v=>v.id\) : \[\]/);
});
