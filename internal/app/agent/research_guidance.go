package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

// ResearchGuidance is trusted host state bound to a research conversation.
// It constrains model-visible tools but never grants permission to execute one.
type ResearchGuidanceProvider interface {
	GuidanceForConversation(ctx context.Context, conversationID string) (workflow.ResearchGuidance, bool, error)
}

func discussionConversationMessages(messages []conversation.Message) []conversation.Message {
	projected := append([]conversation.Message(nil), messages...)
	for i := range projected {
		if projected[i].Internal {
			projected[i].Parts = nil
			projected[i].Citations = nil
			projected[i].Reasoning = nil
		}
	}
	return projected
}

// The live Workflow host can validate input-aware stage rules before a Chat
// Run commits its final answer. Only WORKFLOW_AI_SUBMISSION_INVALID represents
// a correctable model candidate; ownership/storage errors are fatal.
type researchSubmissionValidator interface {
	ValidateResearchSubmission(context.Context, string, string, string, string) (json.RawMessage, error)
}

func researchSubmissionDiagnostic(err error) string {
	parts := []string{err.Error()}
	var fieldFormat *citationFieldRepairFormatError
	if workflow.IsAIStageOutputFormatError(err) || errors.As(err, &fieldFormat) {
		parts = append(parts, "校验类别：阶段提交封装或字段格式。该候选尚未通过完整校验，不代表科学内容已被判定不合格。")
	}
	var classified *apperr.Error
	if errors.As(err, &classified) {
		for _, detail := range []string{classified.UserMessage, classified.Details} {
			if detail = strings.TrimSpace(detail); detail != "" && !strings.Contains(strings.Join(parts, "\n"), detail) {
				parts = append(parts, detail)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// The model needs the complete diagnostic; the conversation error does not.
// Keep source excerpts and repair instructions in ErrorDetails, not the banner.
func researchSubmissionFailureMessage(err error) string {
	message := "科研结果经两次修正仍未通过检查，当前阶段已停止。"
	var assessmentErrors *workflow.CitationAssessmentErrors
	var quoteErrors *workflow.CitationQuoteErrors
	var quoteErr *workflow.CitationQuoteError
	var basisErrors *workflow.ReviewBasisErrors
	var stateErrors *workflow.ReviewFindingStateErrors
	var repairFormat *reviewRepairFormatError
	var editSubmission *revisionEditSubmissionError
	var reviewSelection *reviewRepairSelectionError
	var reviewSource *reviewSourceSubmissionError
	var citationSource *citationSourceSubmissionError
	var quoteRepairFormat *citationRepairFormatError
	var fieldRepairFormat *citationFieldRepairFormatError
	var quoteLength *citationRepairLengthError
	var quoteSelection *citationRepairSelectionError
	var evidenceErrors *workflow.EvidenceSubmissionErrors
	var identity *workflow.LiteratureIdentityErrors
	if recoverableSubmissionText(err, nil) {
		message = "阶段提交修正预算已用尽：模型仍未返回有效的结构化结果。已停止，未接受结果或执行文本中的工具指令；这不代表报告科学审查不合格。"
	} else if errors.As(err, &reviewSource) {
		message = "独立审查的原文选择提交经两次修正仍未符合当前来源或范围约定，当前阶段已停止。最后一次选择尚未进入完整科学校验，不代表报告已被判定不合格；原文、已完成阶段和历史保留。"
	} else if errors.As(err, &editSubmission) {
		message = "局部编辑请求经两次修正仍无法匹配已确认原稿，当前阶段已停止。原稿哈希、文字定位或提交字段不符合本轮约定；最后一次编辑未进入完整科学校验，不代表报告科学审查不合格。已完成阶段与原稿保留。"
	} else if errors.As(err, &identity) {
		message = "文献筛选的候选标识经两次纠正仍未与当前批次对应，当前阶段已停止；不是文献不足或科研审查不通过。已保存的筛选批次和检索结果保留。"
	} else if errors.As(err, &quoteLength) {
		message = fmt.Sprintf("引用选段经两次修正仍超过长度限制，当前阶段已停止。最后一次选段为 %d 字符，上限 %d 字符；不是提交字段混用，也不代表报告科学审查不合格。已完成的材料处理保留。", quoteLength.Actual, quoteLength.Limit)
	} else if errors.As(err, &quoteSelection) {
		message = "引用选段经两次修正仍未能在同一原文中连续定位，当前阶段已停止。已完成的材料处理保留；这不是报告科学审查不合格。"
	} else if errors.As(err, &fieldRepairFormat) {
		message = "综合字段修正的提交格式经两次纠正仍未通过，当前阶段已停止。resultUpdates 字段或原稿基线不符合本轮约定；未接受候选，不代表科学审查不合格，原材料与历史保留。"
	} else if errors.As(err, &quoteRepairFormat) {
		message = "引用修正的提交格式经两次纠正仍未通过，当前阶段已停止。提交字段、引用标识或修正基线不符合本轮约定；已完成的材料处理保留。这不是报告科学审查不合格。"
	} else if errors.As(err, &citationSource) {
		message = "文献综合的原文选择经两次修正仍未符合当前来源、连续范围或长度约定，当前阶段已停止。最后一次选择尚未通过完整校验，不代表报告科学审查不合格；原材料、已完成阶段与历史保留。"
	} else if errors.As(err, &reviewSelection) {
		message = "独立审查的原文选段经两次修正仍不符合来源、连续范围或长度要求，当前阶段已停止。不是提交字段混用，也不代表报告已被判定不合格。"
	} else if errors.As(err, &repairFormat) {
		message = "独立审查的修正提交格式连续两次未通过，当前阶段已停止。这是提交协议错误，不代表报告科学内容被判定不合格。"
	} else if errors.As(err, &basisErrors) {
		message = "独立审查的依据定位经两次修正仍未通过，当前阶段已停止。这是审查提交错误，不代表报告已被判定不合格。"
	} else if errors.As(err, &stateErrors) {
		message = "独立审查的问题说明经两次修正仍未通过，当前阶段已停止。这是审查提交错误，不代表报告已被判定不合格。"
	} else if errors.As(err, &evidenceErrors) {
		message += "证据范围声明和引用摘录仍存在未解决的问题。"
	} else if errors.As(err, &assessmentErrors) {
		message += "推荐引用与证据判定未完整对应，或相关摘录仍有不匹配。"
	} else if errors.As(err, &quoteErrors) {
		message += "多条引用摘录仍与对应原文不匹配。"
	} else if errors.As(err, &quoteErr) {
		message += "引用 " + tool.SafeActivityText(quoteErr.Reference, 80) + " 的摘录与对应原文不匹配。"
	} else if workflow.IsAIStageOutputFormatError(err) {
		message = "阶段提交格式经两次纠正仍未通过，当前阶段已停止。未接受该候选；这不代表报告科学审查不合格，已完成的材料处理保留。"
	} else {
		message += "阶段结果仍不符合输出格式或内容要求。"
	}
	return message + "详细原因已保留在诊断信息中；可重试当前阶段，无需新建任务或重新上传材料。"
}

func filterResearchTools(definitions []tool.Definition, guidance workflow.ResearchGuidance) ([]tool.Definition, map[string]struct{}) {
	allowed := make(map[string]struct{}, len(guidance.AllowedToolNames))
	for _, name := range guidance.AllowedToolNames {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = struct{}{}
		}
	}
	filtered := make([]tool.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if _, ok := allowed[definition.QualifiedName]; ok {
			filtered = append(filtered, definition)
		}
	}
	return filtered, allowed
}

// intersectResearchToolScope keeps the durable Chat Run contract as the hard
// upper bound while accepting a narrower, current-stage Guidance projection.
// Guidance is useful for semantic routing, but it must never expand the tool
// set frozen when the Workflow execution was created.
func intersectResearchToolScope(definitions []tool.Definition, projected, frozen map[string]struct{}) ([]tool.Definition, map[string]struct{}) {
	// An eventually-consistent projection can briefly report an empty allowed
	// list while the durable execution already has its frozen stage scope. Do
	// not turn that transient empty projection into a tool blackout; only a
	// non-empty projection may narrow the frozen set.
	if len(projected) == 0 && len(frozen) > 0 {
		projected = frozen
	}
	allowed := make(map[string]struct{})
	for name := range projected {
		if _, ok := frozen[name]; ok {
			allowed[name] = struct{}{}
		}
	}
	filtered := make([]tool.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if _, ok := allowed[definition.QualifiedName]; ok {
			filtered = append(filtered, definition)
		}
	}
	return filtered, allowed
}

func researchToolAllowed(allowed map[string]struct{}, name string) bool {
	_, ok := allowed[strings.TrimSpace(name)]
	return ok
}
