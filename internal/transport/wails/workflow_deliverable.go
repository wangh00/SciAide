package wails

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type SaveWorkflowDeliverableRequest struct {
	ProjectID  string `json:"projectId"`
	RunID      string `json:"runId"`
	OutputName string `json:"outputName"`
}

type workflowResearchDesign struct {
	Title                 string   `json:"title"`
	ResearchQuestion      string   `json:"researchQuestion"`
	Objectives            []string `json:"objectives"`
	Hypotheses            []string `json:"hypotheses"`
	PopulationAndSampling []string `json:"populationAndSampling"`
	VariablesOrMaterials  []string `json:"variablesOrMaterials"`
	DataCollectionPlan    []string `json:"dataCollectionPlan"`
	AnalysisPlan          []string `json:"analysisPlan"`
	QualityControls       []string `json:"qualityControls"`
	EthicsAndRisks        []string `json:"ethicsAndRisks"`
	Milestones            []string `json:"milestones"`
	EvidenceGaps          []string `json:"evidenceGaps"`
	Limitations           []string `json:"limitations"`
	Status                string   `json:"status"`
}

type workflowDeliveryGate struct {
	Approved bool `json:"approved"`
}

type workflowResearchReport struct {
	Markdown      string   `json:"markdown"`
	ClaimSummary  []string `json:"claimSummary"`
	MethodSummary string   `json:"methodSummary"`
	Limitations   []string `json:"limitations"`
	Confidence    string   `json:"confidence"`
}

func (f *WorkflowFacade) SaveDeliverable(request SaveWorkflowDeliverableRequest) (artifact.SaveResult, error) {
	if f.artifacts == nil {
		return artifact.SaveResult{}, fmt.Errorf("Workflow Artifact service is not configured")
	}
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.OutputName = strings.TrimSpace(request.OutputName)
	if request.ProjectID == "" || request.RunID == "" {
		return artifact.SaveResult{}, fmt.Errorf("project and Workflow Run are required")
	}
	if request.OutputName != "research_design" && request.OutputName != "report_draft" {
		return artifact.SaveResult{}, fmt.Errorf("unsupported Workflow deliverable")
	}
	detail, err := f.runtime.Get(f.lifecycle.Context(), request.ProjectID, request.RunID)
	if err != nil {
		return artifact.SaveResult{}, err
	}
	if detail.Run.ProjectID != request.ProjectID || detail.Run.Status != workflow.RunCompleted {
		return artifact.SaveResult{}, fmt.Errorf("Workflow deliverable is not completed")
	}
	if err := workflow.ValidateReviewedOutput(detail, request.OutputName); err != nil {
		return artifact.SaveResult{}, err
	}
	var outputs map[string]json.RawMessage
	if err := json.Unmarshal(detail.Run.Outputs, &outputs); err != nil {
		return artifact.SaveResult{}, fmt.Errorf("decode frozen Workflow outputs: %w", err)
	}
	rawOutput := outputs[request.OutputName]
	if len(rawOutput) == 0 {
		return artifact.SaveResult{}, fmt.Errorf("Workflow output does not contain %s", request.OutputName)
	}
	var gate workflowDeliveryGate
	if rawGate := outputs["delivery_gate"]; len(rawGate) == 0 || json.Unmarshal(rawGate, &gate) != nil || !gate.Approved {
		return artifact.SaveResult{}, fmt.Errorf("Workflow deliverable has not passed its delivery gate")
	}
	name, markdown, err := frozenWorkflowDeliverable(request.OutputName, detail.Run.WorkflowName, rawOutput)
	if err != nil {
		return artifact.SaveResult{}, err
	}
	if assessment := detail.DeliveryAssessment; assessment != nil && assessment.Status == "reviewed" {
		markdown += "\n\n---\n\n## 交付验收说明\n\n"
		markdown += "- 交付类型：" + assessment.Label + "。\n"
		markdown += "- 已核对交付内容与本次独立审查、门禁的冻结快照一致。\n"
		if len(assessment.Checks) > 0 {
			markdown += fmt.Sprintf("- 本轮研究约定包含 %d 项验收条件，均已有 AI 辅助核对记录。\n", len(assessment.Checks))
		}
		markdown += "- AI 审查与可复现记录不等于科学结论已被独立验证；研究设计或综述不能据此视为已经完成实证研究。\n"
	}
	digest := sha256.Sum256(rawOutput)
	var citations []artifact.Citation
	if rawCitations := outputs["citations"]; len(rawCitations) > 0 {
		if err := json.Unmarshal(rawCitations, &citations); err != nil {
			return artifact.SaveResult{}, fmt.Errorf("decode frozen research citations: %w", err)
		}
	}
	return f.artifacts.SaveWorkflowDeliverable(f.lifecycle.Context(), artifact.WorkflowDeliverableCommand{
		ProjectID:      request.ProjectID,
		WorkflowRunID:  request.RunID,
		ResearchTaskID: detail.Run.ResearchTaskID,
		OutputName:     request.OutputName,
		OutputSHA256:   hex.EncodeToString(digest[:]),
		Name:           name,
		Markdown:       markdown,
		Citations:      citations,
	})
}

