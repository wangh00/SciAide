package agent

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

// Classify a refusal, never try to make the provider bypass it. Text detection
// is deliberately limited to an unstructured first-person opening: source
// quotations, valid JSON and narrative discussions of refusals are not signals.
func stageSubmissionRefusal(turn modelTurn, schema json.RawMessage, previous string) error {
	native := strings.EqualFold(strings.TrimSpace(turn.finishReason), "refusal")
	for _, item := range turn.providerItems {
		if item.Type == "refusal" {
			native = true
		}
		if item.Type != "message" {
			continue
		}
		var message struct {
			Content []struct{ Type string } `json:"content"`
		}
		if json.Unmarshal(item.Payload, &message) == nil {
			for _, block := range message.Content {
				native = native || block.Type == "refusal"
			}
		}
	}
	text := strings.TrimSpace(visibleModelText(turn.text))
	if !native {
		if workflow.HasAIStageOutputCandidate(text, schema) || stageSubmissionEnvelope(text) || json.Valid([]byte(text)) {
			return nil
		}
		if !textStartsWithSubmissionRefusal(text) {
			return nil
		}
	}
	diagnostic := "MODEL_STAGE_REFUSAL\n模型明确拒绝继续；未接受结果，不自动纠正或重复发送拒绝请求。\n" + tool.SafeActivityText(text, 2400)
	return &apperr.Error{
		Code:        "WORKFLOW_AI_MODEL_REFUSED",
		UserMessage: "模型明确拒绝继续本阶段，已停止，不再按格式错误自动纠正。这不是报告科学审查不合格；原始回复和此前未解决的问题保留在诊断中。请先核对模型服务，再重试当前阶段，无需新建任务或重新上传材料。",
		Details:     submissionFailureDetails(diagnostic, previous),
	}
}

func textStartsWithSubmissionRefusal(text string) bool {
	opening := strings.ToLower(strings.TrimLeft(text, "*#_ \t\r\n"))
	if explicitSubmissionRefusalOpening(opening) {
		return true
	}
	// Some models introduce their identity before declining the workflow. Skip
	// only ONE short, standalone identity paragraph, never arbitrary prose,
	// quoted source material or a scan for refusal words later in the response.
	heading, body := submissionOpeningParagraph(text)
	if body == "" || len([]rune(heading)) > 256 || strings.Contains(heading, "\n") || strings.HasPrefix(strings.TrimSpace(heading), ">") {
		return false
	}
	identity := strings.ToLower(strings.Trim(strings.TrimSpace(heading), "*#_ \t"))
	if !submissionModelIdentityHeading(identity) {
		return false
	}
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, ">") {
		return false
	}
	return explicitSubmissionRefusalOpening(strings.ToLower(strings.TrimLeft(body, "*#_ \t")))
}

func explicitSubmissionRefusalOpening(opening string) bool {
	paragraph, _ := submissionOpeningParagraph(opening)
	for _, prefix := range []string{
		"i cannot assist with this request", "i can't assist with this request", "i cannot help with this request",
		"i can't help with this request", "i cannot comply with this request", "i'm unable to assist with this request",
		"i am unable to assist with this request", "sorry, i cannot assist with this request", "sorry, i can't help with this request",
		"我无法协助此请求", "我无法协助这个请求", "我不能协助此请求", "抱歉，我无法协助", "抱歉，我不能协助",
	} {
		if strings.HasPrefix(paragraph, prefix) {
			// Existing Chinese openings are intentionally partial phrases.
			return prefix[0] >= 0x80 || submissionRefusalSentenceBoundary(paragraph[len(prefix):])
		}
	}
	const workflowDecline = "i don't follow or apply sciaide-specific research workflows"
	return strings.HasPrefix(paragraph, workflowDecline) && submissionDeclineStatementBoundary(paragraph[len(workflowDecline):])
}

func submissionRefusalSentenceBoundary(suffix string) bool {
	if submissionDeclineStatementBoundary(suffix) {
		return true
	}
	suffix = strings.TrimLeft(suffix, "*_")
	for _, first := range suffix {
		if !unicode.IsSpace(first) {
			return false // Do not match an attached word such as "requester".
		}
		break
	}
	// Preserve ordinary refusal explanations (as/for/because/...) rather than
	// maintaining a vocabulary whitelist. A metalinguistic predicate after the
	// prefix describes quoted words, not a first-person refusal of this task.
	suffix = strings.TrimSpace(suffix)
	wordEnd := len(suffix)
	for at, char := range suffix {
		if !unicode.IsLetter(char) {
			wordEnd = at
			break
		}
	}
	switch suffix[:wordEnd] {
	case "is", "was", "means", "appears", "sounds":
		return false
	}
	return true
}

func submissionDeclineStatementBoundary(suffix string) bool {
	suffix = strings.TrimLeft(suffix, "*_ \t")
	if suffix == "" {
		return true
	}
	for _, first := range suffix {
		if first == '\n' || (unicode.IsPunct(first) && first != '_') {
			return true
		}
		break
	}
	return false
}

func submissionModelIdentityHeading(heading string) bool {
	var description string
	if strings.HasPrefix(heading, "i am ") {
		description = strings.TrimPrefix(heading, "i am ")
	} else if strings.HasPrefix(heading, "i'm ") {
		description = strings.TrimPrefix(heading, "i'm ")
	} else {
		return false
	}
	if _, after, found := strings.Cut(description, ","); found {
		description = strings.TrimSpace(after)
	}
	description = strings.TrimRight(description, ".! ")
	for _, label := range []string{"an ai", "an ai assistant", "an ai model", "a language model", "a large language model", "an artificial intelligence"} {
		if description == label {
			return true
		}
		for _, relation := range []string{" built by ", " developed by ", " created by ", " trained by "} {
			if strings.HasPrefix(description, label+relation) {
				return true
			}
		}
	}
	return false
}

// A blank line can contain spaces or tabs. Use the same paragraph boundary for
// both the optional identity heading and the refusal opening; later paragraphs
// never supply missing refusal evidence. This projection does not alter audit.
func submissionOpeningParagraph(text string) (string, string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			break
		}
		end += start
		if strings.TrimSpace(text[start:end]) == "" {
			return strings.TrimSpace(text[:start]), strings.TrimSpace(text[end+1:])
		}
		start = end + 1
	}
	return strings.TrimSpace(text), ""
}

func submissionFailureDetails(current, previous string) string {
	if current == "" {
		return previous
	}
	if previous == "" || strings.Contains(current, previous) {
		return current
	}
	return "此前尚未解决的提交问题：\n" + previous + "\n\n本次停止原因：\n" + current
}
