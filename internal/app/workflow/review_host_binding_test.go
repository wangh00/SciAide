package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Unit fixtures must model the durable execution that production commits,
// rather than treating a completed boolean as an authentic review receipt.
func freezeReviewExecutionFixture(t *testing.T, detail *RunDetail, index int) {
	t.Helper()
	s := &detail.Steps[index]
	s.InputSHA256 = hashJSON(s.Input)
	full, err := outputPort(s.Output, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	detail.AIExecutions = append(detail.AIExecutions, AIExecution{WorkflowStepID: s.ID, Attempt: s.Attempt, Status: "completed", InputSHA256: s.InputSHA256, Output: full, OutputSHA256: hashJSON(full)})
}

func TestHostReviewBindingOmitsMechanicalHashButKeepsScience(t *testing.T) {
	input, wire, schema := issueTransportFixture(t)
	v := decodeObject(wire)
	delete(v, "reviewedInputSha256")
	without := mustJSON(v)
	if err := (tool.JSONSchemaValidator{}).Validate(ReviewSubmissionSchema(schema), without); err != nil {
		t.Fatal(err)
	}
	node := CompiledNode{ID: "custom_domain_review", OutputSchema: schema}
	value, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(without), node, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 || changes[0].Rule != "bind_current_frozen_review_input" || decodeObject(value)["reviewedInputSha256"] != hashJSON(input) || decodeObject(value)["approved"] != false {
		t.Fatal("binding lost identity or decision")
	}
	if err := validateWorkflowAIStageOutput(node, value, input); err != nil {
		t.Fatal(err)
	}
	// Already bound submissions remain idempotent, including rejects.
	again, c, err := normalizeWorkflowAIStageSubmissionForInput(string(value), node, input)
	if err != nil || len(c) != 0 || !rawJSONEqual(value, again) {
		t.Fatal("non-idempotent binding", err)
	}
	v["reviewedInputSha256"] = strings.Repeat("0", 64)
	wrong, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(v)), node, input)
	if err == nil && validateWorkflowAIStageOutput(node, wrong, input) == nil {
		t.Fatal("explicit wrong hash repaired")
	}
	delete(v, "reviewedInputSha256")
	v["approved"] = true
	wrong, _, err = normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(v)), node, input)
	if err == nil && validateWorkflowAIStageOutput(node, wrong, input) == nil {
		t.Fatal("false approval accepted")
	}
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(without), node, json.RawMessage(`broken`)); err == nil {
		t.Fatal("missing frozen input accepted")
	}
}

func TestCurrentReviewVerifierSharedAcrossGateAndDelivery(t *testing.T) {
	for _, corruption := range []string{"execution", "input", "upstream", "schema"} {
		t.Run(corruption, func(t *testing.T) {
			d := reviewedDeliveryFixture(t)
			switch corruption {
			case "execution":
				d.AIExecutions = nil
			case "input":
				d.Steps[1].InputSHA256 = "wrong"
			case "upstream":
				d.Steps[0].Output = raw(`{"analysis":{"changed":true}}`)
			case "schema":
				d.Steps[1].Output = raw(`{"analysis":{"approved":true}}`)
				freezeReviewExecutionFixture(t, &d, 1)
			}
			if _, err := bindNodeInput(d, d.Run.Compilation.Nodes[2]); err == nil {
				t.Fatal("gate accepted invalid current review")
			}
			if ValidateReviewedOutput(d, "research_design") == nil {
				t.Fatal("delivery disagreed with gate")
			}
		})
	}
}
