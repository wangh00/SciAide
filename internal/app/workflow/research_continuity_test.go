package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHistoricalStageReadPreservesLargeCodeAndPagingIdentity(t *testing.T) {
	code := strings.Repeat("# 中文代码\nprint('result')\n", 2000)
	snapshot := rawObject(map[string]any{"steps": []Step{{NodeID: "method_implementation", Attempt: 2, Status: StepCompleted, Input: raw(`{"parameter":3}`), Output: rawObject(map[string]any{"analysis": map[string]any{"code": code}})}}})
	cmd := ResearchTaskReadCommand{Section: "revision_stage_output", RevisionID: "revision-a", NodeID: "method_implementation"}
	value, err := researchRevisionStageRecord(snapshot, cmd)
	if err != nil {
		t.Fatal(err)
	}
	var recovered strings.Builder
	for {
		var page struct {
			Content    string `json:"content"`
			Next       int    `json:"nextOffset"`
			More       bool   `json:"hasMore"`
			RevisionID string `json:"revisionId"`
		}
		if err := json.Unmarshal(researchDiscussionPage(value, cmd), &page); err != nil {
			t.Fatal(err)
		}
		if page.RevisionID != cmd.RevisionID {
			t.Fatal("lost revision identity")
		}
		recovered.WriteString(page.Content)
		if !page.More {
			break
		}
		if page.Next <= cmd.Offset {
			t.Fatal("paging stalled")
		}
		cmd.Offset = page.Next
	}
	if recovered.String() != string(value) {
		t.Fatal("historical code truncated or changed")
	}
	cmd.NodeID = "missing"
	if _, err := researchRevisionStageRecord(snapshot, cmd); err == nil {
		t.Fatal("unknown stage accepted")
	}
}

func TestImplementationPromptUpdatesKeepV5Executable(t *testing.T) {
	for _, version := range []string{"dynamic-implementation-v5", dynamicImplementationPromptVersion, dynamicImplementationPromptVersion + "-ds1"} {
		node := CompiledNode{ID: "method_implementation", PromptVersion: version}
		if err := validateAIStageProtocol(node); err != nil {
			t.Fatal(err)
		}
		if _, err := implementationConfirmation(node, raw(`{}`), raw(`{"researchChanges":[]}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := implementationConfirmation(node, raw(`{}`), raw(`{}`)); err == nil {
			t.Fatal("compatible version bypassed existing change confirmation")
		}
	}
}

func TestResearchArtifactInventoryKeepsProducerIdentity(t *testing.T) {
	d := RunDetail{Steps: []Step{
		{NodeID: "later", Ordinal: 2, Attempt: 3, Status: StepCompleted, Output: raw(`{"artifacts":[{"id":"b","workspacePath":"analysis-output/b.csv","sha256":"b-hash"}]}`)},
		{NodeID: "failed", Ordinal: 0, Status: StepFailed, Output: raw(`{"artifacts":[{"id":"not-produced"}]}`)},
		{NodeID: "first", Ordinal: 1, Attempt: 1, Status: StepCompleted, Output: raw(`{"artifacts":[{"id":"a","workspacePath":"analysis-output/a.png"}]}`)},
	}}
	items := researchArtifactInventory(d)
	if len(items) != 2 || items[0].NodeID != "first" || items[1].Attempt != 3 || items[1].Artifact.SHA256 != "b-hash" {
		t.Fatalf("inventory=%+v", items)
	}
	if len(researchArtifactInventory(RunDetail{})) != 0 {
		t.Fatal("empty run invented artifacts")
	}
}

func TestComparisonBaselinePersistsAndReachesRevisedStage(t *testing.T) {
	d := RunDetail{Run: Run{Inputs: raw(`{}`)}, Steps: []Step{
		{NodeID: "method_implementation", Ordinal: 0, Attempt: 1, Status: StepCompleted, Output: raw(`{"analysis":{"methodSummary":"original method"}}`)},
		{NodeID: "python_analysis", Ordinal: 1, Attempt: 1, Status: StepCompleted, Output: raw(`{"structured":{"mean":12.5}}`)},
		{NodeID: "report_drafting", Ordinal: 2, Status: StepQueued},
	}}
	baseline := researchComparisonBaseline(d)
	d.Events = []RuntimeEvent{{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{NodeID: "method_implementation", Changes: []string{"compare methods"}}, "startOrdinal": 0, "comparisonBaseline": baseline})}}
	// Round-trip durable state and clear current results as replay does.
	encoded, _ := json.Marshal(d)
	var restored RunDetail
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	restored.Steps[1].Output = raw(`{}`)
	input, err := bindNodeInput(restored, CompiledNode{ID: "report_drafting", Kind: NodeAgentStage, Arguments: raw(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(input), "12.5") || !strings.Contains(string(input), "original method") || !strings.Contains(string(input), "comparisonBaseline") {
		t.Fatal(string(input))
	}
	if len(priorResearchComparison(RunDetail{})) != 0 {
		t.Fatal("legacy run should not require a baseline")
	}
}

func TestComparisonBaselineMarksTruncation(t *testing.T) {
	d := RunDetail{Steps: []Step{{NodeID: "python_analysis", Status: StepCompleted, Output: rawObject(map[string]any{"text": strings.Repeat("大", 50000)})}}}
	b := researchComparisonBaseline(d)
	if !json.Valid(b) || !strings.Contains(string(b), `"truncated":true`) || len(b) > 30000 {
		t.Fatal("unbounded or unmarked comparison baseline")
	}
}

func TestRevisionRetainsReadablePreviousArtifactPaths(t *testing.T) {
	d := RunDetail{Steps: []Step{{NodeID: "python_analysis", Status: StepCompleted, Attempt: 1, Output: raw(`{"artifacts":[{"id":"a","workspacePath":"analysis-output/previous-results.json","sha256":"old-hash"}]}`)}}}
	d.Events = []RuntimeEvent{{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"comparisonBaseline": researchComparisonBaseline(d)})}}
	d.Steps = nil
	files := priorResearchArtifacts(d)
	if len(files) != 1 || files[0].Artifact.SHA256 != "old-hash" {
		t.Fatal(files)
	}
	if !strings.Contains(trustedWorkspaceFilesInstruction(d), "analysis-output/previous-results.json") {
		t.Fatal("previous outputs disappeared from read inventory after replay")
	}
	// A newer legacy revision must not accidentally use an unrelated old baseline.
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: raw(`{}`)})
	if len(priorResearchArtifacts(d)) != 0 {
		t.Fatal("stale baseline reused")
	}
}

func TestCapabilitiesComposeExistingStagesWithoutGrantingTools(t *testing.T) {
	stages := map[string]bool{}
	for _, stage := range dynamicResearchStageCatalog() {
		stages[stage.StageID] = true
	}
	for _, capability := range researchCapabilities() {
		if len(capability.Outputs) == 0 || len(capability.Limitations) == 0 {
			t.Fatal(capability.ID)
		}
		for _, id := range capability.Stages {
			if !stages[id] {
				t.Fatalf("unknown stage %s", id)
			}
		}
	}
	p := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, CompiledNode{ID: "explore", OutputSchema: raw(`{}`)})
	for _, want := range []string{"not a discipline whitelist", "never add tools or permissions", "Do not require a category selection"} {
		if !strings.Contains(p, want) {
			t.Fatal(want)
		}
	}
}
