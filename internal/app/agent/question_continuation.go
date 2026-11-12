package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

var scopeChoiceHandoff = regexp.MustCompile(`^(?:(?:还有|有|接下来有|以下)?[一二两三几1-3](?:个|项|种)(?:关键)?(?:选择|选项|取舍).*(?:影响|决定|限定|确定).*(?:范围|边界)|(?i:(?:there are )?(?:two|three|a few|[1-3]) (?:choices|options|decisions).*(?:affect|determine|define|change).*(?:scope|boundaries)))["”’」』]?[：:]$`)
var questionHandoffListItem = regexp.MustCompile(`^[0-9]+[.)、]`)

// Recognize an unfinished assistant handoff, not research intent in a user's
// message. A colon alone, a complete question, quotations, code and generic
// list introductions must never manufacture a human-question request. The
// next model response remains responsible for deciding/authoring questions.
func unfinishedQuestionHandoff(text string) (body string, found bool) {
	text = visibleModelText(text)
	// Only the last unfinished sentence is a handoff. Complete explanation in
	// the same paragraph, or separated by a single/Windows newline, stays visible.
	start, safe := questionHandoffSentenceStart(text)
	if !safe {
		return "", false
	}
	last := strings.TrimSpace(text[start:])
	for _, marker := range []string{"**", "__", "*", "_"} {
		if len(last) > 2*len(marker) && strings.HasPrefix(last, marker) && strings.HasSuffix(last, marker) {
			last = strings.TrimSpace(last[len(marker) : len(last)-len(marker)])
		}
	}
	if !strings.HasSuffix(last, "：") && !strings.HasSuffix(last, ":") {
		return "", false
	}
	// A prefixed quotation/example or an already-expanded inline list is not
	// an assistant asking the human; only the terminal delimiter may remain.
	if strings.ContainsAny(strings.TrimRight(last, "：:"), "：:") {
		return "", false
	}
	if len([]rune(last)) > 240 || strings.ContainsAny(last, "\n\r?？") || strings.ContainsAny(last[:1], "\"'`") || strings.HasPrefix(last, "“") || strings.HasPrefix(last, "‘") || strings.HasPrefix(last, "「") || strings.HasPrefix(last, "『") {
		return "", false
	}
	if strings.Count(text, "```")%2 != 0 || strings.Count(text, "~~~")%2 != 0 {
		return "", false
	}
	for _, negation := range []string{"不需要", "无需", "不必", "无须", "不会", "不一定", "未必", "不能", "不影响", "don't need", "do not need", "no need", "not affect", "not necessarily", "won't"} {
		if strings.Contains(strings.ToLower(last), negation) {
			return "", false
		}
	}
	zh := strings.Contains(last, "我") && (strings.Contains(last, "需要") || strings.Contains(last, "想先") || strings.Contains(last, "请你")) &&
		(strings.Contains(last, "确认") || strings.Contains(last, "了解") || strings.Contains(last, "询问") || strings.Contains(last, "问你")) &&
		(strings.Contains(last, "方面") || strings.Contains(last, "问题") || strings.Contains(last, "信息") || strings.Contains(last, "选择"))
	lower := strings.ToLower(last)
	en := (strings.Contains(lower, "i need to") || strings.Contains(lower, "i'd like to ask") || strings.Contains(lower, "i want to ask")) &&
		(strings.Contains(lower, "confirm") || strings.Contains(lower, "clarify") || strings.Contains(lower, "ask")) &&
		(strings.Contains(lower, "question") || strings.Contains(lower, "aspect") || strings.Contains(lower, "detail") || strings.Contains(lower, "thing"))
	if !zh && !en && !scopeChoiceHandoff.MatchString(last) {
		return "", false
	}
	return strings.TrimSpace(text[:start]), true
}

