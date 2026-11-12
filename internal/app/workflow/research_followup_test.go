package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOriginalDeliveryCriterionPreservesFrozenLegacyAndDraftMaturity(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		input, output := acceptanceFixture(t)
		stage := decodeObject(input)
		criteria, err := researchAcceptanceCriteria(mustJSON(stage["researchContract"]))
		if err != nil {
			t.Fatal(err)
		}
		criteria = appendOriginalDeliveryCriterion(criteria)
		if legacy {
			criteria[len(criteria)-1].Text = legacyOriginalDeliveryCriterion
		}
		stage["acceptanceCriteria"] = criteria
		stage["requireOriginalDelivery"] = true
		stage["reviewHistory"] = reviewHistory{IssuePrefix: "issue-1-"}
		output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "研究对象已提供"}, {"criterion-2", "met", "分析草稿已提供"}, {"criterion-3", "met", "明确允许待核验的设计草稿，实际内容已提供"}}
		if err := validateResearchAcceptance(mustJSON(output), mustJSON(stage)); err != nil {
			t.Fatal(err)
		}
		// The maturity fix must not waive an actual missing deliverable.
		output["acceptanceChecks"].([]ResearchAcceptanceCheck)[1].Status = "not_met"
		if validateResearchAcceptance(mustJSON(output), mustJSON(stage)) == nil {
			t.Fatal("false approval accepted")
		}
	}
}

func TestFollowupReadStrategyRetainsRelevantFullEvidenceAndNoNewToolLimit(t *testing.T) {
	for _, boundary := range []string{"not a full-task audit", "explicitly requested comprehensive audit", "completely with nextOffset/hasMore", "sha256 are unchanged", "complete contents are no longer available", "not a tool-call limit", "state that access limitation early"} {
		if !strings.Contains(researchEvidenceFollowupInstruction, boundary) {
			t.Fatal("missing reading/quality boundary", boundary)
		}
	}
	output := json.RawMessage(`{"结果":"已有材料"}`)
	detail := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{{ID: "custom-review", OutputSchema: trackedResearchReviewSchema()}, {ID: "custom-delivery"}}, Outputs: []Output{{Name: "custom-result", FromNode: "custom-delivery"}}}}, Steps: []Step{{NodeID: "custom-delivery", Ordinal: 2, Attempt: 3, Output: output}, {NodeID: "custom-review", Ordinal: 1}}}
	stages := researchDiscussionStages(detail)
	if stages[0]["nodeId"] != "custom-review" || stages[0]["isReview"] != true || stages[1]["outputSha256"] != hashJSON(output) || stages[1]["outputCharacters"] != len([]rune(string(output))) || !reflect.DeepEqual(stages[1]["publishedOutputs"], []string{"custom-result"}) {
		t.Fatal(stages)
	}
	// Index is metadata, not a copied/rewritten output or a fixed route name.
	if _, exists := stages[1]["output"]; exists || string(detail.Steps[0].Output) != string(output) {
		t.Fatal("evidence changed")
	}
}

func TestRecoveryGuidanceRespectsEvidenceAdoptionAndHumanChoices(t *testing.T) {
	node := CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, node)
	for _, text := range []string{researchDeliveryBoundaryInstruction, reviewEvidenceFollowupInstruction, "generic host-added", "specific substantive requirement", "draft versus implementation-ready"} {
		if !strings.Contains(prompt, text) {
			t.Fatal("missing review scope", text)
		}
	}
	for _, text := range []string{"publicly discoverable", "read-only investigation", "Never pass URLs or invented IDs", "user-input question-card", "does not automatically unlock", "single existing confirmation"} {
		if !strings.Contains(researchEvidenceFollowupInstruction, text) {
			t.Fatal("missing recovery boundary", text)
		}
	}
}
