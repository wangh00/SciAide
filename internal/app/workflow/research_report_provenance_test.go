package workflow

import (
	"strings"
	"testing"
)

func TestReportRetrievalAppendixIsDeterministicAndAudited(t *testing.T) {
	node := CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion + "-implementation-v1", OutputSchema: dynamicReportSchema()}
	input := raw(`{"researchContract":{"query":"planned query"},"researchSourceContext":{"retrieval":{"queries":["actual query"],"sources":[{"sourceId":"pubmed","providerQuery":"actual[Title/Abstract]","status":"ok","count":20},{"sourceId":"arxiv","status":"failed","errorCode":"rate_limited","count":0}],"candidateCount":18,"firstPageOnly":true,"completedAt":"2026-09-16T12:00:00Z"}}}`)
	rawOutput := `{"markdown":"# Findings\nFinding [K-AAAAAAAAAAAA].","claimSummary":["Finding"],"methodSummary":"Observed method","limitations":["Abstract-only evidence"],"confidence":"low"}`
	value, changes, err := normalizeWorkflowAIStageSubmissionForInput(rawOutput, node, input)
	if err != nil || len(changes) != 1 || changes[0].Rule != "append_frozen_retrieval_log" {
		t.Fatalf("%v %+v", err, changes)
	}
	if err := validateWorkflowAIStageOutput(node, value, input); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"planned query", "actual query", "actual[Title/Abstract]", "rate_limited", "2026-09-16", "[K-AAAAAAAAAAAA]", "Abstract-only evidence"} {
		if !strings.Contains(string(value), text) {
			t.Fatal(text)
		}
	}
	again, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(value), node, input)
	if err != nil || len(changes) != 0 || !rawJSONEqual(value, again) {
		t.Fatal("appendix duplicated", err)
	}
	if validateWorkflowAIStageOutput(node, raw(rawOutput), input) == nil {
		t.Fatal("recovery accepted missing appendix")
	}
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(strings.Replace(string(value), "actual query", "tampered", 1), node, input); err == nil {
		t.Fatal("rewritten log accepted")
	}
	legacy := node
	legacy.PromptVersion = "dynamic-report-v4"
	unchanged, changes, err := normalizeWorkflowAIStageSubmissionForInput(rawOutput, legacy, input)
	if err != nil || len(changes) != 0 || !rawJSONEqual(unchanged, raw(rawOutput)) {
		t.Fatal("old task changed")
	}
}

func TestReportProcessChecksPreserveCitationsAndScientificLimits(t *testing.T) {
	node := CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion, OutputSchema: dynamicReportSchema()}
	report := map[string]any{"markdown": "Review of evidence [K-AAAAAAAAAAAA]. 未获取全文，证据覆盖有限。", "claimSummary": []string{}, "methodSummary": "Methods", "limitations": []string{"无法确认样本是否重叠。"}, "confidence": "low"}
	if err := validateReportProvenance(node, mustJSON(report), raw(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"markdown", "claimSummary", "methodSummary", "limitations"} {
		copy := decodeObject(mustJSON(report))
		if field == "claimSummary" || field == "limitations" {
			copy[field] = []string{"显示时可呈现为常规编号引用"}
		} else {
			copy[field] = "显示时可呈现为常规编号引用"
		}
		if validateReportProvenance(node, mustJSON(copy), raw(`{}`)) == nil {
			t.Fatal(field)
		}
	}
	block := retrievalCodeBlock("topic\n```\n# injected")
	if !strings.HasPrefix(block, "````text") {
		t.Fatal(block)
	}
}
