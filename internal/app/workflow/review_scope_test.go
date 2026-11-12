package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewScopeIndexesSuppliedEvidenceWithoutRequiringToolReads(t *testing.T) {
	input := raw(`{"evidenceContext":[{"quote":"Abstract A"},{"quote":""},{"quote":"Abstract B"}],"researchSourceContext":{"indexedReadRanges":[{"locator":"lines:1-20"}]}}`)
	node := CompiledNode{ID: "review", OutputSchema: trackedResearchReviewSchema()}
	scope := independentReviewScope(RunDetail{}, Step{Input: input}, node)
	var inventory reviewScopeInventory
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(scope, researchDeliveryBoundaryInstruction+reviewEvidenceFollowupInstruction+reviewScopeInstruction))), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.SuppliedExcerpts) != 2 || inventory.SuppliedExcerpts[1].Pointer != "/evidenceContext/2/quote" || inventory.AdditionalReadLogPointer == "" {
		t.Fatalf("lost independent evidence channel: %+v", inventory)
	}
	for _, entry := range inventory.SuppliedExcerpts {
		value, requirement, err := reviewBasisAt(decodeObject(input), entry.Pointer)
		if err != nil || requirement || value == "" {
			t.Fatalf("invalid navigation pointer: %s: %v", entry.Pointer, err)
		}
	}
	withoutReads := independentReviewScope(RunDetail{}, Step{Input: raw(`{"evidenceContext":[{"quote":"Still supplied"}]}`)}, node)
	if !strings.Contains(withoutReads, "/evidenceContext/0/quote") || strings.Contains(withoutReads, `"additionalReadLogPointer"`) {
		t.Fatal("source excerpts incorrectly require a tool log")
	}
	if independentReviewScope(RunDetail{}, Step{Input: input}, CompiledNode{ID: "report", OutputSchema: dynamicReportSchema()}) != "" {
		t.Fatal("review context leaked into another stage")
	}
}

func TestReviewSharedDefectCanCoverMultipleCriteria(t *testing.T) {
	input, output := acceptanceFixture(t)
	stage := decodeObject(input)
	stage["reviewHistory"] = reviewHistory{IssuePrefix: "issue-1-"}
	stage["context"] = map[string]any{"markdown": "变量编码表将在以后编制。"}
	input = mustJSON(stage)
	output["approved"] = false
	output["reviewedInputSha256"] = hashJSON(input)
	output["methodIssues"] = []string{"缺少变量编码表正文"}
	output["requiredCorrections"] = []string{"补齐编码表草稿"}
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "not_met", "issue-1-1：同一缺项"}, {"criterion-2", "not_met", "issue-1-1：同一缺项，不重复返修"}}
	output["reviewFindings"] = []trackedReviewFinding{{ID: "issue-1-1", Status: "open", Category: "methodIssues", Summary: "缺少变量编码表正文", Location: "正文", Basis: []reviewBasis{{"/context/markdown", "变量编码表将在以后编制。"}}, Reason: "只有待办", Origin: "new", ChangeReason: "首次发现", CorrectionIndex: 0}}
	if err := validateResearchAcceptance(mustJSON(output), input); err != nil {
		t.Fatal(err)
	}
	if err := validateTrackedReview(mustJSON(output), input); err != nil {
		t.Fatal("host demands duplicate finding per criterion", err)
	}
}

