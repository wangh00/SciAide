package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

func TestRepairControlSchemasNeverMixFullAndPatch(t *testing.T) {
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
	patch := reviewRepairValidPatch(r)
	req := stageRepairRequest(model.ChatRequest{}, reviewRepairTestSchema, r.schema, r.instruction())
	v := tool.JSONSchemaValidator{}
	if len(req.Tools) != 2 || req.ForcedTool != "" {
		t.Fatal("correction choice unavailable")
	}
	for i, candidate := range []string{reviewRepairTestBase, patch} {
		if err := v.Validate(req.Tools[i].InputSchema, json.RawMessage(candidate)); err != nil {
			t.Fatal(err)
		}
		if err := v.Validate(req.Tools[1-i].InputSchema, json.RawMessage(candidate)); err == nil {
			t.Fatal("cross-shape accepted")
		}
	}
	hybrid := strings.TrimSuffix(patch, "}") + `,"approved":false,"limitations":[]}`
	for _, d := range req.Tools {
		if err := v.Validate(d.InputSchema, json.RawMessage(hybrid)); err == nil {
			t.Fatal("mixed object offered")
		}
	}
	_, formatErr := r.merge(hybrid)
	if formatErr == nil || !strings.Contains(researchSubmissionFailureMessage(formatErr), "提交协议错误") {
		t.Fatal("transport failure is still reported as scientific failure", formatErr)
	}
	for _, name := range []string{stageSubmissionTool, stageRepairTool} {
		if _, err := stageCorrectionCandidate([]model.ToolCall{{Name: name, Arguments: json.RawMessage(`{}`)}}, "", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stageCorrectionCandidate([]model.ToolCall{{Name: stageRepairTool}}, "", false); err == nil {
		t.Fatal("patch outside correction accepted")
	}
	if _, err := stageCorrectionCandidate([]model.ToolCall{{Name: stageRepairTool}, {Name: stageSubmissionTool}}, "", true); err == nil {
		t.Fatal("two submissions accepted")
	}
	if strings.Contains(req.Messages[len(req.Messages)-1].Content, "only available function is") {
		t.Fatal("conflicting sole-function guidance")
	}
}

// Opt-in read-only replay of actual rejected bytes; it never executes a model
// or accepts the scientific conclusions of either recorded candidate.
func TestCapturedMixedReviewSubmission(t *testing.T) {
	dir := os.Getenv("SCIAIDE_MIXED_REVIEW_REPLAY")
	if dir == "" {
		t.Skip("no incident capture")
	}
	data, err := os.ReadFile(filepath.Join(dir, "turns.json"))
	if err != nil {
		t.Fatal(err)
	}
	var turns []struct {
		Text  string `json:"draft_text"`
		Index int    `json:"turn_index"`
	}
	if err = json.Unmarshal(data, &turns); err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(dir, "normalized-base.json"))
	if err != nil {
		t.Fatal("run workflow capture replay first", err)
	}
	schema, err := os.ReadFile(filepath.Join(dir, "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	diagnosticBytes, err := os.ReadFile(filepath.Join(dir, "basis-diagnostic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic workflow.ReviewBasisErrors
	if err = json.Unmarshal(diagnosticBytes, &diagnostic); err != nil {
		t.Fatal(err)
	}
	r := newReviewRepair(string(base), schema, &diagnostic)
	if r == nil {
		t.Fatal("actual rejected base could not enter local repair")
	}
	req := stageRepairRequest(model.ChatRequest{}, workflow.ReviewSubmissionSchema(schema), r.schema, r.instruction())
	// Rebuild the previous transport union to prove the rejected hybrid was
	// actually offered by the host, not just an arbitrary invalid model object.
	var legacy, patchShape map[string]any
	_ = json.Unmarshal(req.Tools[0].InputSchema, &legacy)
	_ = json.Unmarshal(r.schema, &patchShape)
	for key, value := range patchShape["properties"].(map[string]any) {
		legacy["properties"].(map[string]any)[key] = value
	}
	delete(legacy, "required")
	legacySchema, _ := json.Marshal(legacy)
	count := 0
	for _, turn := range turns {
		if turn.Index < 5 {
			continue
		}
		var candidate map[string]json.RawMessage
		if json.Unmarshal([]byte(turn.Text), &candidate) != nil || candidate["limitations"] == nil || candidate["reviewFindingsPatch"] == nil {
			t.Fatal("incident not reproduced")
		}
		if _, err := r.merge(turn.Text); err == nil {
			t.Fatal("hybrid silently accepted")
		}
		if err := (tool.JSONSchemaValidator{}).Validate(legacySchema, json.RawMessage(turn.Text)); err != nil {
			t.Fatal("old host schema did not offer recorded hybrid", err)
		}
		for _, d := range req.Tools {
			if err := (tool.JSONSchemaValidator{}).Validate(d.InputSchema, json.RawMessage(turn.Text)); err == nil {
				t.Fatal("new interface offers captured mixed shape")
			}
		}
		// Strip complete-result fields only in a derived test specimen. The
		// production host still rejects hybrids; it never chooses a branch for AI.
		patch, _ := json.Marshal(map[string]json.RawMessage{"baseSHA256": candidate["baseSHA256"], "reviewFindingsPatch": candidate["reviewFindingsPatch"]})
		if _, err := r.merge(string(patch)); err != nil {
			t.Fatal("actual hash/patch not reproducible", err)
		}
		count++
	}
	if count != 2 {
		t.Fatal(count)
	}
}
