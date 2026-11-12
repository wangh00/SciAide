package workflow

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// This is derived from the confirmed replay suffix, never from intent keywords
// or a model's claim that a change is "wording only". Unknown/custom suffixes
// retain the complete existing baseline context.
func reportOnlyRevision(detail RunDetail, revision ResearchRevisionProposal) bool {
	if revision.NodeID != "report_drafting" || revision.RepeatsSideEffects || revision.DataVersion != nil || revision.SourceVersion != nil || len(revision.Materials) > 0 {
		return false
	}
	start := findStepByNode(detail.Steps, revision.NodeID)
	if start == nil {
		return false
	}
	// A later independently confirmed repair may reopen methods/data while
	// the older negotiated wording requirements are still carried forward.
	// Retain those requirements, but never label newly recomputed results as
	// unchanged or discard the comparison baseline for that broader repair.
	for i := len(detail.Events) - 1; i >= 0; i-- {
		event := detail.Events[i]
		if event.Type == "workflow.user_revision_queued" {
			break
		}
		if event.Type != "workflow.review_revision_queued" && event.Type != "workflow.upstream_revision_queued" {
			continue
		}
		var repair struct {
			NodeID string `json:"producerNodeId"`
		}
		if json.Unmarshal(event.Payload, &repair) != nil {
			return false
		}
		producer := findStepByNode(detail.Steps, repair.NodeID)
		if producer == nil || producer.Ordinal < start.Ordinal {
			return false
		}
	}
	nodes := compilationNodeMap(detail.Run.Compilation)
	producer, ok := nodes[start.NodeID]
	if !ok || !usesReportProvenance(producer) || (producer.Kind != NodeAgentStage && producer.Kind != NodeAIAnalysis) {
		return false
	}
	for _, step := range detail.Steps {
		if step.Ordinal < start.Ordinal {
			continue
		}
		node, found := nodes[step.NodeID]
		if !found || node.SideEffect {
			return false
		}
		if step.NodeID == start.NodeID {
			continue
		}
		if ((node.Kind == NodeAIAnalysis || node.Kind == NodeAgentStage) && isIndependentReviewSchema(node.OutputSchema)) || isReviewGateNode(node) {
			continue
		}
		return false
	}
	return true
}

// A stage output may contain the entire structured report twice: in analysis
// and in text as the exact JSON submission. Remove only that proven duplicate,
// not distinct commentary or extra fields. Full raw output stays in the saved
// revision snapshot and event. Number spellings survive this comparison.
func reportRevisionPriorOutput(prior json.RawMessage) json.RawMessage {
	if !json.Valid(prior) || rejectIsolationDuplicateKeys(prior) != nil {
		return prior
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(prior, &record) != nil || len(record["analysis"]) == 0 {
		return prior
	}
	var text string
	if json.Unmarshal(record["text"], &text) != nil {
		return prior
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "\n```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
	} else if strings.HasPrefix(text, "```json\r\n") && strings.HasSuffix(text, "\r\n```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\r\n"), "\r\n```")
	}
	canonical := func(raw []byte) []byte {
		if !json.Valid(raw) || rejectIsolationDuplicateKeys(raw) != nil {
			return nil
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return nil
		}
		var tail any
		if decoder.Decode(&tail) != io.EOF {
			return nil
		}
		encoded, _ := json.Marshal(value)
		return encoded
	}
	analysis, submission := canonical(record["analysis"]), canonical([]byte(text))
	if analysis == nil || submission == nil || !bytes.Equal(analysis, submission) {
		return prior
	}
	delete(record, "text")
	encoded, err := json.Marshal(record)
	if err != nil {
		return prior
	}
	return encoded
}

func userRevisionStageContext(detail RunDetail, node CompiledNode, revision ResearchRevisionProposal) map[string]any {
	value := map[string]any{"proposalId": revision.ID, "summary": revision.Summary, "changes": revision.Changes, "reason": revision.Reason, "startNodeId": revision.NodeID, "affectedStages": revision.AffectedStages}
	if revision.WriteContractVersion != "" {
		value["editTargets"], value["writeContract"] = revision.EditTargets, revision.WriteContract
		value["writeContractVersion"], value["writeContractSha256"] = revision.WriteContractVersion, revision.WriteContractSHA256
		if revision.DesignWriteIsolationVersion != "" {
			value["designWriteIsolationVersion"] = revision.DesignWriteIsolationVersion
			value["designWriteIsolationInstruction"] = designWriteIsolationInstruction
		}
	}
	reportOnly := reportOnlyRevision(detail, revision)
	if prior := priorUserRevisionOutput(detail, node.ID); len(prior) > 0 {
		prior = reportRevisionPriorOutput(prior)
		value["priorOutput"] = prior
	}
	if reportOnly {
		value["reuseContext"] = map[string]any{"kind": "report_only", "revisionId": revision.ID, "currentUpstreamUnchanged": true}
	} else if node.ID == "method_implementation" || node.ID == "result_interpretation" || node.ID == "report_drafting" || node.ID == "independent_review" || node.ID == "research_design" || node.ID == "evidence_screening" {
		if baseline := priorResearchComparison(detail); len(baseline) > 0 {
			value["comparisonBaseline"] = revisionComparisonProjection(baseline)
		}
	}
	return value
}