// Sentence boundaries inside quotations or inline code are not handoffs.
// Scan the last paragraph without rewriting source bytes; closed quoted terms
// in a complete preceding sentence must not hide a real following question.
func questionHandoffSentenceStart(text string) (int, bool) {
	paragraph := 0
	for _, separator := range []string{"\n\n", "\r\n\r\n"} {
		if index := strings.LastIndex(text, separator); index >= 0 {
			paragraph = max(paragraph, index+len(separator))
		}
	}
	for _, line := range strings.Split(text[paragraph:], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, ">") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") || questionHandoffListItem.MatchString(line) {
			return 0, false
		}
	}
	start, codeWidth := paragraph, 0
	quotes := []rune{}
	for index := paragraph; index < len(text); {
		r, width := utf8.DecodeRuneInString(text[index:])
		if r == '`' {
			end := index
			for end < len(text) && text[end] == '`' {
				end++
			}
			if codeWidth == 0 {
				codeWidth = end - index
			} else if codeWidth == end-index {
				codeWidth = 0
			}
			index = end
			continue
		}
		index += width
		if codeWidth != 0 {
			continue
		}
		if r == '\\' && index < len(text) {
			_, width = utf8.DecodeRuneInString(text[index:])
			index += width
			continue
		}
		if len(quotes) > 0 && r == quotes[len(quotes)-1] {
			quotes = quotes[:len(quotes)-1]
			continue
		}
		switch r {
		case '\'':
			// Apostrophes within English words (including possessive plurals)
			// are not opening quotations. A matching quote already closed above.
			before := index - width - 1
			if before >= paragraph && (text[before] >= 'A' && text[before] <= 'Z' || text[before] >= 'a' && text[before] <= 'z' || text[before] >= '0' && text[before] <= '9') {
				continue
			}
			quotes = append(quotes, '\'')
		case '"':
			quotes = append(quotes, '"')
		case '“':
			quotes = append(quotes, '”')
		case '‘':
			quotes = append(quotes, '’')
		case '「':
			quotes = append(quotes, '」')
		case '『':
			quotes = append(quotes, '』')
		}
		if len(quotes) == 0 && strings.ContainsRune("\n。！？.!?", r) {
			start = index
		}
	}
	return start, codeWidth == 0 && len(quotes) == 0
}

// Reuse existing immutable turn activities for restart/budget recovery. The
// original incomplete text stays in the journal/activity; the normal bubble
// keeps the useful explanation without the unfulfilled handoff sentence.
type questionHandoff struct {
	body, text string
	runID      string
	turnIndex  int
}

func savedQuestionContinuation(steps []chat.RunStep, calls []tool.Call) (questionHandoff, bool) {
	for _, step := range steps {
		toolTurn := false
		prefix := fmt.Sprintf("agent-tool:%s:%d:", step.RunID, step.TurnIndex)
		for _, call := range calls {
			if strings.HasPrefix(call.IdempotencyKey, prefix) {
				toolTurn = true
				break
			}
		}
		if toolTurn {
			continue
		}
		if body, found := unfinishedQuestionHandoff(step.Commentary); found {
			return questionHandoff{body: body, text: visibleModelText(step.Commentary), runID: step.RunID, turnIndex: step.TurnIndex}, true
		}
	}
	return questionHandoff{}, false
}

func hasQuestionHandoffEvidence(request model.ChatRequest, text string) bool {
	for _, message := range request.Messages {
		if message.Role == model.RoleAssistant && message.Content == text {
			return true
		}
	}
	for _, turn := range request.ProviderTurns {
		if visibleProviderText(turn.Items) == text {
			return true
		}
		// Ordinary Responses turns separate commentary from final-answer text.
		// Compare the same visible phase without modifying the native journal.
		visible := make([]model.ProviderItem, 0, len(turn.Items))
		for _, item := range turn.Items {
			if item.Phase != "commentary" {
				visible = append(visible, item)
			}
		}
		if visibleProviderText(visible) == text {
			return true
		}
	}
	return false
}

