package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func implementationDecisionFixture(legacy bool) RunDetail {
	analysis := raw(`{"methodSummary":"paired t < bootstrap","analysisInput":{"alpha":0.05},"limitations":["待研究者确认"],"researchChanges":[{"before":"unspecified","after":"percentile bootstrap","reason":"uncertainty","impact":"CI method"}],"code":"result = {'mean': 3}"}`)
	context := map[string]any{"outputSha256": hashJSON(analysis)}
	if !legacy {
		context["analysisContentSha256"] = implementationContentSHA256(analysis)
	}
	return RunDetail{
		Run:            Run{ID: "run", Inputs: raw(`{}`), Compilation: Compilation{Nodes: []CompiledNode{{ID: "method_implementation", PromptVersion: dynamicImplementationPromptVersion}}}},
		Steps:          []Step{{ID: "impl", WorkflowRunID: "run", NodeID: "method_implementation", Attempt: 1, Status: StepCompleted, Input: raw(`{}`), InputSHA256: hashJSON(raw(`{}`)), Output: rawObject(map[string]any{"analysis": analysis})}},
		HumanDecisions: []HumanDecision{{ID: "decision", WorkflowRunID: "run", WorkflowStepID: "impl", Kind: "node_confirmation", Attempt: 1, Approved: true, Context: rawObject(context), CreatedAt: time.Date(2026, 10, 10, 7, 46, 31, 0, time.UTC)}},
		AIExecutions:   []AIExecution{{ID: "execution", WorkflowRunID: "run", WorkflowStepID: "impl", Attempt: 1, Status: "completed", Output: analysis, OutputSHA256: hashJSON(analysis)}},
	}
}

func TestImplementationDecisionIsVersionBoundAndNotApprovalByCompletion(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			mutate func(*RunDetail)
			status string
		}{
			{"matching", func(*RunDetail) {}, "approved"},
			{"other run", func(d *RunDetail) { d.HumanDecisions[0].WorkflowRunID = "other" }, "unconfirmed"},
			{"other step", func(d *RunDetail) { d.HumanDecisions[0].WorkflowStepID = "other" }, "unconfirmed"},
			{"prior attempt", func(d *RunDetail) { d.Steps[0].Attempt++ }, "unconfirmed"},
			{"rejected", func(d *RunDetail) { d.HumanDecisions[0].Approved = false }, "unconfirmed"},
			{"no decision", func(d *RunDetail) { d.HumanDecisions = nil }, "unconfirmed"},
			{"text claim only", func(d *RunDetail) {
				d.HumanDecisions = nil
				d.Events = []RuntimeEvent{{Type: "workflow.human_decided", Payload: raw(`{"approved":true,"stepId":"impl"}`)}}
			}, "unconfirmed"},
			{"changed code", func(d *RunDetail) {
				a, _ := outputPort(d.Steps[0].Output, "analysis")
				o := decodeObject(a)
				o["code"] = "result = {'mean': 4}"
				d.Steps[0].Output = rawObject(map[string]any{"analysis": o})
			}, "unconfirmed"},
			{"changed parameters", func(d *RunDetail) {
				a, _ := outputPort(d.Steps[0].Output, "analysis")
				o := decodeObject(a)
				o["analysisInput"] = map[string]any{"alpha": 0.1}
				d.Steps[0].Output = rawObject(map[string]any{"analysis": o})
			}, "unconfirmed"},
			{"cleared on reset", func(d *RunDetail) { d.Steps[0].Status = StepQueued; d.Steps[0].Output = raw(`{}`) }, ""},
			{"different protocol", func(d *RunDetail) { d.Run.Compilation.Nodes[0].PromptVersion = "custom" }, ""},
		} {
			t.Run(tc.name+map[bool]string{true: "/legacy", false: "/new"}[legacy], func(t *testing.T) {
				d := implementationDecisionFixture(legacy)
				tc.mutate(&d)
				p := currentImplementationDecision(d)
				if tc.status == "" {
					if p != nil {
						t.Fatal(p)
					}
					return
				}
				if p == nil || p.Status != tc.status {
					t.Fatalf("got %+v want %s", p, tc.status)
				}
			})
		}
	}
	d := implementationDecisionFixture(false)
	d.HumanDecisions = nil
	d.Steps[0].Output = raw(`{"analysis":{"researchChanges":[]}}`)
	if p := currentImplementationDecision(d); p == nil || p.Status != "not_required" || p.DecisionID != "" {
		t.Fatal(p)
	}
}

