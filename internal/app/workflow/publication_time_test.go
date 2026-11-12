package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

func TestPublicationTimeContextUsesOneResolver(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	for _, tc := range []struct {
		text, quote string
		from, to    int
	}{
		{"检索近十年的文献", "近十年", 2016, 2026},
		{"检索2015年至今的英文文献", "2015年至今", 2015, 2026},
		{"检索自2015年以来发表的论文", "自2015年以来", 2015, 2026},
		{"查从2015年到现在的文献", "从2015年到现在", 2015, 2026},
		{"本轮只选4–6项2015 年至今的英文代表性研究", "2015 年至今", 2015, 2026},
		{"检索2015—2025年文献", "", 2015, 2025},
		{"Find papers published since 2015.", "since 2015", 2015, 2026},
		{"Find studies from the past 10 years.", "past 10 years", 2016, 2026},
		{"Find studies published 2020-2025.", "", 2020, 2025},
		{"Exclude papers published since 2015.", "", 0, 0},
		{"Studies with follow-up since 2015.", "", 0, 0},
		{"查2015—2025年文献，近十年这个词仅作背景", "", 2015, 2025},
		{"我2008年入学，检索近十年文献", "近十年", 2016, 2026},
		{"2015年至今的随访研究", "", 0, 0},
		{"实验日期限定2015年至今", "", 0, 0},
		{"年龄从2015年到现在的变化研究", "", 0, 0},
		{"不检索2015年至今的文献", "", 0, 0},
		{"检索2030年至今文献", "", 0, 0},
		{"检索2015至今与2020至今的文献", "", 0, 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			v := newResearchTimeContext(tc.text, now)
			if v.PublicationYears.From != tc.from || v.PublicationYears.To != tc.to || v.RelativeQuote != tc.quote {
				t.Fatalf("resolved %+v; want %d/%d quote=%q", v, tc.from, tc.to, tc.quote)
			}
			if v.Date != "2026-11-12" || v.Zone != "CST" {
				t.Fatal("host date or zone changed", v)
			}
		})
	}
}

