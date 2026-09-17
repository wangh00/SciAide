package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func reviewedDeliveryFixture(t *testing.T) RunDetail {
	t.Helper()
	input, review := acceptanceFixture(t)
	full := rawObject(review)
	core, err := reviewCoreForGate(full)
	if err != nil {
		t.Fatal(err)
	}
	var stage map[string]json.RawMessage
	_ = json.Unmarshal(input, &stage)
	gateInput := rawObject(map[string]any{"subject": input, "review": core})
	approval := rawObject(map[string]any{"approved": true, "reviewedInputSha256": hashJSON(input)})
	detail := reviewRevisionFixture()
	detail.Run.Status = RunCompleted
	detail.Run.Compilation.Nodes[1].OutputSchema = researchAcceptanceReviewSchema()
	detail.Run.Compilation.Outputs = []Output{{Name: "research_design", FromNode: "design", FromPort: "analysis", Required: true}, {Name: "independent_review", FromNode: "review", FromPort: "analysis", Required: true}, {Name: "delivery_gate", FromNode: "review_gate", FromPort: "structured", Required: true}}
	detail.Steps[0].Output = rawObject(map[string]any{"analysis": stage["context"]})
	detail.Steps[1].Input, detail.Steps[1].InputSHA256, detail.Steps[1].Output = input, hashJSON(input), rawObject(map[string]any{"analysis": full})
	detail.Steps[2].Status = StepCompleted
	detail.Steps[2].Input, detail.Steps[2].InputSHA256, detail.Steps[2].Output = gateInput, hashJSON(gateInput), rawObject(map[string]any{"structured": approval})
	detail.Run.Outputs = rawObject(map[string]any{"research_design": stage["context"], "independent_review": full, "delivery_gate": approval})
	detail.AIExecutions = []AIExecution{{WorkflowStepID: detail.Steps[1].ID, Attempt: 1, Status: "completed", InputSHA256: hashJSON(input), Output: full, OutputSHA256: hashJSON(full)}}
	return detail
}

func TestReviewedDeliveryChecksEveryCurrentDependency(t *testing.T) {
	for _, port := range []string{"researchContract", "computedResults", "implementationContext", "evidenceContext", "dataPreflight", "sourceArtifacts"} {
		t.Run(port, func(t *testing.T) {
			d := reviewedDeliveryFixture(t)
			var input map[string]json.RawMessage
			_ = json.Unmarshal(d.Steps[1].Input, &input)
			value := input[port]
			if len(value) == 0 {
				value = raw(`{"snapshot":"original"}`)
				input[port] = value
			}
			d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "dependency", FromPort: "analysis", ToNode: "review", ToPort: port})
			d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "dependency", Kind: NodeAIAnalysis})
			d.Steps = append(d.Steps, Step{ID: "dependency-step", NodeID: "dependency", Status: StepCompleted, Output: rawObject(map[string]any{"analysis": value})})
			encoded, _ := json.Marshal(input)
			refreshDeliveryReview(t, &d, encoded)
			if err := ValidateReviewedOutput(d, "research_design"); err != nil {
				t.Fatalf("matching dependency: %v", err)
			}
			for _, state := range []string{"changed", "incomplete", "missing port"} {
				t.Run(state, func(t *testing.T) {
					changed := d
					changed.Steps = append([]Step(nil), d.Steps...)
					dependency := &changed.Steps[len(changed.Steps)-1]
					switch state {
					case "changed":
						dependency.Output = raw(`{"analysis":{"snapshot":"new"}}`)
					case "incomplete":
						dependency.Status = StepFailed
					case "missing port":
						dependency.Output = raw(`{}`)
					}
					if err := ValidateReviewedOutput(changed, "research_design"); err == nil || !strings.Contains(err.Error(), port) {
						t.Fatalf("delivery should identify stale %s: %v", port, err)
					}
					if _, err := bindNodeInput(changed, changed.Run.Compilation.Nodes[2]); err == nil || !strings.Contains(err.Error(), port) {
						t.Fatalf("gate should reject before execution: %v", err)
					}
				})
			}
		})
	}
}

func refreshDeliveryReview(t *testing.T, d *RunDetail, input json.RawMessage) {
	t.Helper()
	full, err := outputPort(d.Steps[1].Output, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	var review map[string]json.RawMessage
	_ = json.Unmarshal(full, &review)
	review["reviewedInputSha256"], _ = json.Marshal(hashJSON(input))
	full, _ = json.Marshal(review)
	core, err := reviewCoreForGate(full)
	if err != nil {
		t.Fatal(err)
	}
	d.Steps[1].Input, d.Steps[1].InputSHA256 = input, hashJSON(input)
	d.Steps[1].Output = rawObject(map[string]any{"analysis": full})
	d.AIExecutions[0].InputSHA256 = hashJSON(input)
	d.AIExecutions[0].Output, d.AIExecutions[0].OutputSHA256 = full, hashJSON(full)
	d.Steps[2].Input = rawObject(map[string]any{"subject": input, "review": core})
	d.Steps[2].InputSHA256 = hashJSON(d.Steps[2].Input)
	approval := rawObject(map[string]any{"approved": true, "reviewedInputSha256": hashJSON(input)})
	d.Steps[2].Output = rawObject(map[string]any{"structured": approval})
	var outputs map[string]json.RawMessage
	_ = json.Unmarshal(d.Run.Outputs, &outputs)
	outputs["independent_review"], outputs["delivery_gate"] = full, approval
	d.Run.Outputs, _ = json.Marshal(outputs)
}

func TestReviewDependencyUsesFrozenRunInputs(t *testing.T) {
	d := reviewedDeliveryFixture(t)
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "$input", FromPort: "contract", ToNode: "review", ToPort: "researchContract"})
	var input map[string]json.RawMessage
	_ = json.Unmarshal(d.Steps[1].Input, &input)
	d.Run.Inputs = rawObject(map[string]any{"contract": input["researchContract"]})
	if err := ValidateReviewedOutput(d, "research_design"); err != nil {
		t.Fatal(err)
	}
	d.Run.Inputs = raw(`{"contract":{"successCriteria":["changed"]}}`)
	if err := ValidateReviewedOutput(d, "research_design"); err == nil {
		t.Fatal("changed run input accepted")
	}
}

