package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestLiteratureSourceSelectionMustBeStructured(t *testing.T) {
	plan := decodeObject(raw(`{"queries":["AI writing feedback"],"rationale":["same topic"],"publicationYears":{"from":0,"to":0},"yearEvidence":"","providerQueries":{"pubmed":["AI writing"],"europepmc":["TITLE_ABS:writing"],"crossref":["AI writing"],"openalex":["AI AND writing"]},"retrievalProfile":"education","sourceRationale":"Only ERIC, OpenAlex and Crossref."}`))
	v := tool.JSONSchemaValidator{}
	if err := v.Validate(literatureRetrievalSchema(), mustJSON(plan)); err == nil {
		t.Fatal("prose-only source promise accepted without structured sourceIds")
	}
	for _, ids := range [][]string{{"eric", "openalex", "crossref"}, {}} {
		plan["sourceIds"] = ids
		if len(ids) == 0 {
			plan["sourceRationale"] = "No database restriction; use the education profile."
			plan["providerQueries"] = map[string]any{"pubmed": []string{"AI writing"}, "europepmc": []string{"TITLE_ABS:writing"}, "crossref": []string{"AI writing"}, "openalex": []string{"AI AND writing"}}
		} else {
			plan["providerQueries"] = map[string]any{"eric": []string{"subject:Writing"}, "openalex": []string{"AI AND writing"}, "crossref": []string{"AI writing feedback"}}
		}
		if err := v.Validate(literatureRetrievalSchema(), mustJSON(plan)); err != nil {
			t.Fatal("explicit sources or explicit profile fallback refused", err)
		}
	}
	for _, ids := range [][]string{{"unknown"}, {"eric", "eric"}} {
		plan["sourceIds"] = ids
		if err := v.Validate(literatureRetrievalSchema(), mustJSON(plan)); err == nil {
			t.Fatal("unknown or duplicate sources accepted", ids)
		}
	}
}

func TestLiteratureFrozenSourceSelectionCannotExpandDuringContinuation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		next    []string
		wantErr bool
	}{
		{"preserve", nil, false},
		{"retry-subset", []string{"crossref"}, false},
		{"expand", []string{"eric", "europepmc"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, detail, _ := literatureFixture(t, 0)
			detail.Run.Compilation.Nodes = append(detail.Run.Compilation.Nodes, CompiledNode{ID: "literature_query_expansion", PromptVersion: literatureRetrievalVersion})
			detail.Steps = append(detail.Steps, Step{NodeID: "literature_query_expansion", Status: StepCompleted, Output: raw(`{"analysis":{"sourceIds":["eric","openalex","crossref"]}}`)})
			detail.Events = []RuntimeEvent{{Type: literatureCheckpointEvent, Payload: mustJSON(map[string]any{"discoveryId": "discovery", "state": literatureCheckpoint{Phase: "search", NextQueries: []string{"new feedback topic"}, NextSources: tc.next}})}}
			input, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "literature_discovery"}, raw(`{"query":"old topic","retrievalProfile":"education","sourceIds":["eric","openalex","crossref"]}`))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "frozen sourceIds") {
					t.Fatal("continuation expanded frozen sources", string(input), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var args struct{ SourceIDs []string }
			if err := json.Unmarshal(input, &args); err != nil {
				t.Fatal(err)
			}
			want := []string{"eric", "openalex", "crossref"}
			if len(tc.next) > 0 {
				want = tc.next
			}
			if strings.Join(args.SourceIDs, ",") != strings.Join(want, ",") {
				t.Fatal("source binding lost", string(input))
			}
		})
	}
}

func TestLiteratureSourceBindingInitialMissingAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name, version, output string
		wantErr               bool
		wantSources           string
	}{
		{"new-explicit-overrides-stale-input", literatureRetrievalVersion, `{"analysis":{"sourceIds":["eric","crossref"]}}`, false, "eric,crossref"},
		{"new-explicit-profile-clears-stale-input", literatureRetrievalVersion, `{"analysis":{"sourceIds":[]}}`, false, ""},
		{"new-missing-stops", literatureRetrievalVersion, `{"analysis":{}}`, true, ""},
		{"legacy-keeps-input", literatureRetrievalLegacyVersion, `{"analysis":{}}`, false, "pubmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, detail, _ := literatureFixture(t, 0)
			detail.Run.Compilation.Nodes = append(detail.Run.Compilation.Nodes, CompiledNode{ID: "literature_query_expansion", PromptVersion: tc.version})
			detail.Steps = append(detail.Steps, Step{NodeID: "literature_query_expansion", Status: StepCompleted, Output: raw(tc.output)})
			input, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "literature_discovery"}, raw(`{"query":"old topic","retrievalProfile":"education","sourceIds":["pubmed"]}`))
			if tc.wantErr {
				if err == nil {
					t.Fatal("new missing source plan silently fell back")
				}
				return
			}
			var args struct{ SourceIDs []string }
			if err != nil || json.Unmarshal(input, &args) != nil || strings.Join(args.SourceIDs, ",") != tc.wantSources {
				t.Fatal("wrong source binding or old frozen behavior changed", string(input), err)
			}
		})
	}
}
