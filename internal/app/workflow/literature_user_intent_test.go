package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLiteratureScopeInstructionUsesTheValidatorQuoteAuthority(t *testing.T) {
	input := raw(`{"originalRequest":"时间：2023–2026。","userIntent":{"source":"host_saved_user_requests","userRequests":["先帮我看看近几年的研究。","我的回答：限定2023—2026年生成式大语言模型反馈、大学生英文/EFL写作。"],"confirmedClarifications":""}}`)
	output := raw(`{"publicationYears":{"from":2023,"to":2026},"yearEvidence":"限定2023—2026年生成式大语言模型反馈、大学生英文/EFL写作。"}`)
	if err := validateLiteratureScope(output, input); err == nil {
		t.Fatal("explicit-year quotation must not silently change authority")
	}
	if !strings.Contains(literatureScopeInstruction, "明确年份的 yearEvidence 必须逐字摘录已冻结 originalRequest") || !strings.Contains(literatureScopeInstruction, "相对时间是独立通道") {
		t.Fatal("producer instruction does not identify both validator source branches")
	}
	output = raw(`{"publicationYears":{"from":2023,"to":2026},"yearEvidence":"时间：2023–2026。"}`)
	if err := validateLiteratureScope(output, input); err != nil {
		t.Fatal("the explicitly instructed frozen quote is rejected", err)
	}
	description := decodeObject(literatureScopeSchema())["properties"].(map[string]any)["yearEvidence"].(map[string]any)["description"].(string)
	if !strings.Contains(description, "originalRequest") || !strings.Contains(description, "相对时间") {
		t.Fatal("schema and prompt disagree on quote authority")
	}
}