func frozenWorkflowDeliverable(outputName, workflowName string, raw json.RawMessage) (string, string, error) {
	switch outputName {
	case "research_design":
		var design workflowResearchDesign
		if err := json.Unmarshal(raw, &design); err != nil {
			return "", "", fmt.Errorf("decode frozen research design: %w", err)
		}
		if design.Status != "research_design_not_empirical_result" || strings.TrimSpace(design.Title) == "" || strings.TrimSpace(design.ResearchQuestion) == "" {
			return "", "", fmt.Errorf("Workflow research design is incomplete or has an invalid delivery status")
		}
		return strings.TrimSpace(design.Title), renderWorkflowResearchDesign(design), nil
	case "report_draft":
		var report workflowResearchReport
		if err := json.Unmarshal(raw, &report); err != nil {
			return "", "", fmt.Errorf("decode frozen research report: %w", err)
		}
		report.Markdown = strings.TrimSpace(report.Markdown)
		report.MethodSummary = strings.TrimSpace(report.MethodSummary)
		if report.Markdown == "" || report.MethodSummary == "" || !validReportConfidence(report.Confidence) {
			return "", "", fmt.Errorf("Workflow research report is incomplete")
		}
		return workflowReportTitle(report.Markdown, workflowName), report.Markdown, nil
	default:
		return "", "", fmt.Errorf("unsupported Workflow deliverable")
	}
}

func validReportConfidence(value string) bool {
	switch strings.TrimSpace(value) {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func workflowReportTitle(markdown, workflowName string) string {
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") && strings.TrimSpace(strings.TrimPrefix(line, "# ")) != "" {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	if workflowName = strings.TrimSpace(workflowName); workflowName != "" {
		return workflowName + " - 研究报告"
	}
	return "科研 Workflow 研究报告"
}

func renderWorkflowResearchDesign(design workflowResearchDesign) string {
	var result strings.Builder
	result.WriteString("# ")
	result.WriteString(strings.TrimSpace(design.Title))
	result.WriteString("\n\n> 交付类型：研究设计。该文档不是数据采集、模型训练或统计分析后的实证结果。\n")
	writeWorkflowDesignText(&result, "研究问题", design.ResearchQuestion)
	writeWorkflowDesignList(&result, "研究目标", design.Objectives)
	writeWorkflowDesignList(&result, "研究假设", design.Hypotheses)
	writeWorkflowDesignList(&result, "研究人群与采样", design.PopulationAndSampling)
	writeWorkflowDesignList(&result, "变量与材料", design.VariablesOrMaterials)
	writeWorkflowDesignList(&result, "数据采集计划", design.DataCollectionPlan)
	writeWorkflowDesignList(&result, "分析计划", design.AnalysisPlan)
	writeWorkflowDesignList(&result, "质量控制", design.QualityControls)
	writeWorkflowDesignList(&result, "伦理与风险", design.EthicsAndRisks)
	writeWorkflowDesignList(&result, "实施里程碑", design.Milestones)
	writeWorkflowDesignList(&result, "证据缺口", design.EvidenceGaps)
	writeWorkflowDesignList(&result, "局限", design.Limitations)
	return strings.TrimSpace(result.String())
}

func writeWorkflowDesignText(result *strings.Builder, title, value string) {
	if value = strings.TrimSpace(value); value != "" {
		result.WriteString("\n## ")
		result.WriteString(title)
		result.WriteString("\n\n")
		result.WriteString(value)
		result.WriteByte('\n')
	}
}

func writeWorkflowDesignList(result *strings.Builder, title string, values []string) {
	if len(values) == 0 {
		return
	}
	result.WriteString("\n## ")
	result.WriteString(title)
	result.WriteString("\n\n")
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result.WriteString("- ")
			result.WriteString(value)
			result.WriteByte('\n')
		}
	}
}
