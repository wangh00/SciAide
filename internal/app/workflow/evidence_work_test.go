package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestEvidenceWorkOptionalSchemaAndDownstreamIdentity(t *testing.T) {
	base := CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}
	input := raw(`{"_selectedEvidence":{"phase":"batch","documents":["d"]},"candidates":[]}`)
	node := selectedEvidencePhaseNode(base, input)
	for _, extra := range []string{"", `,"workProduct":"|领域|判断|依据|\n|分配隐藏|未知|当前摘录没有该过程|"`} {
		output := raw(`{"documentAnalyses":[{"documentId":"d","finding":"unknown","applicability":"limited","limitations":"excerpt only","references":[]` + extra + `}]}`)
		if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(output), node, input); err != nil {
			t.Fatal(err)
		}
		for _, direct := range []bool{false, true} {
			var data struct {
				Documents []json.RawMessage `json:"documentAnalyses"`
			}
			_ = json.Unmarshal(output, &data)
			in := mustJSON(map[string]any{"_selectedEvidence": selectedEvidenceInput{Direct: direct, Phase: "coverage", Documents: []string{"d"}, Analyses: data.Documents}})
			d := RunDetail{Steps: []Step{{NodeID: "evidence_screening", Status: StepCompleted, Attempt: 3, Input: in, InputSHA256: hashJSON(in), Output: mustJSON(map[string]any{"analysis": json.RawMessage(output)})}}}
			for _, target := range []CompiledNode{{ID: "method_selection"}, {ID: "report_drafting", PromptVersion: researchReportVersion}, {ID: "independent_review", OutputSchema: independentReviewSchema()}} {
				bound := addEvidenceWork(d, target, raw(`{}`))
				if !rawJSONEqual(mustJSON(decodeObject(bound)["evidenceWork"]), mustJSON(currentEvidenceWork(d))) || extra != "" && !strings.Contains(string(bound), "分配隐藏") {
					t.Fatal("work lost", string(bound))
				}
				if target.ID == "independent_review" {
					if err := validateReviewDependencies(d, Step{Input: bound}); err != nil {
						t.Fatal(err)
					}
				}
			}
			bound := addEvidenceWork(d, CompiledNode{ID: "independent_review", OutputSchema: independentReviewSchema()}, raw(`{}`))
			d.Steps[0].Attempt++
			if err := validateReviewDependencies(d, Step{Input: bound}); err == nil {
				t.Fatal("stale evidence work accepted")
			}
			d.Steps[0].Status = StepQueued
			if currentEvidenceWork(d) != nil {
				t.Fatal("incomplete evidence/history inherited")
			}
		}
	}
	if strings.Contains(string(base.OutputSchema), "workProduct") {
		t.Fatal("original route schema mutated")
	}
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(node.OutputSchema); err != nil {
		t.Fatal(err)
	}
}
func TestEvidenceWorkAndRecordMetadataAreNotScientificSupport(t *testing.T) {
	input := decodeObject(raw(`{"evidenceWork":{"inputSha256":"hash","interpretation":"guidance","documentAnalyses":[{"workProduct":"actual limited judgement"}]},"researchRecordAudit":{"status":"available","interpretation":"not trial dedup","queries":[{"sourceRecordCount":3}]}}`))
	for _, tc := range []struct {
		path     string
		metadata bool
	}{{"/evidenceWork/inputSha256", true}, {"/evidenceWork/interpretation", true}, {"/evidenceWork/documentAnalyses/0/workProduct", false}, {"/researchRecordAudit/status", true}, {"/researchRecordAudit/queries/0/sourceRecordCount", false}} {
		_, meta, err := reviewBasisAt(input, tc.path)
		if err != nil || meta != tc.metadata {
			t.Fatal(tc, meta, err)
		}
	}
}
