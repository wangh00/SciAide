package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/tool"
)

type ResearchDeliveryAssessment struct {
	Status      string                    `json:"status"`
	Kind        string                    `json:"kind"`
	Label       string                    `json:"label"`
	Summary     string                    `json:"summary"`
	Checks      []ResearchAcceptanceCheck `json:"checks,omitempty"`
	Limitations []string                  `json:"limitations,omitempty"`
}

// ValidateReviewedOutput prevents a completed flag or an unrelated approved
// gate from authorizing export. Follow the frozen edges all the way from the
// requested output to its producer, review attempt, and gate input/result.
func ValidateReviewedOutput(detail RunDetail, name string) error {
	if detail.Run.Status != RunCompleted {
		return fmt.Errorf("科研任务尚未完成，不能登记最终交付物")
	}
	var outputs map[string]json.RawMessage
	if json.Unmarshal(detail.Run.Outputs, &outputs) != nil || len(outputs[name]) == 0 {
		return fmt.Errorf("科研任务没有所选交付输出")
	}
	for _, declared := range detail.Run.Compilation.Outputs {
		value, exists := outputs[declared.Name]
		if !exists && !declared.Required {
			continue
		}
		source := findStepByNode(detail.Steps, declared.FromNode)
		if source == nil || source.Status != StepCompleted {
			return fmt.Errorf("交付输出 %s 的来源尚未完成", declared.Name)
		}
		frozen, err := outputPort(source.Output, declared.FromPort)
		if err != nil || !rawJSONEqual(value, frozen) {
			return fmt.Errorf("交付输出 %s 与阶段快照不一致", declared.Name)
		}
		if compilationNodeMap(detail.Run.Compilation)[source.NodeID].Kind == NodePython {
			computed, err := outputPort(source.Output, "structured")
			var state struct {
				Status string `json:"status"`
			}
			if err != nil || json.Unmarshal(computed, &state) != nil || state.Status != "success" {
				return fmt.Errorf("交付输出 %s 没有成功的 Python 计算记录", declared.Name)
			}
		}
	}
	declaration, found := researchOutputDeclaration(detail.Run.Compilation, name)
	if !found {
		return fmt.Errorf("交付输出未在冻结方案中声明")
	}
	producer := findStepByNode(detail.Steps, declaration.FromNode)
	if producer == nil || producer.Status != StepCompleted {
		return fmt.Errorf("交付内容的产出阶段尚未完成")
	}
	produced, err := outputPort(producer.Output, declaration.FromPort)
	if err != nil || !rawJSONEqual(produced, outputs[name]) {
		return fmt.Errorf("交付内容与产出阶段的冻结结果不一致")
	}
	gateDeclaration, found := researchOutputDeclaration(detail.Run.Compilation, "delivery_gate")
	if !found {
		return fmt.Errorf("交付物缺少冻结的交付门禁")
	}
	nodes := compilationNodeMap(detail.Run.Compilation)
	gate := findStepByNode(detail.Steps, gateDeclaration.FromNode)
	if gate == nil || gate.Status != StepCompleted || !isReviewGateNode(nodes[gate.NodeID]) {
		return fmt.Errorf("交付门禁尚未完成")
	}
	gateOutput, err := outputPort(gate.Output, gateDeclaration.FromPort)
	if err != nil || !rawJSONEqual(gateOutput, outputs["delivery_gate"]) {
		return fmt.Errorf("交付门禁输出快照不一致")
	}
	var review *Step
	for _, edge := range detail.Run.Compilation.Edges {
		if edge.ToNode == gate.NodeID && edge.ToPort == "review" {
			review = findStepByNode(detail.Steps, edge.FromNode)
			break
		}
	}
	if review == nil || review.Status != StepCompleted || !isIndependentReviewSchema(nodes[review.NodeID].OutputSchema) {
		return fmt.Errorf("交付物缺少已完成的独立审查")
	}
	if review.InputSHA256 == "" || hashJSON(review.Input) != review.InputSHA256 || gate.InputSHA256 == "" || hashJSON(gate.Input) != gate.InputSHA256 {
		return fmt.Errorf("独立审查或门禁输入摘要不一致")
	}
	if err := validateReviewDependencies(detail, *review); err != nil {
		return err
	}
	reviewOutput, err := outputPort(review.Output, "analysis")
	if err != nil {
		return err
	}
	var execution *AIExecution
	for i := range detail.AIExecutions {
		candidate := &detail.AIExecutions[i]
		if candidate.WorkflowStepID == review.ID && candidate.Attempt == review.Attempt && candidate.Status == "completed" {
			execution = candidate
		}
	}
	if execution == nil || execution.InputSHA256 != review.InputSHA256 || execution.OutputSHA256 == "" || hashJSON(execution.Output) != execution.OutputSHA256 || !rawJSONEqual(execution.Output, reviewOutput) {
		return fmt.Errorf("独立审查与当前尝试的 AI 冻结输出不一致")
	}
	if err := (tool.JSONSchemaValidator{}).Validate(nodes[review.NodeID].OutputSchema, reviewOutput); err != nil {
		return fmt.Errorf("独立审查不符合冻结契约：%w", err)
	}
	if err := validateWorkflowAIStageOutput(nodes[review.NodeID], reviewOutput, review.Input); err != nil {
		return err
	}
	var decision struct {
		Approved  bool   `json:"approved"`
		InputHash string `json:"reviewedInputSha256"`
	}
	if json.Unmarshal(reviewOutput, &decision) != nil || !decision.Approved || decision.InputHash != review.InputSHA256 {
		return fmt.Errorf("独立审查没有批准当前冻结输入")
	}
	var gateInput struct {
		Subject json.RawMessage `json:"subject"`
		Review  json.RawMessage `json:"review"`
	}
	if json.Unmarshal(gate.Input, &gateInput) != nil || !reviewSubjectMatchesInput(gateInput.Subject, review.Input) {
		return fmt.Errorf("门禁未覆盖当前独立审查的完整输入")
	}
	core := reviewOutput
	if isResearchAcceptanceSchema(nodes[review.NodeID].OutputSchema) || schemaDeclaresProperty(nodes[review.NodeID].OutputSchema, "revisionPlan") {
		core, err = reviewCoreForGate(reviewOutput)
		if err != nil {
			return err
		}
	}
	if !rawJSONEqual(core, gateInput.Review) {
		return fmt.Errorf("门禁引用了过期或不同的审查结果")
	}
	var approval struct {
		Approved  bool   `json:"approved"`
		InputHash string `json:"reviewedInputSha256"`
	}
	if json.Unmarshal(gateOutput, &approval) != nil || !approval.Approved || approval.InputHash != review.InputSHA256 {
		return fmt.Errorf("门禁未批准当前审查输入")
	}
	var reviewInput map[string]json.RawMessage
	if json.Unmarshal(review.Input, &reviewInput) != nil {
		return fmt.Errorf("独立审查输入无效")
	}
	for _, edge := range detail.Run.Compilation.Edges {
		if edge.ToNode == review.NodeID && edge.FromNode == declaration.FromNode && edge.FromPort == declaration.FromPort && rawJSONEqual(reviewInput[edge.ToPort], outputs[name]) {
			return nil
		}
	}
	return fmt.Errorf("所选交付内容不在这次独立审查的输入范围内，请重新审查后登记")
}