func TestDeliveryRejectsCodeMismatchEvenWithMatchingReviewSnapshots(t *testing.T) {
	d := reviewedDeliveryFixture(t)
	var input map[string]json.RawMessage
	_ = json.Unmarshal(d.Steps[1].Input, &input)
	input["implementationContext"] = raw(`{"code":"result = {'rows': 3}"}`)
	input["computedResults"] = rawObject(map[string]any{"status": "success", "codeSha256": hashBytes([]byte("result = {'rows': 2}"))})
	for _, port := range []string{"implementationContext", "computedResults"} {
		d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: port, FromPort: "analysis", ToNode: "review", ToPort: port})
		d.Steps = append(d.Steps, Step{ID: port, NodeID: port, Status: StepCompleted, Output: rawObject(map[string]any{"analysis": input[port]})})
	}
	encoded, _ := json.Marshal(input)
	refreshDeliveryReview(t, &d, encoded)
	if err := ValidateReviewedOutput(d, "research_design"); err == nil || !strings.Contains(err.Error(), "实际方法代码") {
		t.Fatalf("review approval must not override mismatched execution: %v", err)
	}
}

func TestReviewedDeliveryRejectsStaleOrUnrelatedApproval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*RunDetail)
		valid  bool
	}{
		{"complete", func(*RunDetail) {}, true},
		{"not completed", func(d *RunDetail) { d.Run.Status = RunFailed }, false},
		{"stale output", func(d *RunDetail) {
			var o map[string]any
			_ = json.Unmarshal(d.Run.Outputs, &o)
			o["research_design"] = map[string]any{"title": "changed"}
			d.Run.Outputs = rawObject(o)
		}, false},
		{"review attempt changed", func(d *RunDetail) { d.Steps[1].Attempt = 2 }, false},
		{"review hash drift", func(d *RunDetail) { d.AIExecutions[0].Output = raw(`{"approved":true}`) }, false},
		{"gate subject changed", func(d *RunDetail) {
			d.Steps[2].Input = raw(`{"subject":{},"review":{}}`)
			d.Steps[2].InputSHA256 = hashJSON(d.Steps[2].Input)
		}, false},
		{"unreviewed producer", func(d *RunDetail) {
			for i := range d.Run.Compilation.Edges {
				if d.Run.Compilation.Edges[i].ToNode == "review" {
					d.Run.Compilation.Edges[i].FromNode = "other"
				}
			}
		}, false},
		{"missing AI record", func(d *RunDetail) { d.AIExecutions = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := reviewedDeliveryFixture(t)
			tc.mutate(&d)
			err := ValidateReviewedOutput(d, "research_design")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestResearchDeliveryDoesNotUpgradeDesignToEmpiricalResult(t *testing.T) {
	d := reviewedDeliveryFixture(t)
	value := assessResearchDelivery(d)
	if value == nil || value.Status != "reviewed" || value.Kind != "research_design" || len(value.Checks) != 2 {
		t.Fatalf("assessment=%+v", value)
	}
	d.AIExecutions = nil
	value = assessResearchDelivery(d)
	if value == nil || value.Status != "unverified" {
		t.Fatalf("missing review was marked ready: %+v", value)
	}
}

func TestLegacyGateSubjectCannotDropNewResearchContext(t *testing.T) {
	subject := raw(`{"title":"design"}`)
	if !reviewSubjectMatchesInput(subject, rawObject(map[string]any{"context": subject})) {
		t.Fatal("historical context-only envelope rejected")
	}
	if reviewSubjectMatchesInput(subject, rawObject(map[string]any{"context": subject, "researchContract": map[string]any{"successCriteria": []string{"verify methods"}}})) {
		t.Fatal("gate discarded unreviewed research agreement")
	}
}

func TestCompletedDeliveryCannotContainFailedPythonResults(t *testing.T) {
	d := reviewedDeliveryFixture(t)
	d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "python", Kind: NodePython})
	d.Run.Compilation.Outputs = append(d.Run.Compilation.Outputs, Output{Name: "analysis", FromNode: "python", FromPort: "structured", Required: true})
	failed := raw(`{"status":"failed"}`)
	d.Steps = append(d.Steps, Step{ID: "python-step", NodeID: "python", Status: StepCompleted, Output: rawObject(map[string]any{"structured": failed})})
	var outputs map[string]json.RawMessage
	_ = json.Unmarshal(d.Run.Outputs, &outputs)
	outputs["analysis"] = failed
	d.Run.Outputs = rawObject(map[string]any{"research_design": outputs["research_design"], "independent_review": outputs["independent_review"], "delivery_gate": outputs["delivery_gate"], "analysis": failed})
	if err := ValidateReviewedOutput(d, "research_design"); err == nil {
		t.Fatal("failed Python was accepted as part of a completed delivery")
	}
}
