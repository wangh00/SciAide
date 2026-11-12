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

func TestFrozenDesignPreservesConcreteMaterialsAndLegacyOutput(t *testing.T) {
	for _, title := range []string{"问卷初稿", "访谈提纲", "实验协议"} {
		body := "题干与选项\n\n|步骤|内容|\n|1|实际操作|\n\n未验证草稿"
		raw, _ := json.Marshal(workflowResearchDesign{Title: "研究设计", ResearchQuestion: "研究问题", Status: "research_design_not_empirical_result", Deliverables: []workflowDesignMaterial{{Title: title, Markdown: body}}})
		_, markdown, err := frozenWorkflowDeliverable("research_design", "设计", raw)
		if err != nil || !strings.Contains(markdown, body) || !strings.Contains(markdown, "## "+title) {
			t.Fatalf("concrete material lost: %v %s", err, markdown)
		}
	}
	_, markdown, err := frozenWorkflowDeliverable("research_design", "旧设计", json.RawMessage(`{"title":"旧设计","researchQuestion":"研究问题","status":"research_design_not_empirical_result"}`))
	if err != nil || !strings.Contains(markdown, "研究问题") {
		t.Fatal(err)
	}
}