func reviewSubjectMatchesInput(subject, input json.RawMessage) bool {
	if rawJSONEqual(subject, input) {
		return true
	}
	// Older gate bindings passed only the context value. Accept that exact
	// historical envelope, but never discard additional evidence/contract fields.
	var envelope map[string]json.RawMessage
	return json.Unmarshal(input, &envelope) == nil && len(envelope) == 1 && rawJSONEqual(envelope["context"], subject)
}

func researchOutputDeclaration(compilation Compilation, name string) (Output, bool) {
	for _, output := range compilation.Outputs {
		if output.Name == name {
			return output, true
		}
	}
	return Output{}, false
}

func assessResearchDelivery(detail RunDetail) *ResearchDeliveryAssessment {
	if _, exists := researchOutputDeclaration(detail.Run.Compilation, "delivery_gate"); !exists {
		return nil
	}
	assessment := &ResearchDeliveryAssessment{Status: "pending", Kind: "research_draft", Label: "研究交付稿", Summary: "任务尚未完成；中间结果不能作为最终交付。"}
	var outputs map[string]json.RawMessage
	_ = json.Unmarshal(detail.Run.Outputs, &outputs)
	if _, hasDesign := researchOutputDeclaration(detail.Run.Compilation, "research_design"); hasDesign {
		assessment.Kind, assessment.Label = "research_design", "研究设计"
	}
	if _, hasAnalysis := researchOutputDeclaration(detail.Run.Compilation, "analysis"); hasAnalysis {
		assessment.Kind, assessment.Label = "analysis_result", "计算分析结果"
	} else if _, hasCitations := researchOutputDeclaration(detail.Run.Compilation, "citations"); hasCitations && assessment.Kind != "research_design" {
		assessment.Kind, assessment.Label = "evidence_draft", "证据综述稿"
	}
	if detail.Run.Status != RunCompleted {
		if detail.Run.Status == RunFailed || detail.Run.Status == RunInterrupted {
			assessment.Status = "blocked"
			assessment.Summary = "流程尚未通过交付检查，请处理失败或中断后继续。"
			for _, step := range detail.Steps {
				if step.Status == StepFailed && isReviewGateNode(compilationNodeMap(detail.Run.Compilation)[step.NodeID]) {
					assessment.Status = "revision_required"
					assessment.Summary = "独立审查未通过，需要修订结果并重新审查。"
				}
			}
		}
		return assessment
	}
	name := ""
	for _, candidate := range []string{"report_draft", "research_design", "interpretation"} {
		if len(outputs[candidate]) > 0 {
			name = candidate
			break
		}
	}
	if name == "" {
		return nil
	} // Other templates retain their own publication contract.
	if err := ValidateReviewedOutput(detail, name); err != nil {
		assessment.Status, assessment.Summary = "unverified", err.Error()
		return assessment
	}
	assessment.Status = "reviewed"
	assessment.Summary = "交付内容与本次独立审查及门禁快照一致；AI 审查不等于科学结论已被独立验证。"
	var review struct {
		Checks      []ResearchAcceptanceCheck `json:"acceptanceChecks"`
		Limitations []string                  `json:"limitations"`
	}
	_ = json.Unmarshal(outputs["independent_review"], &review)
	assessment.Checks, assessment.Limitations = review.Checks, review.Limitations
	return assessment
}
