package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func boundDataFixture() RunDetail {
	d := revisionDataFixture()
	d.Run.ID, d.Run.WorkflowPurpose = "formal-run", PurposeUserPlan
	d.Run.InputsSHA256 = hashJSON(d.Run.Inputs)
	d.Events = []RuntimeEvent{{WorkflowRunID: d.Run.ID, Type: "workflow.created", Payload: rawObject(map[string]any{"inputsSha256": d.Run.InputsSHA256})}}
	return d
}

func TestCurrentDataBindingRequiresPersistedAdoptionAndFrozenFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RunDetail)
		bound  bool
	}{
		{"formal adopted start", func(*RunDetail) {}, true},
		{"planning only", func(d *RunDetail) { d.Run.WorkflowPurpose = PurposeResearchStarter }, false},
		{"no persisted start", func(d *RunDetail) { d.Events = nil }, false},
		{"other run event", func(d *RunDetail) { d.Events[0].WorkflowRunID = "other" }, false},
		{"tampered snapshot", func(d *RunDetail) { d.Run.Inputs = raw(`{}`) }, false},
		{"unbound task", func(d *RunDetail) { d.Run.ResearchTaskID = "" }, false},
		{"mismatched start", func(d *RunDetail) { d.Events[0].Payload = raw(`{"inputsSha256":"wrong"}`) }, false},
		{"no file port", func(d *RunDetail) { d.Run.Compilation.Inputs[0].FileKind = "" }, false},
		{"mutable path", func(d *RunDetail) {
			d.Run.Inputs = raw(`{"input_paths":["raw.csv"]}`)
			d.Run.InputsSHA256 = hashJSON(d.Run.Inputs)
			d.Events[0].Payload = rawObject(map[string]any{"inputsSha256": d.Run.InputsSHA256})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := boundDataFixture()
			tc.change(&d)
			before := string(mustJSON(d))
			record := currentResearchDataBinding(d)
			if (record != nil) != tc.bound {
				t.Fatalf("record=%+v", record)
			}
			if record != nil && (record.Status != "bound" || record.Files[0].Name != "old.csv" || record.Files[0].SHA256 != strings.Repeat("a", 64)) {
				t.Fatal(record)
			}
			if string(mustJSON(d)) != before {
				t.Fatal("mutated original")
			}
		})
	}
}

func TestBindingTracksOnlyConfirmedDataVersionAndSurvivesRoundTrip(t *testing.T) {
	d := boundDataFixture()
	original := currentResearchDataBinding(d)
	sha := strings.Repeat("b", 64)
	file := RevisionDataFile{InputFile: InputFile{RelativePath: "research-inputs/updated-" + sha + ".csv", SHA256: sha}}
	p := ResearchRevisionProposal{ID: "pending", DataVersion: &ResearchDataVersion{Files: []RevisionDataFile{file}}}
	d.RevisionProposals = []ResearchRevisionProposal{p}
	if !rawJSONEqual(mustJSON(currentResearchDataBinding(d)), mustJSON(original)) {
		t.Fatal("pending upload adopted")
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": p})})
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": ResearchRevisionProposal{ID: "wording", NodeID: "report_drafting"}})})
	var reopened RunDetail
	_ = json.Unmarshal(mustJSON(d), &reopened)
	updated := currentResearchDataBinding(reopened)
	if updated == nil || updated.Source != "confirmed_data_revision" || len(updated.Files) != 1 || updated.Files[0].SHA256 != sha || updated.EffectiveInputsSHA256 == original.EffectiveInputsSHA256 {
		t.Fatal(updated)
	}
	if !strings.Contains(string(reopened.Run.Inputs), original.Files[0].SHA256) {
		t.Fatal("original inputs overwritten")
	}
}

func TestBoundDataReachesEveryAIStageAndLegacyPromptWithoutRewritingHistory(t *testing.T) {
	d := boundDataFixture()
	d.Run.Inputs = rawObject(map[string]any{"input_paths": exactWorkflowInputPaths(d.Run.Inputs), "research_goal": "该表格仍需在正式执行前由用户确认绑定；本次仅准备候选研究路线，不启动计算。"})
	d.Run.InputsSHA256 = hashJSON(d.Run.Inputs)
	d.Events[0].Payload = rawObject(map[string]any{"inputsSha256": d.Run.InputsSHA256})
	before := string(mustJSON(d))
	for _, id := range []string{"question_refinement", "method_selection", "method_implementation", "result_interpretation", "report_drafting", "independent_review"} {
		node := CompiledNode{ID: id, Kind: NodeAgentStage, Arguments: raw(`{"dataBinding":{"status":"forged"},"context":"仅规划，未绑定"}`), OutputSchema: raw(`{"type":"object"}`), PromptVersion: dynamicImplementationPromptVersion}
		input, err := bindNodeInput(d, node)
		if err != nil {
			t.Fatal(id, err)
		}
		values := decodeObject(input)
		if !rawJSONEqual(mustJSON(values["dataBinding"]), mustJSON(currentResearchDataBinding(d))) {
			t.Fatal(id, string(input))
		}
		prompt := buildAIStagePrompt(d, Step{Input: input}, node)
		if !strings.Contains(prompt, researchDataBindingInstruction) || !strings.Contains(prompt, "仅规划，未绑定") || strings.Contains(prompt, "<current_data_binding>") {
			t.Fatal("lost current binding or duplicated it", id)
		}
		legacy := raw(`{"context":"仅规划，未绑定"}`)
		prompt = buildAIStagePrompt(d, Step{Input: legacy}, node)
		if !strings.Contains(prompt, "<current_data_binding>") || !strings.Contains(prompt, "\n"+string(legacy)+"\n</stage_input>") {
			t.Fatal("legacy input rewritten or missing authority")
		}
	}
	if string(mustJSON(d)) != before {
		t.Fatal("mutated history")
	}
	value, requirement, err := reviewBasisAt(decodeObject(raw(`{"dataBinding":{"status":"bound"}}`)), "/dataBinding/status")
	if err != nil || !requirement || value != "bound" {
		t.Fatal("binding classified as scientific evidence", err)
	}
	if _, _, err := reviewBasisAt(decodeObject(raw(`{}`)), "/dataBinding/status"); err == nil {
		t.Fatal("invented legacy pointer allowed")
	}
}
