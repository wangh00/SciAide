package wails

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFrozenWorkflowDeliverableBuildsReviewedResearchReport(t *testing.T) {
	raw := json.RawMessage(`{"markdown":"# 睡眠数据实证报告\n\n结论正文。","claimSummary":["结论"],"methodSummary":"冻结 Python 分析","limitations":["样本量有限"],"confidence":"medium"}`)
	name, markdown, err := frozenWorkflowDeliverable("report_draft", "动态研究路线", raw)
	if err != nil {
		t.Fatal(err)
	}
	if name != "睡眠数据实证报告" || !strings.Contains(markdown, "结论正文") {
		t.Fatalf("report deliverable = name %q, markdown %q", name, markdown)
	}
}

func TestFrozenWorkflowDeliverableRejectsIncompleteResearchReport(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"markdown":"","claimSummary":[],"methodSummary":"method","limitations":[],"confidence":"medium"}`),
		json.RawMessage(`{"markdown":"# report","claimSummary":[],"methodSummary":"","limitations":[],"confidence":"medium"}`),
		json.RawMessage(`{"markdown":"# report","claimSummary":[],"methodSummary":"method","limitations":[],"confidence":"certain"}`),
	} {
		if _, _, err := frozenWorkflowDeliverable("report_draft", "route", raw); err == nil {
			t.Fatalf("incomplete report was accepted: %s", raw)
		}
	}
}
