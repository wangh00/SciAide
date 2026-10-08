package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

// ResearchGuidance is trusted host state bound to a research conversation.
// It constrains model-visible tools but never grants permission to execute one.
type ResearchGuidanceProvider interface {
	GuidanceForConversation(ctx context.Context, conversationID string) (workflow.ResearchGuidance, bool, error)
}

// The live Workflow host can validate input-aware stage rules before a Chat
// Run commits its final answer. Only WORKFLOW_AI_SUBMISSION_INVALID represents
// a correctable model candidate; ownership/storage errors are fatal.
type researchSubmissionValidator interface {
	ValidateResearchSubmission(context.Context, string, string, string, string) (json.RawMessage, error)
}

func researchSubmissionDiagnostic(err error) string {
	parts := []string{err.Error()}
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
	var quoteErrors *workflow.CitationQuoteErrors
	var quoteErr *workflow.CitationQuoteError
	if errors.As(err, &quoteErrors) {
		message += "多条引用摘录仍与对应原文不匹配。"
	} else if errors.As(err, &quoteErr) {
		message += "引用 " + tool.SafeActivityText(quoteErr.Reference, 80) + " 的摘录与对应原文不匹配。"
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