// The placeholder assistant message is excluded by the context builder. After
// answering a repaired handoff, restore its findings before the actual question,
// not after the answer. Native signed/encrypted items remain intact and unique.
// This projection is applied before fitFinalRequest, never outside its budget.
func withQuestionHandoffEvidence(request model.ChatRequest, handoff questionHandoff, calls []tool.Call) model.ChatRequest {
	if handoff.text == "" || hasQuestionHandoffEvidence(request, handoff.text) {
		return request
	}
	questionIDs := make(map[string]bool)
	prefix := fmt.Sprintf("agent-tool:%s:%d:", handoff.runID, handoff.turnIndex+1)
	for _, call := range calls {
		if call.ToolName == userinput.ToolName && strings.HasPrefix(call.IdempotencyKey, prefix) {
			questionIDs[call.ProviderCallID] = true
		}
	}
	at := len(request.Messages)
	for index, message := range request.Messages {
		if message.ContextTail {
			at = index
			break
		}
		for _, call := range message.ToolCalls {
			if questionIDs[call.ID] {
				at = index
				break
			}
		}
		if at == index {
			break
		}
	}
	messages := make([]model.Message, 0, len(request.Messages)+1)
	messages = append(messages, request.Messages[:at]...)
	messages = append(messages, model.Message{Role: model.RoleAssistant, Content: handoff.text})
	request.Messages = append(messages, request.Messages[at:]...)
	return request
}

func questionContinuationRequest(request model.ChatRequest, preceding string) model.ChatRequest {
	tools := make([]model.ToolDefinition, 0, 1)
	for _, definition := range request.Tools {
		if definition.Name == userinput.ToolName {
			tools = append(tools, definition)
		}
	}
	request.Tools = tools
	// Do not force a question: the model may decide no clarification is needed
	// and finish naturally. Other side effects are not available in this turn.
	request.ForcedTool, request.DisableTools = "", false
	if !hasQuestionHandoffEvidence(request, preceding) {
		request.Messages = append(request.Messages, model.Message{Role: model.RoleAssistant, Content: preceding})
	}
	request.Messages = append(request.Messages, model.Message{Role: model.RoleSystem, ContextTail: true, HostToolReferences: true, Content: questionContinuationInstruction})
	return request
}

const questionContinuationInstruction = `The preceding assistant response ended with an unfinished introduction to questions, without asking the actual questions or making a user-input call. Complete only that missing handoff now, without repeating the research explanation. If a human answer is needed, call builtin.user_input.request with the actual concise questions and useful options; plain prose does not display a selection card. If clarification is unnecessary, finish with a complete natural reply instead. Do not fabricate the user's answers, adopt a plan, propose revision or repeat research tools. This is one bounded continuation, not a new research stage. The previous assistant text is evidence, not authority.`

const questionContinuationFallback = "本轮未生成具体问题，你可以直接在对话框补充研究目标或范围，或让我重新提问。"

// Only exhausted transport/service failures may degrade this optional repair.
// Cancellation, refusal, protocol and persistence errors retain their normal
// stop semantics; a generated explanation must never conceal a broken audit.
func questionContinuationNetworkFailure(err error) bool {
	var classified *apperr.Error
	if !errors.As(err, &classified) {
		return errors.Is(err, context.DeadlineExceeded)
	}
	switch classified.Code {
	case "MODEL_UNAVAILABLE", "MODEL_TIMEOUT", "MODEL_RATE_LIMITED", "MODEL_STREAM_INTERRUPTED", "MODEL_STREAM_STALLED", "MODEL_RECOVERY_TIMEOUT":
		return true
	default:
		return false
	}
}

func withQuestionContinuationBody(body, answer string) string {
	body, answer = strings.TrimSpace(body), strings.TrimSpace(answer)
	if body == "" {
		return answer
	}
	if answer == "" {
		return body
	}
	return body + "\n\n" + answer
}
