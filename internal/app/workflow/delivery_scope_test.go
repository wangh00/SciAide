package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func scopeFixture() RunDetail {
	return RunDetail{Run: Run{Inputs: raw(`{"contract":{"researchQuestion":"original question","objectives":["full research goal"],"successCriteria":["complete appraisal","map overlap"]}}`), Compilation: Compilation{Nodes: []CompiledNode{{ID: "report_drafting", Kind: NodeAgentStage}, {ID: "independent_review", Kind: NodeAgentStage, OutputSchema: researchAcceptanceReviewSchema()}}, Edges: []Edge{{FromNode: "$input", FromPort: "contract", ToNode: "report_drafting", ToPort: "researchContract"}, {FromNode: "$input", FromPort: "contract", ToNode: "independent_review", ToPort: "researchContract"}}}}}
}
func scopeCommand() ProposeResearchRevisionCommand {
	return ProposeResearchRevisionCommand{NodeID: "report_drafting", DeliveryScope: &DeliveryScopeSelection{Summary: " Deliver bounded evidence map, not completed systematic review ", SuccessCriteria: []string{" provide observed outcomes ", "disclose unverified appraisal/overlap and avoid independent-study totals"}}}
}
func confirmScope(d *RunDetail, scope *ResearchDeliveryScope) {
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: mustJSON(map[string]any{"proposal": ResearchRevisionProposal{NodeID: "report_drafting", DeliveryScope: scope}})})
}
func TestDeliveryScopeRequiresConfirmationAndPersistsAcrossOrdinaryRepair(t *testing.T) {
	d := scopeFixture()
	original := cloneRaw(d.Run.Inputs)
	scope, err := prepareDeliveryScope(d, scopeCommand())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(scope.PreviousCriteria, ",") != "complete appraisal,map overlap" || scope.SuccessCriteria[0] != "provide observed outcomes" {
		t.Fatal(scope)
	}
	d.RevisionProposals = []ResearchRevisionProposal{{DeliveryScope: scope, Status: "pending"}}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.revision_proposed", Payload: mustJSON(map[string]any{"proposal": d.RevisionProposals[0]})})
	for _, e := range d.Run.Compilation.Edges {
		v, _ := resolveWorkflowEdgeValue(d, e)
		if strings.Contains(string(v), "provide observed") {
			t.Fatal("pending proposal changed criteria")
		}
	}
	confirmScope(&d, scope)
	for _, e := range d.Run.Compilation.Edges {
		v, err := resolveWorkflowEdgeValue(d, e)
		if err != nil || !strings.Contains(string(v), "provide observed outcomes") || !strings.Contains(string(v), "original question") || !strings.Contains(string(v), "full research goal") {
			t.Fatal(string(v), err)
		}
	}
	bound, err := bindNodeInput(d, d.Run.Compilation.Nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	var stage struct {
		Criteria []ResearchAcceptanceCriterion `json:"acceptanceCriteria"`
	}
	_ = json.Unmarshal(bound, &stage)
	if len(stage.Criteria) != 2 || stage.Criteria[0].Text != scope.SuccessCriteria[0] {
		t.Fatal("review kept old criteria", string(bound))
	}
	node := d.Run.Compilation.Nodes[1]
	node.Prompt = "researchContract 是最初冻结的研究约定"
	prompt := buildAIStagePrompt(d, Step{Input: bound, InputSHA256: hashJSON(bound)}, node)
	if strings.Contains(prompt, "researchContract 是最初冻结的研究约定") || !strings.Contains(prompt, "不恢复 previousCriteria") {
		t.Fatal("contradictory legacy scope instruction")
	}
	output := map[string]any{"approved": true, "acceptanceChecks": []ResearchAcceptanceCheck{{"criterion-1", "not_met", "missing table"}, {"criterion-2", "met", "disclosed"}}, "requiredCorrections": []string{"provide table"}}
	if err := validateResearchAcceptance(mustJSON(output), bound); err == nil {
		t.Fatal("scope change automatically approves missing actual work")
	}
	output["approved"] = false
	if err := validateResearchAcceptance(mustJSON(output), bound); err != nil {
		t.Fatal(err)
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: mustJSON(map[string]any{"proposal": ResearchRevisionProposal{NodeID: "evidence_screening"}})})
	if currentDeliveryScope(d) == nil {
		t.Fatal("ordinary repair reset confirmed criteria")
	}
	second, err := prepareDeliveryScope(d, scopeCommand())
	if err != nil || strings.Join(second.PreviousCriteria, "|") != strings.Join(scope.SuccessCriteria, "|") {
		t.Fatal("renegotiation lost effective baseline", err)
	}
	var restarted RunDetail
	if err = json.Unmarshal(mustJSON(d), &restarted); err != nil {
		t.Fatal(err)
	}
	if !rawJSONEqual(mustJSON(currentDeliveryScope(restarted)), mustJSON(scope)) || !rawJSONEqual(d.Run.Inputs, original) {
		t.Fatal("restart lost scope or original was overwritten")
	}
}
func TestDeliveryScopeRejectsUnsupportedTargetAndIncompleteConditions(t *testing.T) {
	for _, mutate := range []func(*ProposeResearchRevisionCommand){func(c *ProposeResearchRevisionCommand) { c.NodeID = "method_selection" }, func(c *ProposeResearchRevisionCommand) { c.DeliveryScope.Summary = " " }, func(c *ProposeResearchRevisionCommand) { c.DeliveryScope.SuccessCriteria = nil }, func(c *ProposeResearchRevisionCommand) { c.DeliveryScope.SuccessCriteria = []string{" "} }} {
		c := scopeCommand()
		mutate(&c)
		if _, err := prepareDeliveryScope(scopeFixture(), c); err == nil {
			t.Fatal("invalid scope accepted", c)
		}
	}
	if _, err := prepareDeliveryScope(RunDetail{}, scopeCommand()); err == nil {
		t.Fatal("missing contract invented")
	}
	if s, err := prepareDeliveryScope(RunDetail{}, ProposeResearchRevisionCommand{}); err != nil || s != nil {
		t.Fatal("ordinary repair changed")
	}
}