func TestPublicationTimeHistoryPreservesScopeAcrossUnrelatedDates(t *testing.T) {
	message := func(role conversation.Role, text string) conversation.Message {
		return conversation.Message{Role: role, Parts: []conversation.MessagePart{{Type: "text", Text: text}}}
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	history := []conversation.Message{
		message(conversation.RoleUser, "检索2015年至今的文献"),
		message(conversation.RoleAssistant, "改成2010—2020年的文献"),
		message(conversation.RoleUser, "我2008年入学，先用中文解释"),
	}
	v := researchHumanTimeContext(history, now)
	if v.RelativeQuote != "2015年至今" || v.PublicationYears.From != 2015 || v.PublicationYears.To != 2026 {
		t.Fatal("unrelated date or assistant changed publication scope", v)
	}
	history = append(history, message(conversation.RoleUser, "更正为2020—2024年的文献"))
	v = researchHumanTimeContext(history, now)
	if v.RelativeQuote != "" || v.PublicationYears.From != 2020 || v.PublicationYears.To != 2024 {
		t.Fatal("later explicit publication range did not replace the old anchor", v)
	}
	history = append(history, message(conversation.RoleUser, "不检索近十年文献，范围要重新讨论"))
	v = researchHumanTimeContext(history, now)
	if v.PublicationYears.Active() || v.RelativeQuote != "" {
		t.Fatal("withdrawn publication range resurrected the old anchor", v)
	}
	for _, text := range []string{"改查2020年的文献", "近几年文献", "不限制发表年份"} {
		changed := append(history[:3:3], message(conversation.RoleUser, text))
		v := researchHumanTimeContext(changed, now)
		if v.RelativeQuote != "" || v.PublicationYears.Active() {
			t.Fatal("unsupported or withdrawn scope retained the old anchor", text, v)
		}
	}
}

func TestPublicationTimeFrozenMetadataCannotBypassItsDate(t *testing.T) {
	for _, tc := range []struct {
		date  string
		valid bool
	}{{"2026-11-11", true}, {"2025-11-11", false}, {"2026-02-30", false}, {"", true}} {
		input := mustJSON(map[string]any{
			"originalRequest":     "AI organized request",
			"researchTimeContext": map[string]any{"date": tc.date, "relativeQuote": "近十年", "publicationYears": map[string]int{"from": 2016, "to": 2026}},
			"userIntent":          map[string]any{"userRequests": []string{"检索近十年文献"}},
		})
		output := raw(`{"publicationYears":{"from":2016,"to":2026},"yearEvidence":"近十年"}`)
		if err := validateLiteratureScope(output, input); (err == nil) != tc.valid {
			t.Fatalf("date=%q valid=%v: %v", tc.date, tc.valid, err)
		}
	}
}

func TestPublicationTimeValidationBindsQuoteRangeAndFrozenDate(t *testing.T) {
	for _, tc := range []struct {
		name, original, quote, date string
		from, to                    int
		valid                       bool
	}{
		{"since to present", "检索2015年至今的文献", "2015年至今", "2026-11-11", 2015, 2026, true},
		{"whole literal quote", "检索2015年至今的文献", "检索2015年至今的文献", "2026-11-11", 2015, 2026, true},
		{"frozen not current year", "检索2015年以来的论文", "2015年以来", "2025-12-31", 2015, 2025, true},
		{"explicit range", "检索2020—2024年文献", "2020—2024年", "2026-11-11", 2020, 2024, true},
		{"English since", "Find papers published since 2015.", "since 2015", "2026-11-11", 2015, 2026, true},
		{"wrong upper", "检索2015年至今的文献", "2015年至今", "2026-11-11", 2015, 2025, false},
		{"wrong lower", "检索2015年至今的文献", "2015年至今", "2026-11-11", 2014, 2026, false},
		{"drop upper", "检索2015年至今的文献", "2015年至今", "2026-11-11", 2015, 0, false},
		{"drop range", "检索2015年至今的文献", "", "2026-11-11", 0, 0, false},
		{"invent numeric quote", "检索2015年至今的文献", "2015年至2026年的文献", "2026-11-11", 2015, 2026, false},
		{"missing date", "检索2015年至今的文献", "2015年至今", "", 2015, 2026, false},
		{"malformed date", "检索2015年至今的文献", "2015年至今", "2026-02-30", 2015, 2026, false},
		{"future start", "检索2030年至今的文献", "2030年至今", "2026-11-11", 2030, 2031, false},
		{"negated", "不检索2015年至今的文献", "2015年至今", "2026-11-11", 2015, 2026, false},
		{"unrelated experimental time", "研究的实验日期为2015—2026年", "2015—2026年", "2026-11-11", 2015, 2026, false},
		{"conflicting requests", "检索2015至今与2020至今的文献", "2015至今", "2026-11-11", 2015, 2026, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := mustJSON(map[string]any{"originalRequest": tc.original, "researchTimeContext": map[string]any{"date": tc.date, "publicationYears": map[string]int{"from": 0, "to": 0}}})
			output := mustJSON(map[string]any{"publicationYears": map[string]int{"from": tc.from, "to": tc.to}, "yearEvidence": tc.quote})
			err := validateLiteratureScope(output, input)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

// Opt-in replay reads the unchanged original schema, input and three real
// candidates. No model, database write, rewritten quote or new date is used.
func TestPublicationTimeCapturedSinceYearSubmissions(t *testing.T) {
	dir := os.Getenv("SCIAIDE_PUBLICATION_TIME_CAPTURE")
	if dir == "" {
		t.Skip("no isolated publication-time capture supplied")
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var node CompiledNode
	if err := json.Unmarshal(read("failed-node.json"), &node); err != nil {
		t.Fatal(err)
	}
	input := read("failed-stage-input.json")
	before := hashJSON(input)
	for turn := 1; turn <= 3; turn++ {
		candidate := string(read("query-candidate-" + strconv.Itoa(turn) + ".json"))
		output, _, err := NormalizeAIStageSubmission(candidate, node.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateWorkflowAIStageOutput(node, output, input); err != nil {
			t.Fatalf("real submission %d rejected: %v", turn, err)
		}
		t.Logf("original submission %d passes unchanged frozen schema and host date grounding", turn)
	}
	if hashJSON(read("failed-stage-input.json")) != before {
		t.Fatal("replay changed captured input")
	}
	if !strings.Contains(string(input), "2015") {
		t.Fatal("wrong capture supplied")
	}
}