// Optional replay of the user's captured input, without reading/writing the
// application database or sending any request to a real model.
func TestCapturedReviewScopePreservesRejection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_SCOPE_REPLAY")
	if dir == "" {
		t.Skip("no scoped capture supplied")
	}
	input, err := os.ReadFile(filepath.Join(dir, "input_json.json"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(filepath.Join(dir, "output_json.json"))
	if err != nil {
		t.Fatal(err)
	}
	node := CompiledNode{ID: "independent_review", PromptVersion: "dynamic-independent-review-v8-focused-v1", OutputSchema: trackedResearchReviewSchema()}
	step := Step{Input: input, InputSHA256: hashJSON(input)}
	scope := independentReviewScope(RunDetail{}, step, node)
	var inventory reviewScopeInventory
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(scope, researchDeliveryBoundaryInstruction+reviewEvidenceFollowupInstruction+reviewScopeInstruction))), &inventory); err != nil || len(inventory.SuppliedExcerpts) != 10 {
		t.Fatalf("actual supplied excerpts lost: %v %+v", err, inventory)
	}
	analysis, err := outputPort(output, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	// Guidance must not silently rewrite the old scientific verdict, findings,
	// repair destinations or digest. Only a new model review may reassess them.
	normalized, _, err := normalizeWorkflowAIStageSubmissionForInput(string(analysis), node, input)
	if err != nil || !rawJSONEqual(analysis, normalized) {
		t.Fatal("historical review changed", err)
	}
	if err := validateWorkflowAIStageOutput(node, normalized, input); err != nil {
		t.Fatal(err)
	}
}

func TestReviewScopeDoesNotChangeFrozenInputOrOldReviewProtocol(t *testing.T) {
	input, err := os.ReadFile("testdata/review_submission/frozen-input.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []json.RawMessage{independentReviewSchema(), researchAcceptanceReviewSchema(), trackedResearchReviewSchema()} {
		node := CompiledNode{ID: "independent_review", PromptVersion: "dynamic-independent-review-v7-focused-v1", OutputSchema: schema}
		step := Step{Input: input, InputSHA256: hashJSON(input)}
		beforeNode, beforeStep := string(mustJSON(node)), string(mustJSON(step))
		prompt := buildAIStagePrompt(RunDetail{}, step, node)
		for _, text := range []string{reviewScopeInstruction, step.InputSHA256} {
			if !strings.Contains(strings.ReplaceAll(prompt, "\r\n", "\n"), strings.ReplaceAll(text, "\r\n", "\n")) {
				t.Fatal("missing scope guidance or modified frozen input")
			}
		}
		start := strings.Index(prompt, "<stage_input>\n") + len("<stage_input>\n")
		end := strings.Index(prompt[start:], "\n</stage_input>") + start
		if end < start || !rawJSONEqual(raw(prompt[start:end]), input) {
			t.Fatal("current-first key ordering changed frozen values")
		}
		if strings.Contains(prompt, "If its checkedScope lacks a matching document/locator") {
			t.Fatal("unconditional log-only rejection retained")
		}
		if beforeNode != string(mustJSON(node)) || beforeStep != string(mustJSON(step)) {
			t.Fatal("historical state mutated")
		}
		version := reportExecutionPromptVersion(node, prompt)
		wantVersion := node.PromptVersion
		if usesTrackedReview(node) {
			wantVersion += "-" + reviewRepairVersion
		}
		if version != wantVersion+"-"+reviewScopeVersion+"-"+reportDeliverableVersion {
			t.Fatal(version)
		}
		node.PromptVersion = version
		if reportExecutionPromptVersion(node, prompt) != version {
			t.Fatal("duplicate prompt version suffix")
		}
	}
}

func TestReviewScopeEnumeratesOnlyActualDownstreamGates(t *testing.T) {
	node := CompiledNode{ID: "review", OutputSchema: trackedResearchReviewSchema()}
	gate := CompiledNode{ID: "publish", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}}
	// Use the gate identity recognized by the runtime, independent of node name.
	if !isReviewGateNode(gate) {
		t.Fatal("fixture is not a review gate")
	}
	unrelated := gate
	unrelated.ID = "other_gate"
	detail := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{node, gate, unrelated}, Edges: []Edge{
		{FromNode: "intermediate", ToNode: "publish"},
		{FromNode: "review", ToNode: "intermediate"},
	}}}}
	scope := independentReviewScope(detail, Step{Input: raw(`{}`)}, node)
	var inventory reviewScopeInventory
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(scope, researchDeliveryBoundaryInstruction+reviewEvidenceFollowupInstruction+reviewScopeInstruction))), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.DownstreamDeliveryGateIDs) != 1 || inventory.DownstreamDeliveryGateIDs[0] != "publish" {
		t.Fatal(inventory.DownstreamDeliveryGateIDs)
	}
}