func TestDeliveryScopeOnlyRebindsReportAndIndependentReview(t *testing.T) {
	d := scopeFixture()
	for _, id := range []string{"method_selection", "research_design", "method_implementation", "result_interpretation"} {
		d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: id, Kind: NodeAgentStage})
		d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "$input", FromPort: "contract", ToNode: id, ToPort: "researchContract"})
	}
	scope, err := prepareDeliveryScope(d, scopeCommand())
	if err != nil {
		t.Fatal(err)
	}
	confirmScope(&d, scope)
	for _, edge := range d.Run.Compilation.Edges {
		contract, err := resolveWorkflowEdgeValue(d, edge)
		if err != nil {
			t.Fatal(err)
		}
		var actual struct {
			ResearchQuestion string   `json:"researchQuestion"`
			Objectives       []string `json:"objectives"`
			Criteria         []string `json:"successCriteria"`
			DeliveryScope    any      `json:"deliveryScope"`
		}
		if err := json.Unmarshal(contract, &actual); err != nil || actual.ResearchQuestion != "original question" || strings.Join(actual.Objectives, ",") != "full research goal" {
			t.Fatalf("%s changed the scientific question: %s: %v", edge.ToNode, contract, err)
		}
		if edge.ToNode == "report_drafting" || edge.ToNode == "independent_review" {
			if strings.Join(actual.Criteria, ",") != strings.Join(scope.SuccessCriteria, ",") || actual.DeliveryScope == nil {
				t.Fatalf("%s lost confirmed delivery conditions: %s", edge.ToNode, contract)
			}
		} else if strings.Join(actual.Criteria, ",") != "complete appraisal,map overlap" || actual.DeliveryScope != nil {
			t.Fatalf("%s received delivery-only conditions in upstream method: %s", edge.ToNode, contract)
		}
	}
	// A later ordinary upstream repair must not reset the confirmed delivery
	// conditions or rewrite the original method/design contract.
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: mustJSON(map[string]any{"proposal": ResearchRevisionProposal{NodeID: "method_selection"}})})
	for _, edge := range d.Run.Compilation.Edges {
		contract, err := resolveWorkflowEdgeValue(d, edge)
		if err != nil {
			t.Fatal(err)
		}
		current := strings.Contains(string(contract), "provide observed outcomes")
		if current != (edge.ToNode == "report_drafting" || edge.ToNode == "independent_review") {
			t.Fatalf("ordinary repair changed the wrong stage %s: %s", edge.ToNode, contract)
		}
	}
}

func TestDeliveryScopePromptUsesOneCurrentAcceptanceList(t *testing.T) {
	d := scopeFixture()
	scope, err := prepareDeliveryScope(d, scopeCommand())
	if err != nil {
		t.Fatal(err)
	}
	confirmScope(&d, scope)
	for _, node := range d.Run.Compilation.Nodes {
		bound, err := bindNodeInput(d, node)
		if err != nil {
			t.Fatal(err)
		}
		prompt := buildAIStagePrompt(d, Step{Input: bound, InputSHA256: hashJSON(bound)}, node)
		for _, phrase := range []string{"researchContract.successCriteria as the current acceptance list", "previousCriteria is history, not an additional completion gate", "Neither limited-evidence acceptance nor ordinary repair changes the current list"} {
			if !strings.Contains(prompt, phrase) {
				t.Fatalf("%s missing scope precedence %q", node.ID, phrase)
			}
		}
		if strings.Contains(prompt, "Preserve the original scope, evidence and acceptance criteria.") {
			t.Fatalf("%s still has contradictory old acceptance instruction", node.ID)
		}
		if isIndependentReviewSchema(node.OutputSchema) && !strings.Contains(reviewScopeInstruction, "confirmed current successCriteria takes precedence over previousCriteria") {
			t.Fatal("independent review reimposes historical delivery conditions")
		}
	}
}