// Projection only: keep every stage and distinct commentary, but do not send
// a second identical JSON serialization of the same complete analysis object.
// The original event and immutable revision snapshot are never rewritten.
func revisionComparisonProjection(baseline json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	var stages []map[string]json.RawMessage
	if json.Unmarshal(baseline, &fields) != nil || json.Unmarshal(fields["stages"], &stages) != nil {
		return baseline
	}
	for _, stage := range stages {
		if len(stage["output"]) > 0 {
			stage["output"] = reportRevisionPriorOutput(stage["output"])
		}
	}
	encoded, err := json.Marshal(stages)
	if err != nil {
		return baseline
	}
	fields["stages"] = encoded
	encoded, err = json.Marshal(fields)
	if err != nil {
		return baseline
	}
	return encoded
}

const reportRevisionReuseInstruction = `
<report_only_revision_input version="report-only-revision-input-v1">
The host-confirmed replay suffix starts at report drafting and includes only report/review/delivery checks. No upstream computation or evidence stage is reopened. The unchanged current upstream methods, results, artifacts, evidence, requirements and review criteria remain fully supplied in stage_input; they are not a summary or a new approval. For the report, priorOutput retains the full previous structured draft; only an exactly duplicate JSON submission text may be omitted. The repeated historical comparisonBaseline is not included in this report-only execution. The original output and complete pre-confirmation snapshot remain in host history; reuseContext.revisionId identifies that snapshot for a targeted revision_stage_input/revision_stage_output read when the stage already exposes task.read. This metadata grants no new permission.
The host keeps the frozen researchSourceContext.bibliography candidate rows visible for this report-only correction. Use those rows directly for the required inventory; do not page the same bibliography again unless a concrete field is absent from the visible rows.
"Fully supplied" refers to each existing bound field, not to every original file's contents. computedResults may be only the Kernel summary; sourceArtifacts is a file/hash inventory, not the file data. When an exact numeric operand or relevant source passage is missing from visible current fields, read that actual result/source through the exposed resource tools and finish its necessary pages. Never treat a summary, hash or unchanged status as verification of a number or method.
Use current executed machine-readable results, not old report prose or proposal examples, for derived numeric statements. Verify the changed claims and the complete agreed deliverable independently. Additional source/Skill reads must resolve a concrete evidence, method or provenance question, not recreate every unchanged file or compare duplicate baselines. Do not skip scientific checks or unresolved findings; there is no tool-count quota. A report-only change cannot repair an actual upstream numeric/method defect: report it and identify the responsible offered repair stage instead of silently correcting reusable results or re-running Python.
</report_only_revision_input>
`

func reportRevisionReusePrompt(detail RunDetail, node CompiledNode, input map[string]json.RawMessage) string {
	var revision struct {
		ProposalID string `json:"proposalId"`
		Reuse      struct {
			Kind       string `json:"kind"`
			RevisionID string `json:"revisionId"`
		} `json:"reuseContext"`
	}
	confirmed := pendingUserRevision(detail, node.ID)
	if json.Unmarshal(input["_userRevision"], &revision) == nil && revision.Reuse.Kind == "report_only" && confirmed != nil && confirmed.ID != "" && revision.ProposalID == confirmed.ID && revision.Reuse.RevisionID == confirmed.ID && reportOnlyRevision(detail, *confirmed) {
		return reportRevisionReuseInstruction
	}
	return ""
}

func researchReportScopePrompt(node CompiledNode) string {
	if node.ID == "report_drafting" || node.ID == "draft_report" || isIndependentReviewSchema(node.OutputSchema) {
		return researchReportScopeInstruction + researchLiteratureAssessmentInstruction
	}
	return ""
}

// Old claim-based generations remain identifiable, including a later marker
// after their assessment marker. Never regrade or rewrite historical reports.
func hasClaimAssessmentVersion(version string) bool {
	for _, marker := range []string{"claim-assessment-v1", "claim-assessment-v2", "claim-assessment-v3", claimAssessmentVersion} {
		if strings.HasSuffix(version, "-"+marker) || strings.Contains(version, "-"+marker+"-") {
			return true
		}
	}
	return false
}
