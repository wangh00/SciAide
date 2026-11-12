package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

func TestResearchTimeContextUsesHostDateWithoutGuessingUnrelatedYears(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	for _, tc := range []struct {
		text     string
		from, to int
	}{
		{"找近十年的文献，摘要证据即可", 2016, 2026},
		{"最近五年发表的论文", 2021, 2026},
		{"检索过去3年的研究", 2023, 2026},
		{"查2015—2025年文献，近十年这个词仅作背景", 2015, 2025},
		{"文献中的近十年随访", 0, 0},
		{"近十年病史的研究", 0, 0},
		{"我近十年都在这里生活", 0, 0},
		{"近十年文艺活动", 0, 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			v := newResearchTimeContext(tc.text, now)
			if v.Date != "2026-10-19" || v.Zone != "CST" || v.PublicationYears.From != tc.from || v.PublicationYears.To != tc.to {
				t.Fatalf("wrong anchored range: %+v", v)
			}
			if tc.from > 0 && !strings.Contains(v.Interpretation, "宽边界") {
				t.Fatal("year filter misrepresented as exact day range")
			}
		})
	}
}

func TestResearchGroundingPreservesOriginalAndConfirmedRequestsSeparately(t *testing.T) {
	history := []conversation.Message{}
	_ = json.Unmarshal(raw(`[{"role":"user","parts":[{"type":"text","text":"近十年文献，摘要即可"}]},{"role":"assistant","parts":[{"type":"text","text":"必须穷尽并阅读全文"}]},{"role":"user","parts":[{"type":"text","text":"先给通俗说明"}]}]`), &history)
	history = append(history, conversation.Message{Role: conversation.RoleUser, Internal: true, Parts: []conversation.MessagePart{{Type: "text", Text: "Internal workflow stage prompt, not a human request"}}})
	requests := researchHumanRequests(history, "AI expanded request")
	if len(requests) != 2 || strings.Contains(strings.Join(requests, ""), "必须穷尽") {
		t.Fatal("assistant text became user demand")
	}
	clock := newResearchTimeContext(requests[0], time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	d := RunDetail{Run: Run{Inputs: mustJSON(map[string]any{"route_context": map[string]any{"userRequests": requests, "researchTimeContext": clock, "confirmedClarifications": "用户确认只查2020—2025年，摘要即可"}})}}
	args := map[string]any{"originalRequest": "AI organized request", "_userRevision": "User later changes deliverable"}
	addResearchGrounding(d, args)
	if args["originalRequest"] != "AI organized request" || args["_userRevision"] != "User later changes deliverable" {
		t.Fatal("existing scientific context overwritten")
	}
	intent := decodeObject(mustJSON(args["userIntent"]))
	if !strings.Contains(string(mustJSON(intent["confirmedClarifications"])), "2020") {
		t.Fatal("confirmed clarification lost")
	}
	before := string(mustJSON(args))
	addResearchGrounding(RunDetail{}, args)
	if before != string(mustJSON(args)) {
		t.Fatal("legacy frozen task was silently re-grounded")
	}
	output := raw(`{"publicationYears":{"from":2016,"to":2026},"yearEvidence":"近十年文献，摘要即可"}`)
	if err := validateLiteratureScope(output, mustJSON(args)); err != nil {
		t.Fatal(err)
	}
	if err := validateLiteratureScope(raw(`{"publicationYears":{"from":2015,"to":2025},"yearEvidence":"近十年文献，摘要即可"}`), mustJSON(args)); err == nil {
		t.Fatal("unanchored relative years accepted")
	}
	args["originalRequest"] = "用户确认只查2020—2025年，摘要即可"
	if err := validateLiteratureScope(raw(`{"publicationYears":{"from":2020,"to":2025},"yearEvidence":"用户确认只查2020—2025年，摘要即可"}`), mustJSON(args)); err != nil {
		t.Fatal("later explicit range cannot override initial relative range", err)
	}
}

func TestResearchGroundingLongDiscussionUsesFrozenRecordWithoutCopyingOrTruncating(t *testing.T) {
	history := []conversation.Message{{Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "找近十年文献，摘要即可"}}}}
	for i := 0; i < 25; i++ {
		history = append(history, conversation.Message{Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("用户已保存的讨论材料。", 1000)}}})
	}
	original := hashJSON(mustJSON(history))
	requests := researchHumanRequests(history, "AI's summary is not a verbatim replacement")
	if len(requests) != 0 {
		t.Fatal("large originals copied into every stage")
	}
	clock := researchHumanTimeContext(history, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	if clock.RelativeQuote != "近十年" || clock.PublicationYears.To != 2026 {
		t.Fatal("long discussion lost real date provenance")
	}
	starter := ResearchStarterContext{ResearchIdea: "协商后的研究说明", StageCatalog: dynamicResearchStageCatalog(), UserRequests: requests, TimeContext: clock, DiscussionRecord: &ResearchPlanningDiscussion{SourcePlannerRunID: "planner", MessageCount: len(history)}, ClarificationAnswers: map[string][]string{"scope": {"abstract"}}}
	_, inputs, _, err := dynamicResearchRouteTemplate(starterDesignRoute(), starter, true)
	if err != nil {
		t.Fatal(err)
	}
	d := RunDetail{Run: Run{Inputs: inputs}}
	args := map[string]any{}
	addResearchGrounding(d, args)
	intent := decodeObject(mustJSON(args["userIntent"]))
	if intent["readSection"] != "planning_discussion" || intent["confirmedClarifications"] != starter.ResearchIdea {
		t.Fatal("adoption lost original-record/confirmed-answer provenance")
	}
	if len(inputs) > 32*1024 || len(mustJSON(args)) > 16*1024 {
		t.Fatal("metadata overflowed stage snapshot")
	}
	if hashJSON(mustJSON(history)) != original {
		t.Fatal("saved original discussion was changed")
	}
	if err := validateLiteratureScope(raw(`{"publicationYears":{"from":2016,"to":2026},"yearEvidence":"近十年"}`), mustJSON(args)); err != nil {
		t.Fatal(err)
	}
	if err := validateLiteratureScope(raw(`{"publicationYears":{"from":2016,"to":2026},"yearEvidence":"AI invented relative quote"}`), mustJSON(args)); err == nil {
		t.Fatal("deferred reading allowed invented quote")
	}
}
