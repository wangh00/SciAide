package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestLiteratureV4ReallyExecutesSemanticValidation(t *testing.T) {
	bad := raw(`{"queries":["complete topic"],"publicationYears":{"from":2020,"to":2025},"yearEvidence":"invented quote"}`)
	for _, version := range []string{"dynamic-literature-query-expansion-v3", "dynamic-literature-query-expansion-v4"} {
		if err := validateWorkflowAIStageOutput(CompiledNode{ID: "literature_query_expansion", PromptVersion: version}, bad, raw(`{"originalRequest":"published 2020 to 2025"}`)); err == nil {
			t.Fatal("semantic validation bypassed", version)
		}
	}
}

func TestLiteratureRetrievalBoundariesStayDistinctThroughScreeningCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		code research.FailureCode
		want string
	}{
		{"planned-deferred-only", "", "assessed"},
		{"local-slot-budget", research.FailureRequestBudget, "request_budget_exhausted"},
		{"upstream-rate-limit", research.FailureRateLimited, "source_blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _, detail, node := literatureFixture(t, 0)
			status := research.SearchOK
			if tc.code != "" {
				status = research.SearchFailed
			}
			detail.Steps[0].Output = mustJSON(map[string]any{"structured": map[string]any{"queryIds": []string{"q1"}, "queries": []string{"original"}, "partial": tc.code != "", "sources": []research.SourceSearch{{SourceID: "eric", Status: status, ErrorCode: tc.code}}, "searchPlan": map[string]any{"allocation": map[string]any{"deferredSlots": []any{map[string]any{"sourceId": "arxiv", "queryIndex": 1}}}}}})
			input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			args := decodeObject(input)
			var info literatureInput
			json.Unmarshal(mustJSON(args["_literature"]), &info)
			if (tc.code == research.FailureRequestBudget && info.State.SourceFailures) || len(info.State.SearchPlans) != 1 {
				t.Fatal(info.State)
			}
			info.Phase = "coverage"
			args["_literature"] = info
			input = mustJSON(args)
			output := screenFixture(input, true)
			step := detail.Steps[1]
			step.Input = input
			if err := s.completeLiteratureScreening(context.Background(), detail, step, node, AIExecution{ID: "coverage", Status: "completed", Output: output}, output); err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Analysis struct {
					Retrieval struct {
						Status      string
						SearchPlans []json.RawMessage `json:"searchPlans"`
					} `json:"retrieval"`
					Summary string
				} `json:"analysis"`
			}
			if json.Unmarshal(repo.output, &saved) != nil || saved.Analysis.Retrieval.Status != tc.want || len(saved.Analysis.Retrieval.SearchPlans) != 1 {
				t.Fatal(string(repo.output))
			}
			if tc.code == research.FailureRequestBudget && strings.Contains(saved.Analysis.Summary, "部分文献来源未成功返回") {
				t.Fatal("local allocation mislabeled as upstream failure")
			}
		})
	}
}

func TestLiteratureV6SchemaAndRouteCarryRetrievalContract(t *testing.T) {
	valid := raw(`{"queries":["AI writing feedback"],"rationale":["same topic"],"publicationYears":{"from":0,"to":0},"yearEvidence":"","providerQueries":{"eric":["subject:Writing"],"crossref":["AI writing feedback"],"openalex":["AI AND writing"]},"retrievalProfile":"education","sourceRationale":"University writing feedback concerns education, not computer engineering alone.","sourceIds":["eric","openalex","crossref"]}`)
	if err := (tool.JSONSchemaValidator{}).Validate(literatureRetrievalSchema(), valid); err != nil {
		t.Fatal(err)
	}
	n := CompiledNode{ID: "literature_query_expansion", PromptVersion: literatureRetrievalVersion}
	if err := validateWorkflowAIStageOutput(n, valid, raw(`{}`)); err != nil {
		t.Fatal(err)
	}
	forbidden := decodeObject(valid)
	forbidden["providerQueries"] = map[string]any{"eric": []string{"subject:Writing"}, "openalex": []string{"AI AND writing"}, "crossref": []string{"AI writing feedback"}, "pubmed": []string{"AI writing"}}
	if err := validateWorkflowAIStageOutput(n, mustJSON(forbidden), raw(`{}`)); err == nil || !strings.Contains(err.Error(), "not selected") {
		t.Fatalf("unselected provider query was accepted: %v", err)
	}
	obj := decodeObject(valid)
	obj["retrievalProfile"] = "AI"
	if err := validateWorkflowAIStageOutput(n, mustJSON(obj), raw(`{}`)); err == nil {
		t.Fatal("unknown profile accepted")
	}
	route := starterDesignRoute()
	route.RouteID = "retrieval_contract"
	route.StageIDs = append([]string{"question_refinement", "literature_discovery", "candidate_review", "evidence_extraction"}, route.StageIDs[1:]...)
	route.Layers = append([]ResearchRouteLayer{route.Layers[0], {LayerID: "evidence", Title: "证据", Objective: "检索核验", Stages: []ResearchRouteStage{{StageID: "literature_discovery", Objective: "发现文献", Outputs: []string{"候选"}}, {StageID: "candidate_review", Objective: "选定材料", Outputs: []string{"材料"}}, {StageID: "evidence_extraction", Objective: "核验引用", Outputs: []string{"证据"}}}}}, route.Layers[1:]...)
	template, _, _, err := dynamicResearchRouteTemplate(route, ResearchStarterContext{ResearchIdea: "education question", StageCatalog: dynamicResearchStageCatalog()}, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range template.Definition.Nodes {
		if node.ID == n.ID {
			found = node.PromptVersion == literatureRetrievalVersion && strings.Contains(node.Prompt, "14次实际来源HTTP")
		}
	}
	if !found {
		t.Fatal("new route did not opt in")
	}
	for _, port := range []string{"retrievalProfile", "sourceRationale", "sourceIds", "searchPlan"} {
		found = false
		for _, edge := range template.Definition.Edges {
			found = found || edge.ToPort == port
		}
		if !found {
			t.Fatal("missing route edge", port)
		}
	}
}

func TestLiteratureSupplementDoesNotReuseStaleProviderQueriesOrExpandExplicitSources(t *testing.T) {
	s, _, _, detail, _ := literatureFixture(t, 0)
	detail.Events = []RuntimeEvent{{Type: literatureCheckpointEvent, Payload: mustJSON(map[string]any{"discoveryId": "discovery", "state": literatureCheckpoint{Phase: "search", NextQueries: []string{"new feedback topic"}}})}}
	input, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "literature_discovery"}, raw(`{"query":"old topic","queries":["first old complete query"],"sourceIds":["eric"],"retrievalProfile":"education","providerQueries":{"eric":["old projection"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var args struct {
		Query           string
		SourceIDs       []string
		Profile         string `json:"retrievalProfile"`
		ProviderQueries map[string][]string
	}
	if err := json.Unmarshal(input, &args); err != nil {
		t.Fatal(err)
	}
	if args.Query != "new feedback topic" || len(args.ProviderQueries) != 0 || len(args.SourceIDs) != 1 || args.SourceIDs[0] != "eric" || args.Profile != "education" {
		t.Fatal(string(input))
	}
}