func TestImplementationDecisionBindsAllMethodConsumersWithoutEditingMethod(t *testing.T) {
	for _, id := range []string{"result_interpretation", "report_drafting", "independent_review"} {
		t.Run(id, func(t *testing.T) {
			d := implementationDecisionFixture(true)
			target := CompiledNode{ID: id, Kind: NodeAgentStage, Arguments: raw(`{}`)}
			port := "implementationContext"
			if id == "result_interpretation" {
				port = "methodContext"
			}
			d.Run.Compilation.Edges = []Edge{{FromNode: "method_implementation", FromPort: "analysis", ToNode: id, ToPort: port}}
			before := string(d.Steps[0].Output)
			input, err := bindNodeInput(d, target)
			if err != nil {
				t.Fatal(err)
			}
			var i struct {
				Decision *implementationDecision `json:"implementationDecision"`
			}
			_ = json.Unmarshal(input, &i)
			if i.Decision == nil || i.Decision.Status != "approved" {
				t.Fatal(string(input))
			}
			if string(d.Steps[0].Output) != before || !strings.Contains(string(input), "待研究者确认") {
				t.Fatal("mutated original method")
			}
			prompt := buildAIStagePrompt(d, Step{Input: input}, target)
			for _, s := range []string{"version-bound execution authorization", "NOT scientific evidence", "Do not ask the researcher to confirm", "Recompute only if"} {
				if !strings.Contains(prompt, s) {
					t.Fatal("missing instruction", s)
				}
			}
		})
	}
	d := implementationDecisionFixture(false)
	n := CompiledNode{ID: "unrelated", Kind: NodeAgentStage, Arguments: raw(`{"implementationDecision":{"status":"approved"}}`)}
	input, err := bindNodeInput(d, n)
	if err != nil || strings.Contains(string(input), "implementationDecision") {
		t.Fatal("unbound authorization accepted", string(input), err)
	}
}

func TestImplementationAuthorizationCannotServeAsScientificEvidence(t *testing.T) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	stage["implementationDecision"] = map[string]any{"status": "approved"}
	f := &output["reviewFindings"].([]trackedReviewFinding)[0]
	f.Basis = []reviewBasis{{"/implementationDecision/status", "approved"}}
	if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err == nil || !strings.Contains(err.Error(), "仅引用了验收/返修要求") {
		t.Fatal(err)
	}
	// A real numeric defect still rejects an authorized implementation.
	f.Basis = append(f.Basis, reviewBasis{"/computedResults/mean", "3"})
	if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err != nil {
		t.Fatal(err)
	}
	if output["approved"] != false {
		t.Fatal("approval bypassed science")
	}
	// An unsound prior objection can be withdrawn on CURRENT state and prose;
	// do not rename it or silently remove it from the historical issue list.
	input, output = requirementReviewFixture()
	stage = decodeObject(input)
	stage["implementationDecision"] = map[string]any{"status": "approved"}
	f = &output["reviewFindings"].([]trackedReviewFinding)[0]
	f.Status = "withdrawn"
	f.ChangeReason = "原未确认意见与实际执行授权不符；当前稿件已准确说明方法"
	f.Basis = []reviewBasis{{"/implementationDecision/status", "approved"}, {"/context/markdown", "Result: 5."}}
	if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err != nil {
		t.Fatal(err)
	}
}

