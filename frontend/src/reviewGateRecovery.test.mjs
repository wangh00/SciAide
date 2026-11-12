import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {workflowReviewGateRejected,workflowStepCanRetry} from './researchTimelineState.js';
import {toolDecisionLabel} from './retryPresentation.js';
const run={status:'failed',compilation:{nodes:[{id:'gate',tool:{qualifiedName:'builtin.research.workflow.review.gate'}}]}};
test('gate technical failures retry the local gate, not an AI revision',()=>{
 for(const code of ['WORKFLOW_INPUT_BINDING_FAILED','WORKFLOW_TOOL_PROPOSAL_FAILED','TOOL_INVOCATION_FAILED','MODEL_UNAVAILABLE','']){
  const s={nodeId:'gate',status:'failed',errorCode:code,errorMessage:'snapshot error'};
  assert.equal(workflowReviewGateRejected(run,s),false);assert.equal(workflowStepCanRetry(run,s),true);
  assert.equal(toolDecisionLabel({...s,toolName:'builtin.research.workflow.review.gate'}),'');
 }
 for(const code of ['RESEARCH_REVIEW_NOT_APPROVED','TOOL_INVOCATION_FAILED']){
  assert.equal(workflowReviewGateRejected(run,{nodeId:'gate',status:'failed',errorCode:code,errorMessage:'独立审查未通过：待修正'}),true);
  assert.equal(toolDecisionLabel({toolName:'builtin.research.workflow.review.gate',status:'failed',errorCode:code,errorMessage:'独立审查未通过：待修正'}),'审查未通过 · 待修改');
 }
 assert.equal(workflowReviewGateRejected(run,{nodeId:'other',status:'failed',errorCode:'RESEARCH_REVIEW_NOT_APPROVED'}),false);
 assert.equal(workflowReviewGateRejected(run,{nodeId:'gate',status:'completed',errorCode:'RESEARCH_REVIEW_NOT_APPROVED'}),false);
 const app=fs.readFileSync(new URL('./App.tsx',import.meta.url),'utf8');
 assert.match(app,/const reviewGateFailure = recovery \? recovery.kind === "revise" : workflowReviewGateRejected\(detail.run, retryableStep\)/);
 assert.match(app,/重试交付核验/);assert.match(app,/不重新调用模型进行审查/);
 assert.match(app,/runDetail.steps.some\(\(step\) => workflowReviewGateRejected/);
 assert.match(app,/if \(reviewRevision\) setFeedback/);
});
