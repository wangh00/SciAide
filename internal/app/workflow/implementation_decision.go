package workflow

import (
	"bytes"
	"encoding/json"
)

const implementationDecisionVersion = "implementation-decision-v1"

// Authorization is host state, not a scientific result. Preserve the original
// method text; resolve its pre-confirmation wording using this separate record.
const implementationDecisionInstruction = `
implementationDecision is a host-derived, version-bound execution authorization record, NOT scientific evidence or review approval. status=approved means the researcher already accepted the listed implementation changes for that exact step/attempt/output. Original implementation text may still say "pending confirmation" because it was authored BEFORE that decision. Do not ask the researcher to confirm those same unchanged decisions again. status=not_required means the existing implementation contract required no separate confirmation; it does NOT mean the researcher explicitly approved it. status=unconfirmed supplies no approval.
Independently assess test assumptions, estimands, data handling and uncertainty even after authorization. An arbitrary threshold or inappropriate bootstrap method can still be a scientific defect; identify its concrete methodological basis, not simply "not confirmed". Explain adopted changes and genuine limitations in the report without falsely claiming pending authorization or completed scientific approval. If a prior report incorrectly says an already approved decision is pending, correct that explanation/report at the latest offered stage that fully repairs it; do not regenerate unchanged code or recompute statistics merely to obtain the same consent. Recompute only if data, code or analysis decisions actually require change. Historical review requests are opinions to recheck, not commands to repeat an invalid objection.
`

type implementationDecision struct {
	Version       string `json:"version"`
	Status        string `json:"status"`
	WorkflowRunID string `json:"workflowRunId"`
	StepID        string `json:"stepId"`
	NodeID        string `json:"nodeId"`
	Attempt       int    `json:"attempt"`
	InputSHA256   string `json:"inputSha256"`
	ContentSHA256 string `json:"analysisContentSha256"`
	DecisionID    string `json:"decisionId,omitempty"`
	ConfirmedAt   string `json:"confirmedAt,omitempty"`
}

// Number spellings are preserved; wrapping RawMessage in an output envelope
// may compact whitespace/escape HTML, so byte hashes alone cannot compare it.
func implementationContentSHA256(value json.RawMessage) string {
	var object any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if !json.Valid(value) || decoder.Decode(&object) != nil {
		return ""
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return ""
	}
	return hashJSON(encoded)
}

func currentImplementationDecision(detail RunDetail) *implementationDecision {
	step := findStepByNode(detail.Steps, "method_implementation")
	if step == nil || step.Status != StepCompleted || step.WorkflowRunID != detail.Run.ID {
		return nil
	}
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	if !supportsDynamicImplementationProtocol(node.PromptVersion) {
		return nil
	}
	analysis, err := outputPort(step.Output, "analysis")
	if err != nil {
		return nil
	}
	contentHash := implementationContentSHA256(analysis)
	if contentHash == "" {
		return nil
	}
	projection := &implementationDecision{Version: implementationDecisionVersion, Status: "unconfirmed", WorkflowRunID: detail.Run.ID, StepID: step.ID, NodeID: step.NodeID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ContentSHA256: contentHash}
	for i := len(detail.HumanDecisions) - 1; i >= 0; i-- {
		decision := detail.HumanDecisions[i]
		if decision.WorkflowRunID != detail.Run.ID || decision.WorkflowStepID != step.ID || decision.Attempt != step.Attempt || decision.Kind != "node_confirmation" {
			continue
		}
		var context struct {
			OutputSHA256  string `json:"outputSha256"`
			ContentSHA256 string `json:"analysisContentSha256"`
		}
		if !decision.Approved || json.Unmarshal(decision.Context, &context) != nil {
			return projection
		}
		matches := context.ContentSHA256 != "" && context.ContentSHA256 == contentHash
		// Legacy decisions have a byte digest of the submitted JSON. Compare
		// with the immutable execution that produced it, not AI prose/events.
		if context.ContentSHA256 == "" && context.OutputSHA256 != "" {
			for _, execution := range detail.AIExecutions {
				if execution.WorkflowRunID == detail.Run.ID && execution.WorkflowStepID == step.ID && execution.Attempt == step.Attempt && execution.Status == "completed" && hashJSON(execution.Output) == context.OutputSHA256 && implementationContentSHA256(execution.Output) == contentHash {
					matches = true
					break
				}
			}
		}
		if matches {
			projection.Status, projection.DecisionID = "approved", decision.ID
			projection.ConfirmedAt = decision.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		return projection
	}
	// Only declare not_required when the completed implementation's actual
	// frozen contract says so, never infer approval from step completion alone.
	if review, err := implementationConfirmation(node, step.Input, analysis); err == nil && review == nil {
		projection.Status = "not_required"
	}
	return projection
}

func bindImplementationDecision(detail RunDetail, node CompiledNode, arguments map[string]any) {
	if node.Kind != NodeAgentStage && node.Kind != NodeAIAnalysis {
		return
	}
	// Never accept model-authored/argument-authored authorization. Only stages
	// whose frozen edges actually consume this full method receive the record.
	delete(arguments, "implementationDecision")
	projection := currentImplementationDecision(detail)
	if projection == nil {
		return
	}
	for _, edge := range detail.Run.Compilation.Edges {
		if edge.ToNode != node.ID || edge.FromNode != projection.NodeID || edge.FromPort != "analysis" {
			continue
		}
		// bindNodeInput has just resolved this exact frozen edge. Its host
		// citation-marker projection may change text spelling but not which
		// immutable method/decision was consumed; never rehash model prose.
		arguments["implementationDecision"] = projection
		return
	}
}