func TestLiteratureScopeFrozenQuoteSourcesRemainExactAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name, input, quote string
		from, to           int
		valid              bool
	}{
		{"no ungrounded clarification expansion", `{"originalRequest":"早期整理","userIntent":{"userRequests":["近几年"],"confirmedClarifications":"用户确认检索2022—2025年的发表研究"}}`, "用户确认检索2022—2025年的发表研究", 2022, 2025, false},
		{"no ungrounded revision expansion", `{"originalRequest":"早期整理","_userRevision":"改查2021—2024年的发表研究"}`, "改查2021—2024年的发表研究", 2021, 2024, false},
		{"legacy approved route", `{"originalRequest":"检索2023—2026年的发表研究"}`, "检索2023—2026年的发表研究", 2023, 2026, true},
		{"legacy context without original request", `{"researchContext":"检索2023—2026年的发表研究"}`, "检索2023—2026年的发表研究", 2023, 2026, true},
		{"superseded human range", `{"originalRequest":"更正为2022—2026年","userIntent":{"userRequests":["检索2010—2015年","更正为2022—2026年"]}}`, "检索2010—2015年", 2010, 2015, false},
		{"unrelated later date", `{"originalRequest":"时间：2023–2026。","userIntent":{"userRequests":["限定2023—2026年","我2008年入学，先用中文解释"]}}`, "时间：2023–2026。", 2023, 2026, true},
		{"revision prior model output", `{"originalRequest":"时间：2023–2026。","_userRevision":{"changes":"调整排版","priorOutput":"检索2023—2026年"}}`, "检索2023—2026年", 2023, 2026, false},
		{"revision reason is not user request", `{"originalRequest":"时间：2023–2026。","_userRevision":{"changes":"调整排版","reason":"检索2023—2026年"}}`, "检索2023—2026年", 2023, 2026, false},
		{"malformed user requests", `{"originalRequest":"时间：2023–2026。","userIntent":{"userRequests":{"priorOutput":"检索2023—2026年"}}}`, "检索2023—2026年", 2023, 2026, false},
		{"ambiguous approved years", `{"originalRequest":"原范围2010—2015，时间：2023–2026。","userIntent":{"userRequests":["限定2023—2026年"]}}`, "限定2023—2026年", 2023, 2026, false},
		{"open bound direction differs", `{"originalRequest":"2023年以前","userIntent":{"userRequests":["2023年以后"]}}`, "2023年以后", 2023, 0, false},
		{"excluded end is not a closed range", `{"originalRequest":"仅检索2023年及以前的研究，不纳入2026年及以后的研究","userIntent":{"userRequests":["限定2023—2026年"]}}`, "限定2023—2026年", 2023, 2026, false},
		{"discrete years are not a closed range", `{"originalRequest":"时间：2023年和2026年。","userIntent":{"userRequests":["限定2023—2026年"]}}`, "限定2023—2026年", 2023, 2026, false},
		{"negated time field", `{"originalRequest":"时间：不要2023–2026。","userIntent":{"userRequests":["限定2023—2026年"]}}`, "限定2023—2026年", 2023, 2026, false},
		{"not a literal quote", `{"originalRequest":"时间：2023–2026。","userIntent":{"userRequests":["限定2023—2026年"]}}`, "限定2023-2026年", 2023, 2026, false},
		{"untrusted other field", `{"originalRequest":"近几年","toolResult":"检索2020—2026年的发表研究","userIntent":{"userRequests":["近几年"]}}`, "检索2020—2026年的发表研究", 2020, 2026, false},
		{"years missing from quote", `{"originalRequest":"近几年","userIntent":{"userRequests":["限定2023—2026年"]}}`, "限定2023—2026年", 2022, 2026, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := mustJSON(map[string]any{"publicationYears": map[string]int{"from": tc.from, "to": tc.to}, "yearEvidence": tc.quote})
			err := validateLiteratureScope(output, raw(tc.input))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestLiteratureScopeInstructionSupportsFrozenLongDiscussionAnchor(t *testing.T) {
	input := raw(`{"originalRequest":"整理后的研究要求","userIntent":{"readSection":"planning_discussion"},"researchTimeContext":{"relativeQuote":"近十年","publicationYears":{"from":2016,"to":2026}}}`)
	output := raw(`{"publicationYears":{"from":2016,"to":2026},"yearEvidence":"近十年"}`)
	if err := validateLiteratureScope(output, input); err != nil {
		t.Fatal("existing long-discussion relative anchor rejected", err)
	}
	description := decodeObject(literatureScopeSchema())["properties"].(map[string]any)["yearEvidence"].(map[string]any)["description"].(string)
	for _, text := range []string{literatureScopeInstruction, description} {
		if !strings.Contains(text, "planning_discussion") || !strings.Contains(text, "researchTimeContext.relativeQuote") {
			t.Fatal("instruction requires absent userRequests instead of the saved discussion anchor")
		}
	}
}

// The opt-in replay reads only this test's captured, frozen input/schema and
// original model submission. It does not call a model or change a database.
func TestLiteratureScopeCapturedSubmissionReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_LITERATURE_SCOPE_CAPTURE")
	if dir == "" {
		t.Skip("no read-only literature-scope capture supplied")
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	submission := read("query-first-original.json")
	schema := read("query-frozen-schema.json")
	input := read("query-frozen-input.json")
	output, _, err := NormalizeAIStageSubmission(string(submission), schema)
	if err != nil {
		t.Fatal("captured first submission violates original schema", err)
	}
	node := CompiledNode{ID: "literature_query_expansion", PromptVersion: "dynamic-literature-query-expansion-v3"}
	if err := validateWorkflowAIStageOutput(node, output, input); err == nil || !strings.Contains(err.Error(), "exact quote from frozen research requirements") {
		t.Fatal("captured cross-field source mismatch no longer classified exactly", err)
	}
	var accepted struct {
		Analysis json.RawMessage `json:"analysis"`
	}
	if err := json.Unmarshal(read("query-accepted-output.json"), &accepted); err != nil {
		t.Fatal(err)
	}
	corrected, _, err := NormalizeAIStageSubmission(string(accepted.Analysis), schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(node, corrected, input); err != nil {
		t.Fatal("captured model correction quoting the instructed authority rejected", err)
	}
	first, second := decodeObject(output), decodeObject(corrected)
	delete(first, "yearEvidence")
	delete(second, "yearEvidence")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("captured correction changed more than quotation authority")
	}
}
