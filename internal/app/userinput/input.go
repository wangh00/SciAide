// Package userinput is the shared human-question protocol, independent of
// research planning, permissions and execution stages.
package userinput

import (
	"encoding/json"
	"fmt"
	"strings"
)

const ToolName = "builtin.user_input.request"

type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type Question struct {
	ID            string   `json:"id"`
	Header        string   `json:"header,omitempty"`
	Question      string   `json:"question"`
	SelectionMode string   `json:"selectionMode,omitempty"`
	Required      bool     `json:"required"`
	Options       []Option `json:"options,omitempty"`
}
type Questions struct {
	Questions []Question `json:"questions"`
}
type Answer struct {
	OptionIDs []string `json:"optionIds,omitempty"`
	Text      string   `json:"text,omitempty"`
}
type Answers map[string]Answer
type Request struct {
	RunID     string     `json:"runId"`
	CallID    string     `json:"callId"`
	Questions []Question `json:"questions"`
}
type Response struct {
	ConversationID string  `json:"conversationId"`
	RunID          string  `json:"runId"`
	CallID         string  `json:"callId"`
	Answers        Answers `json:"answers"`
	Dismiss        bool    `json:"dismiss,omitempty"`
}

// Bounds are transport limits, not scientific completeness gates.
func Decode(raw json.RawMessage) (Questions, error) {
	var value Questions
	if len(raw) > 64*1024 || json.Unmarshal(raw, &value) != nil || len(value.Questions) == 0 || len(value.Questions) > 12 {
		return value, fmt.Errorf("请提供 1–12 个简短问题；通常只问 1–3 个必要问题")
	}
	seen := map[string]bool{}
	for i := range value.Questions {
		q := &value.Questions[i]
		q.ID = strings.TrimSpace(q.ID)
		q.Question = strings.TrimSpace(q.Question)
		if q.ID == "" || len(q.ID) > 128 || len([]rune(q.Header)) > 80 || seen[q.ID] || q.Question == "" || len([]rune(q.Question)) > 4000 || len(q.Options) > 12 {
			return value, fmt.Errorf("问题标识须唯一，问题及选项须完整")
		}
		seen[q.ID] = true
		if q.SelectionMode == "" {
			q.SelectionMode = "single"
		}
		if q.SelectionMode != "single" && q.SelectionMode != "multiple" {
			return value, fmt.Errorf("selectionMode 须为 single 或 multiple")
		}
		ids := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.ID) == "" || len(o.ID) > 128 || ids[o.ID] || strings.TrimSpace(o.Label) == "" || len([]rune(o.Label)) > 2000 || len([]rune(o.Description)) > 2000 {
				return value, fmt.Errorf("选项标识须唯一且包含标签")
			}
			ids[o.ID] = true
		}
	}
	return value, nil
}

func NormalizeAnswers(questions []Question, answers Answers) (Answers, error) {
	known := map[string]bool{}
	result := Answers{}
	for _, q := range questions {
		known[q.ID] = true
		a := answers[q.ID]
		a.Text = strings.TrimSpace(a.Text)
		if len([]rune(a.Text)) > 8000 {
			return nil, fmt.Errorf("回答过长，请缩短后提交")
		}
		if q.SelectionMode != "multiple" && len(a.OptionIDs) > 1 {
			return nil, fmt.Errorf("该问题只能选择一项")
		}
		seen := map[string]bool{}
		allowed := map[string]bool{}
		for _, o := range q.Options {
			allowed[o.ID] = true
		}
		for _, id := range a.OptionIDs {
			if !allowed[id] || seen[id] {
				return nil, fmt.Errorf("选择不属于当前问题的选项")
			}
			seen[id] = true
		}
		a.OptionIDs = nil
		for _, o := range q.Options {
			if seen[o.ID] {
				a.OptionIDs = append(a.OptionIDs, o.ID)
			}
		}
		if q.Required && len(a.OptionIDs) == 0 && a.Text == "" {
			return nil, fmt.Errorf("请回答“%s”，也可以自由填写", q.Question)
		}
		result[q.ID] = a
	}
	for key := range answers {
		if !known[key] {
			return nil, fmt.Errorf("回答包含已变化的问题")
		}
	}
	return result, nil
}

func AnswerText(questions []Question, answers Answers) string {
	var b strings.Builder
	b.WriteString("我的回答：\n")
	for _, q := range questions {
		a := answers[q.ID]
		labels := []string{}
		for _, o := range q.Options {
			for _, id := range a.OptionIDs {
				if id == o.ID {
					labels = append(labels, o.Label)
				}
			}
		}
		if a.Text != "" {
			labels = append(labels, a.Text)
		}
		if len(labels) > 0 {
			fmt.Fprintf(&b, "- %s：%s\n", q.Question, strings.Join(labels, "；"))
		}
	}
	return strings.TrimSpace(b.String())
}

const Rules = `When a user decision or missing information materially changes the answer or next work, use builtin.user_input.request to ask concise questions with useful options and tradeoffs. This is available throughout ordinary conversation, including after a research deliverable. Usually ask one question, at most three at once; do not ask what the user already answered, turn every reply into a questionnaire, or ask the user to repair program internals. Free-form answers are always supported by the client: do not force the user into the suggested choices. Explicit submission is required; a suggestion is not a user decision. Receiving an answer does not authorize tool side effects, adopt a research route or start revision. A clear research goal with unresolved method choices may proceed to the existing planner; do not require all method decisions before preparing routes. A question card exists only after an actual builtin.user_input.request call; plain text cannot create one. If you announce that you need to ask or confirm something, make the actual question tool call in the same response; never end with an introduction such as "I need to confirm two aspects:" and omit the questions. Each question needs an ID, actual question text and required; optional choices need IDs and labels. Do not say "choose below" or claim a pending card without calling the tool. No Skill load is required to ask a question or continue ordinary conversation; recover a Skill failure without asking the user to choose internal Skill names.`