func TestImplementationDecisionPromptVersionDoesNotMutateFrozenNode(t *testing.T) {
	n := CompiledNode{ID: "result_interpretation", PromptVersion: "dynamic-interpret-v3"}
	if got := reportExecutionPromptVersion(n, implementationDecisionInstruction); got != n.PromptVersion+"-"+implementationDecisionVersion {
		t.Fatal(got)
	}
	if n.PromptVersion != "dynamic-interpret-v3" {
		t.Fatal("mutated frozen node")
	}
	n.PromptVersion += "-" + implementationDecisionVersion
	if got := reportExecutionPromptVersion(n, implementationDecisionInstruction); got != n.PromptVersion {
		t.Fatal("duplicate state version", got)
	}
}

func TestImplementationQualityGuidanceUsesResearchContractNotCaseFields(t *testing.T) {
	n := CompiledNode{ID: "method_implementation", PromptVersion: dynamicImplementationPromptVersion, OutputSchema: dynamicImplementationSchema()}
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, n)
	for _, s := range []string{"actual field parsing/coercion types", "when required by the frozen research contract", "not a new requirement for every research direction"} {
		if !strings.Contains(prompt, s) {
			t.Fatal(s)
		}
	}
	if strings.Contains(prompt, "pre_score") || strings.Contains(prompt, "student_id") {
		t.Fatal("case hard coded")
	}
}

func TestImplementationDecisionAvailableInReadOnlyOverview(t *testing.T) {
	s, f := stageMemoryService()
	d := implementationDecisionFixture(true)
	f.detail.HumanDecisions, f.detail.AIExecutions = d.HumanDecisions, d.AIExecutions
	f.detail.Steps = append(f.detail.Steps, d.Steps...)
	f.detail.Run.Compilation.Nodes = append(f.detail.Run.Compilation.Nodes, d.Run.Compilation.Nodes...)
	value, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(value, &page)
	var overview struct {
		Decision implementationDecision `json:"implementationDecision"`
	}
	_ = json.Unmarshal([]byte(page.Content), &overview)
	if overview.Decision.Status != "approved" {
		t.Fatal(page.Content)
	}
}

// Optional captured live-state replay. No formal database writes or requests.
func TestCapturedImplementationDecisionReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_IMPLEMENTATION_DECISION_REPLAY")
	if dir == "" {
		t.Skip("no task-scoped capture supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "detail.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Run          Run             `json:"run"`
		Steps        []Step          `json:"steps"`
		AIExecutions []AIExecution   `json:"aiExecutions"`
		Decisions    []HumanDecision `json:"humanDecisions"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	d := RunDetail{Run: capture.Run, Steps: capture.Steps, AIExecutions: capture.AIExecutions, HumanDecisions: capture.Decisions}
	p := currentImplementationDecision(d)
	if p == nil || p.Status != "approved" {
		t.Fatal("actual legacy decision not recovered", p)
	}
	for _, id := range []string{"result_interpretation", "report_drafting", "independent_review"} {
		n := compilationNodeMap(d.Run.Compilation)[id]
		input, err := bindNodeInput(d, n)
		if err != nil {
			t.Fatal(id, err)
		}
		var stage struct {
			Decision *implementationDecision `json:"implementationDecision"`
		}
		_ = json.Unmarshal(input, &stage)
		if stage.Decision == nil || stage.Decision.Status != "approved" {
			t.Fatal(id, "missing approved state")
		}
		prompt := buildAIStagePrompt(d, Step{Input: input, InputSHA256: hashJSON(input)}, n)
		if !strings.Contains(prompt, implementationDecisionInstruction) {
			t.Fatal(id, "missing guidance")
		}
		t.Logf("%s: approved decision=%s, method attempt=%d, prompt bytes=%d", id, stage.Decision.DecisionID, stage.Decision.Attempt, len(prompt))
	}
	t.Log("Only state transport replayed: no model judgment generated, original review remains unchanged.")
}
