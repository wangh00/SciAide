package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
)

func TestReportSourceProjectionAuditIsFrozenAndDisclosed(t *testing.T) {
	input := raw(`{"researchContract":{"query":"original logical query"},"researchSourceContext":{"retrieval":{"queries":["original logical query"],"sources":[{"sourceId":"crossref","status":"ok","count":1,"logicalQuery":"original logical query","inputQuery":"writing NOT animal-based","effectiveQuery":"writing","providerQuery":"writing","queryMode":"ranked_bibliographic","queryOrigin":"planned_provider_query","queryProjectionVersion":"source-query-v1","queryLimitations":["negation_not_enforced"]}]}}}`)
	appendix, err := reportRetrievalAppendix(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"original logical query", "对应逻辑检索式", "writing NOT animal-based", "planned_provider_query", "ranked_bibliographic", "source-query-v1", "negation_not_enforced", "不保证扩大召回", "未保留的查询约束", "不证明规划式与逻辑原式语义等价", "编译后的来源查询表达式", "不证明本次已发送 HTTP"} {
		if !strings.Contains(appendix, want) {
			t.Fatal("report omitted projection evidence", want, appendix)
		}
	}
	if strings.Contains(appendix, `"实际查询"`) || strings.Contains(appendix, `"已应用年份限制"`) {
		t.Fatal("prepared fields claimed actual request execution", appendix)
	}
	again, err := reportRetrievalAppendix(input)
	if err != nil || again != appendix {
		t.Fatal("projection appendix is not deterministic", err)
	}
	old, err := reportRetrievalAppendix(raw(`{"researchSourceContext":{"retrieval":{"sources":[{"sourceId":"crossref","status":"ok","count":1,"effectiveQuery":"old query","queryMode":"planned_provider_query"}]}}}`))
	if err != nil || strings.Contains(old, "查询投影版本") || strings.Contains(old, "投影说明") || strings.Contains(old, "来源输入式") {
		t.Fatal("old frozen source log was reinterpreted", err, old)
	}
}

func TestSourceAttemptKeysDoNotConfuseEqualRankedProjections(t *testing.T) {
	base := research.SourceSearch{SourceID: "crossref", EffectiveQuery: "writing", InputQuery: "writing", LogicalQuery: "writing NOT adults", QueryProjectionVersion: "source-query-v1", PublicationYears: research.PublicationYears{From: 2023, To: 2026}}
	key := literatureSourceAttemptKey(base)
	for _, mutate := range []func(*research.SourceSearch){
		func(s *research.SourceSearch) { s.LogicalQuery = "writing NOT children" },
		func(s *research.SourceSearch) { s.InputQuery = "writing AI feedback" },
		func(s *research.SourceSearch) { s.Offset = 20 },
		func(s *research.SourceSearch) { s.PublicationYears.To = 2025 },
		func(s *research.SourceSearch) { s.QueryProjectionVersion = "" },
	} {
		next := base
		mutate(&next)
		if literatureSourceAttemptKey(next) == key {
			t.Fatal("different source attempt can clear another query's failure", next)
		}
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var replay research.SourceSearch
	if err := json.Unmarshal(encoded, &replay); err != nil || literatureSourceAttemptKey(replay) != key {
		t.Fatal("checkpoint replay changed source identity", err)
	}
	legacy := base
	legacy.QueryProjectionVersion = ""
	if literatureSourceAttemptKey(legacy) != "crossref:0:writing" {
		t.Fatal("legacy key was silently upgraded")
	}
	invalidA, invalidB := base, base
	invalidA.EffectiveQuery, invalidB.EffectiveQuery = "", ""
	invalidA.InputQuery, invalidB.InputQuery = "writing AND", "writing OR"
	if literatureSourceAttemptKey(invalidA) == literatureSourceAttemptKey(invalidB) {
		t.Fatal("invalid query inputs collided")
	}
}

func TestLiteratureSourceFailureIsNotClearedByAnotherLogicalQuery(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed-first", true: "failed-last"}[reverse], func(t *testing.T) {
			s, _, _, detail, node := literatureFixture(t, 0)
			failed := research.SourceSearch{SourceID: "crossref", LogicalQuery: "writing NOT adults", InputQuery: "writing", EffectiveQuery: "writing", ProviderQuery: "writing", QueryProjectionVersion: "source-query-v1", Status: research.SearchFailed, ErrorCode: research.FailureUnavailable}
			ok := failed
			ok.LogicalQuery = "writing NOT children"
			ok.Status = research.SearchOK
			ok.ErrorCode = ""
			sources := []research.SourceSearch{failed, ok}
			if reverse {
				sources[0], sources[1] = sources[1], sources[0]
			}
			detail.Steps[0].Output = mustJSON(map[string]any{"structured": map[string]any{"queryIds": []string{"q1", "q2"}, "queries": []string{failed.LogicalQuery, ok.LogicalQuery}, "sources": sources, "partial": false}})
			input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			var info literatureInput
			if err := json.Unmarshal(mustJSON(decodeObject(input)["_literature"]), &info); err != nil {
				t.Fatal(err)
			}
			if !info.State.SourceFailures || len(info.State.FailedSearches) != 1 || !info.State.FailedSearches[literatureSourceAttemptKey(failed)] {
				t.Fatal("different logical query cleared an unresolved source failure", info.State)
			}
		})
	}
}
