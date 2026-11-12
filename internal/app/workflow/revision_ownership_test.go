package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// A model can cite the report copy without owning the material it copied.
func materialRevisionFixture(t *testing.T) RunDetail {
	t.Helper()
	d := recommendationFixture(t)
	design := raw(`{"status":"research_design_not_empirical_result","deliverables":[{"title":"Material A","markdown":"Original material, not yet drafted."}]}`)
	d.Steps[1].Output = rawObject(map[string]any{"analysis": design})
	d.Run.Compilation.Nodes[3].PromptVersion = researchReportVersion
	d.Run.Compilation.Nodes[3].OutputSchema = dynamicReportSchema()
	d.Steps[3].Input = rawObject(map[string]any{"context": design})
	d.Steps[3].InputSHA256 = hashJSON(d.Steps[3].Input)
	report, _, err := enrichDesignMaterials(raw(`{"markdown":"# Report\nA tentative design."}`), d.Steps[3].Input)
	if err != nil {
		t.Fatal(err)
	}
	d.Steps[3].Output = rawObject(map[string]any{"analysis": report})
	for i := range d.Run.Compilation.Edges {
		e := &d.Run.Compilation.Edges[i]
		if e.ToNode == "report_drafting" {
			e.FromNode = "method_implementation"
		}
	}
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "method_implementation", FromPort: "analysis", ToNode: "independent_review", ToPort: "designContext"})
	input := rawObject(map[string]any{"context": report, "designContext": design, "revisionTargets": revisionPlanTargetsForReview(d, "independent_review")})
	d.Steps[4].Input, d.Steps[4].InputSHA256 = input, hashJSON(input)
	value := decodeObject(d.AIExecutions[0].Output)
	value["reviewedInputSha256"] = hashJSON(input)
	value["requiredCorrections"] = []string{"Replace the frozen material with the actual draft"}
	value["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "report_drafting", "reason": "Material A is in the report", "whyNotLater": "The gate cannot write it", "needsUserInput": false, "requiredInput": ""}}
	freezeRecommendationReview(t, &d, value)
	return d
}

func bindMaterialOwnershipFixture(t *testing.T, d *RunDetail) *revisionWriteContract {
	t.Helper()
	contract, err := currentRevisionWriteContract(*d, d.Steps[4].NodeID, d.Steps[4].Input, revisionPlanTargetsForReview(*d, d.Steps[4].NodeID))
	if err != nil || contract == nil {
		t.Fatal("ownership", err)
	}
	fields := decodeObject(d.Steps[4].Input)
	fields["revisionWriteContract"] = contract
	d.Steps[4].Input = mustJSON(fields)
	d.Steps[4].InputSHA256 = hashJSON(d.Steps[4].Input)
	return contract
}

func writeObjectID(t *testing.T, c *revisionWriteContract, kind string) string {
	t.Helper()
	for _, object := range c.Objects {
		if object.Kind == kind {
			return object.ID
		}
	}
	t.Fatal("missing write object", kind)
	return ""
}

func TestRevisionWriteObjectsSeparateEvidenceFromEdits(t *testing.T) {
	d := materialRevisionFixture(t)
	c := bindMaterialOwnershipFixture(t, &d)
	material, body := writeObjectID(t, c, "frozen_design_material"), writeObjectID(t, c, "report_authored_body")
	for _, tt := range []struct {
		name, node string
		edits      []string
		needs      bool
		required   string
		bad        bool
	}{
		{"report cannot replace material", "report_drafting", []string{material}, false, "", true},
		{"real material producer", "method_implementation", []string{material}, false, "", false},
		{"report cites correct design but only edits body", "report_drafting", []string{body}, false, "", false},
		{"combined material and authored text", "method_implementation", []string{body, material}, false, "", false},
		{"unnecessary method rewind", "method_selection", []string{material}, false, "", true},
		{"missing declaration", "report_drafting", nil, false, "", true},
		{"unknown object", "report_drafting", []string{"edit-stale"}, false, "", true},
		{"duplicate object", "method_implementation", []string{material, material}, false, "", true},
		{"genuine inaccessible user material", "", nil, true, "Supply the authorized text", false},
		{"user question does not give report material ownership", "report_drafting", []string{material}, true, "Supply text", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := mustJSON(map[string]any{"requiredCorrections": []string{"Current actionable defect"}, "revisionPlan": []researchRevisionPlanItem{{CorrectionIndex: 0, NodeID: tt.node, Reason: "Current evidence", WhyNotLater: "Responsible editable result", NeedsUserInput: tt.needs, RequiredInput: tt.required, EditTargets: tt.edits}}})
			if err := validateResearchRevisionPlan(output, d.Steps[4].Input); (err != nil) != tt.bad {
				t.Fatal(err)
			}
		})
	}
	for _, object := range c.Objects {
		if object.ID != material {
			continue
		}
		if object.OwnerNodeID != "method_implementation" || object.StepID != d.Steps[1].ID || object.OutputSHA256 != hashJSON(d.Steps[1].Output) || object.ReportCopyPolicy != "legacy_verbatim_material" || len(object.ReviewRoots) != 1 {
			t.Fatalf("bad graph ownership: %+v", object)
		}
	}
}

func TestRevisionWriteRecommendationAndDependencies(t *testing.T) {
	d := materialRevisionFixture(t)
	c := bindMaterialOwnershipFixture(t, &d)
	value := decodeObject(d.AIExecutions[0].Output)
	value["reviewedInputSha256"] = d.Steps[4].InputSHA256
	plan := value["revisionPlan"].([]any)[0].(map[string]any)
	plan["nodeId"], plan["editTargets"] = "method_implementation", []string{writeObjectID(t, c, "frozen_design_material")}
	freezeRecommendationReview(t, &d, value)
	r := researchRevisionRecommendation(d)
	if r == nil || r.Status != "recommended" || r.NodeID != "method_implementation" || len(r.AffectedStages) != 5 {
		t.Fatalf("recommendation=%+v", r)
	}
	if _, err := resolveReviewRevisionRequest(d, d.Steps[5], RetryCommand{UseRecommendation: true, ExpectedReviewSHA256: r.ReviewOutputSHA256}); err != nil {
		t.Fatal(err)
	}
	// Same object content in a different attempt is a different write identity.
	d.Steps[1].Attempt++
	if _, err := currentReviewOutput(d, d.Steps[4]); err == nil {
		t.Fatal("stale ownership accepted")
	}
}

func TestRevisionWriteContractBindsAtInputFreezeAndRejectsBadGraph(t *testing.T) {
	d := materialRevisionFixture(t)
	input, err := bindNodeInput(d, d.Run.Compilation.Nodes[4])
	if err != nil || decodeObject(input)["revisionWriteContract"] == nil {
		t.Fatal("missing frozen contract", err)
	}
	d.Steps[4].Input = input
	prompt := buildAIStagePrompt(d, d.Steps[4], d.Run.Compilation.Nodes[4])
	if !strings.Contains(prompt, revisionWriteInstruction) {
		t.Fatal("missing ownership instruction")
	}
	for _, name := range []string{"report hash", "wrong producer", "missing edge", "upstream mutation"} {
		t.Run(name, func(t *testing.T) {
			copy := materialRevisionFixture(t)
			switch name {
			case "report hash":
				copy.Steps[3].InputSHA256 = "bad"
			case "wrong producer":
				copy.Run.Compilation.Edges = append(copy.Run.Compilation.Edges, Edge{FromNode: "method_selection", FromPort: "analysis", ToNode: "report_drafting", ToPort: "context"})
			case "missing edge":
				for i, e := range copy.Run.Compilation.Edges {
					if e.ToNode == "report_drafting" {
						copy.Run.Compilation.Edges = append(copy.Run.Compilation.Edges[:i], copy.Run.Compilation.Edges[i+1:]...)
						break
					}
				}
			case "upstream mutation":
				copy.Steps[1].Output = raw(`{"analysis":{"different":true}}`)
			}
			if _, err := currentRevisionWriteContract(copy, copy.Steps[4].NodeID, copy.Steps[4].Input, revisionPlanTargetsForReview(copy, copy.Steps[4].NodeID)); err == nil {
				t.Fatal("bad ownership graph accepted")
			}
		})
	}
}

func TestCapturedFrozenMaterialRevisionReplay(t *testing.T) {
	path := os.Getenv("SCIAIDE_REVISION_REPLAY")
	if path == "" {
		t.Skip("optional private captured-case replay")
	}
	captured, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Workflow RunDetail `json:"workflow"`
	}
	if err := json.Unmarshal(captured, &saved); err != nil {
		t.Fatal(err)
	}
	d := saved.Workflow
	// This is a CONTENT replay, not verification of the original byte hashes:
	// the captured debug JSON pretty-printed its RawMessages. Bind a fresh
	// in-memory report input; never rewrite the saved snapshot or its database.
	d.Steps[3].InputSHA256 = hashJSON(d.Steps[3].Input)
	current, _ := outputPort(d.Steps[4].Output, "analysis")
	oldPlan, _, _, err := parseResearchRevisionPlan(current, revisionPlanTargetsForReview(d, d.Steps[4].NodeID))
	if err != nil || len(oldPlan) != 2 || oldPlan[0].NodeID != "report_drafting" {
		t.Fatal("captured plan mismatch", err)
	}
	c, err := currentRevisionWriteContract(d, d.Steps[4].NodeID, d.Steps[4].Input, revisionPlanTargetsForReview(d, d.Steps[4].NodeID))
	if err != nil || c == nil {
		t.Fatal(err)
	}
	materials := 0
	for _, o := range c.Objects {
		if o.Kind == "frozen_design_material" {
			materials++
			if o.OwnerNodeID != "research_design" || o.ReportCopyPolicy != "legacy_verbatim_material" {
				t.Fatal("captured material ownership mismatch")
			}
		}
	}
	if materials != 4 {
		t.Fatal("captured material count", materials)
	}
	// Following the actual old advice cannot replace the original appendix.
	_, _, err = enrichDesignMaterials(raw(`{"markdown":"# Report\n## 附录：本轮设计材料\nActual replacement text."}`), d.Steps[3].Input)
	if err == nil {
		t.Fatal("captured frozen appendix was editable by report")
	}
	// In-memory replay only: keep the original decision and bind declared writes
	// to a NEW input. The private snapshot and frozen old schema stay untouched.
	input := decodeObject(d.Steps[4].Input)
	input["revisionWriteContract"] = c
	output := decodeObject(current)
	plan := output["revisionPlan"].([]any)
	plan[0].(map[string]any)["editTargets"] = []string{writeObjectID(t, c, "frozen_design_material")}
	plan[1].(map[string]any)["editTargets"] = []string{writeObjectID(t, c, "report_authored_body")}
	if validateResearchRevisionPlan(mustJSON(output), mustJSON(input)) == nil {
		t.Fatal("captured report-for-material plan still accepted")
	}
	plan[0].(map[string]any)["nodeId"] = "research_design"
	if err := validateResearchRevisionPlan(mustJSON(output), mustJSON(input)); err != nil {
		t.Fatal(err)
	}
	if output["approved"] != false {
		t.Fatal("science decision changed")
	}
}

func TestLegacyMaterialRevisionDoesNotGuessWritableOwner(t *testing.T) {
	d := materialRevisionFixture(t)
	r := researchRevisionRecommendation(d)
	if r == nil || r.Status != "unavailable" || !strings.Contains(r.Summary, "写入对象") {
		t.Fatalf("unbound material recommendation=%+v", r)
	}
	before := string(d.Steps[1].Output)
	// Following the old recommendation cannot replace the managed appendix.
	_, _, err := enrichDesignMaterials(raw(`{"markdown":"# Report\n## 附录：本轮设计材料\n### Material A\nActual replacement text."}`), d.Steps[3].Input)
	if err == nil || string(d.Steps[1].Output) != before {
		t.Fatal("report unexpectedly acquired ownership", err)
	}
	if _, err := resolveReviewRevisionRequest(d, d.Steps[5], RetryCommand{UseRecommendation: true, ExpectedReviewSHA256: r.ReviewOutputSHA256}); err == nil {
		t.Fatal("unsafe recommendation executable")
	}
}

func TestRevisionWriteCollectionAndCitationPort(t *testing.T) {
	for _, field := range []string{"", `,"deliverables":[]`} {
		t.Run("empty"+field, func(t *testing.T) {
			d := materialRevisionFixture(t)
			design := raw(`{"status":"research_design_not_empirical_result"` + field + `}`)
			d.Run.Compilation.Nodes[1].OutputSchema = dynamicResearchDesignSchema()
			d.Steps[1].Output = rawObject(map[string]any{"analysis": design})
			d.Steps[3].Input = rawObject(map[string]any{"context": design})
			d.Steps[3].InputSHA256 = hashJSON(d.Steps[3].Input)
			input := decodeObject(d.Steps[4].Input)
			input["designContext"] = design
			d.Steps[4].Input = mustJSON(input)
			c := bindMaterialOwnershipFixture(t, &d)
			id := writeObjectID(t, c, "design_material_collection")
			p := []researchRevisionPlanItem{{NodeID: "report_drafting", EditTargets: []string{id}}}
			if validateRevisionWriteObjects(p, d.Steps[4].Input) == nil {
				t.Fatal("report can create missing upstream materials")
			}
			p[0].NodeID = "method_implementation"
			if err := validateRevisionWriteObjects(p, d.Steps[4].Input); err != nil {
				t.Fatal(err)
			}
		})
	}
	d := materialRevisionFixture(t)
	d.Run.Compilation.Nodes[0].Kind = NodeCitationSelection
	d.Steps[0].Output = raw(`{"citations":[]}`)
	targets := append(revisionPlanTargetsForReview(d, d.Steps[4].NodeID), ResearchRevisionTarget{NodeID: d.Steps[0].NodeID})
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: d.Steps[0].NodeID, FromPort: "citations", ToNode: d.Steps[4].NodeID, ToPort: "evidenceContext"})
	c, err := currentRevisionWriteContract(d, d.Steps[4].NodeID, d.Steps[4].Input, targets)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range c.Objects {
		if o.OwnerNodeID == d.Steps[0].NodeID {
			found = true
			if o.OutputPointer != "/citations" || len(o.ReviewRoots) != 1 || o.ReviewRoots[0] != "/evidenceContext" {
				t.Fatalf("wrong citation port: %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("missing citation object")
	}
}

func TestDesignMaterialManagedViewAndLegacyCompilation(t *testing.T) {
	for _, body := range []string{"a", "aba", "中文草稿 🧪"} {
		t.Run(body, func(t *testing.T) {
			d := materialRevisionFixture(t)
			design := map[string]any{"status": "research_design_not_empirical_result", "deliverables": []any{map[string]any{"title": "A", "markdown": body}}}
			d.Steps[1].Output = rawObject(map[string]any{"analysis": design})
			report := d.Run.Compilation.Nodes[3]
			legacy := bindReportComposition(d, report, mustJSON(map[string]any{"context": design}))
			if usesDesignMaterialAssembly(legacy) {
				t.Fatal("old queued compilation silently changed")
			}
			report.PromptVersion += designMaterialPromptSuffix + "-pd1"
			d.Run.Compilation.Nodes[3] = report
			d.Steps[3].Input = bindReportComposition(d, report, legacy)
			d.Steps[3].InputSHA256 = hashJSON(d.Steps[3].Input)
			if !usesDesignMaterialAssembly(d.Steps[3].Input) {
				t.Fatal("new version marker lost with later suffix")
			}
			narrative := "ababa a cat 中文草稿 🧪 remains authored text"
			value := mustJSON(map[string]any{"markdown": narrative, "claimSummary": []string{}, "methodSummary": "Text only", "limitations": []string{"Not validated"}, "confidence": "low"})
			output, _, err := normalizeWorkflowAIStageSubmissionForInput(string(value), report, d.Steps[3].Input)
			if err != nil {
				t.Fatal(err)
			}
			again, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(output), report, d.Steps[3].Input)
			if err != nil || len(changes) != 0 || !rawJSONEqual(output, again) {
				t.Fatal("non-idempotent managed assembly", err)
			}
			d.Steps[3].Output = rawObject(map[string]any{"analysis": output})
			input := decodeObject(d.Steps[4].Input)
			input["context"] = output
			input["designContext"] = design
			bound := bindReportComposition(d, d.Run.Compilation.Nodes[4], mustJSON(input))
			fields := decodeObject(bound)
			if strings.TrimSpace(fields["context"].(map[string]any)["markdown"].(string)) != narrative {
				t.Fatal("incidental material substring incorrectly frozen")
			}
			parts, ok := fields["reportRecordAppendices"].([]any)
			if !ok || len(parts) != 1 || !strings.Contains(parts[0].(string), body) {
				t.Fatal("actual design material not separated")
			}
			assembled, ok := reviewAssembledInput(bound)
			if !ok || !rawJSONEqual(mustJSON(decodeObject(assembled)["context"]), output) {
				t.Fatal("report lost bytes")
			}
			c, err := currentRevisionWriteContract(d, d.Steps[4].NodeID, bound, revisionPlanTargetsForReview(d, d.Steps[4].NodeID))
			if err != nil || c == nil {
				t.Fatal(err)
			}
			for _, o := range c.Objects {
				if o.Kind == "frozen_design_material" && o.ReportCopyPolicy != "host_design_appendix" {
					t.Fatal("wrong copy policy")
				}
			}
			bad := decodeObject(output)
			bad["markdown"] = designMaterialsHeading + "\nChanged material.\n\n" + decodeObject(output)["markdown"].(string)
			if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(bad)), report, d.Steps[3].Input); err == nil {
				t.Fatal("conflicting owned appendix accepted")
			}
		})
	}
}

func TestRevisionOwnershipSizeDoesNotScaleWithSubstringMatches(t *testing.T) {
	d := materialRevisionFixture(t)
	design := map[string]any{"status": "research_design_not_empirical_result", "deliverables": []any{map[string]any{"title": "A", "markdown": "a"}}}
	d.Steps[1].Output = rawObject(map[string]any{"analysis": design})
	d.Steps[3].Input = mustJSON(map[string]any{"context": design})
	d.Steps[3].InputSHA256 = hashJSON(d.Steps[3].Input)
	output := map[string]any{"markdown": strings.Repeat("a", 180000)}
	d.Steps[3].Output = rawObject(map[string]any{"analysis": output})
	input := mustJSON(map[string]any{"context": output, "designContext": design})
	c, err := currentRevisionWriteContract(d, d.Steps[4].NodeID, input, revisionPlanTargetsForReview(d, d.Steps[4].NodeID))
	if err != nil || c == nil || len(mustJSON(c)) > 16000 {
		t.Fatal("ownership grew with matches", err)
	}
}

// This is deliberately NOT claimed to validate natural-language semantics.
// A model can still select the wrong object. Only independent reassessment
// determines whether its declared report-only correction actually fixes science.
func TestRevisionWriteIDsDoNotProveScientificIntent(t *testing.T) {
	d := materialRevisionFixture(t)
	c := bindMaterialOwnershipFixture(t, &d)
	output := mustJSON(map[string]any{"requiredCorrections": []string{"Replace the frozen material"}, "revisionPlan": []researchRevisionPlanItem{{CorrectionIndex: 0, NodeID: "report_drafting", Reason: "Model chose the wrong object", WhyNotLater: "Wrong scientific judgment remains inspectable", EditTargets: []string{writeObjectID(t, c, "report_authored_body")}}}})
	if err := validateResearchRevisionPlan(output, d.Steps[4].Input); err != nil {
		t.Fatal("host guessed semantics", err)
	}
	// A request to relabel the material is not a way to modify its saved source.
	if !strings.Contains(string(d.Steps[1].Output), "not yet drafted") {
		t.Fatal("source altered")
	}
}

func TestDesignMaterialAssemblyKeepsAuthoredLimitsAndExactReplay(t *testing.T) {
	node := CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion + designMaterialPromptSuffix, OutputSchema: dynamicReportSchema()}
	material := strings.Repeat("a", 100000)
	base := map[string]any{"context": map[string]any{"status": "research_design_not_empirical_result", "deliverables": []any{map[string]any{"title": "Large draft", "markdown": material}}}}
	input := bindReportComposition(RunDetail{}, node, mustJSON(base))
	for _, length := range []int{100000, 180000, 180001} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			value := mustJSON(map[string]any{"markdown": strings.Repeat("a", length), "claimSummary": []string{}, "methodSummary": "Text only", "limitations": []string{}, "confidence": "low"})
			output, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(value), node, input)
			if length > 180000 {
				if err == nil {
					t.Fatal("authored limit relaxed")
				}
				return
			}
			if err != nil || len(changes) != 1 || changes[0].Rule != "append_frozen_design_materials" {
				t.Fatal("wrong assembly/normalization", err, changes)
			}
			again, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(output), node, input)
			if err != nil || len(changes) != 0 || !rawJSONEqual(output, again) {
				t.Fatal("large exact host suffix not replayable", err)
			}
			if err := validateReportProvenance(node, output, input); err != nil {
				t.Fatal(err)
			}
			bad := decodeObject(output)
			bad["markdown"] = strings.Replace(bad["markdown"].(string), "### Large draft", "### Altered draft", 1)
			if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(bad)), node, input); err == nil {
				t.Fatal("altered host suffix got capacity exemption")
			}
		})
	}
}

func TestDesignMaterialOnlyCitationsSurviveHostReplay(t *testing.T) {
	d, step, node := handoffFixture()
	fields := decodeObject(step.Input)
	fields["context"] = map[string]any{"status": "research_design_not_empirical_result", "deliverables": []any{map[string]any{"title": "Material A", "markdown": "Actual original material cites [K-AAAAAAAAAAAA]. Unvalidated."}}}
	node.PromptVersion += designMaterialPromptSuffix
	input := bindReportComposition(d, node, addResearchHandoff(d, node, mustJSON(fields)))
	value := raw(`{"markdown":"# Narrative\nNo citations in the authored summary.","claimSummary":[],"methodSummary":"Text only","limitations":[],"confidence":"low"}`)
	output, _, err := enrichReportProvenance(value, input)
	if err != nil || !strings.Contains(string(output), "10.2337/db22-1418-p") {
		t.Fatal("material-only bibliography lost", err)
	}
	for i := 0; i < 3; i++ {
		again, changes, err := enrichReportProvenance(output, input)
		if err != nil || len(changes) != 0 || !rawJSONEqual(output, again) {
			t.Fatal("material bibliography changed on replay", err)
		}
		output = again
	}
}

func TestRevisionWriteReceiptCannotBeDroppedDuringRecovery(t *testing.T) {
	d := materialRevisionFixture(t)
	contract := bindMaterialOwnershipFixture(t, &d)
	value := decodeObject(d.AIExecutions[0].Output)
	value["reviewedInputSha256"] = d.Steps[4].InputSHA256
	plan := value["revisionPlan"].([]any)[0].(map[string]any)
	plan["nodeId"], plan["editTargets"] = "method_implementation", []string{writeObjectID(t, contract, "frozen_design_material")}
	plan["editTargets"] = []string{writeObjectID(t, contract, "frozen_design_material"), writeObjectID(t, contract, "report_authored_body")}
	freezeRecommendationReview(t, &d, value)
	_, _, r, err := reviewRevisionFromTarget(d, d.Steps[5], "method_implementation")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.WriteContract) == 0 || hashJSON(r.WriteContract) != r.WriteContractSHA256 {
		t.Fatal("missing queued receipt")
	}
	d.Steps[1].Status, d.Steps[3].Status = StepQueued, StepQueued
	for _, kind := range []string{"valid", "missing hash", "corrupt", "dropped"} {
		t.Run(kind, func(t *testing.T) {
			copy := r
			switch kind {
			case "missing hash":
				copy.WriteContractSHA256 = ""
			case "corrupt":
				copy.WriteContract = raw(`{"version":"corrupt"}`)
			case "dropped":
				copy.WriteContract = nil
				copy.WriteContractSHA256 = ""
			}
			d.Events = []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: mustJSON(copy)}}
			for _, nodeID := range []string{"method_implementation", "report_drafting"} {
				err := validatePendingRevisionWriteSnapshot(d, nodeID)
				if (err != nil) != (kind != "valid") {
					t.Fatal("recovery silently lost write contract", kind, nodeID, err)
				}
				if kind != "valid" {
					if _, err := bindNodeInput(d, CompiledNode{ID: nodeID}); err == nil || !strings.Contains(err.Error(), "写入对象快照") {
						t.Fatal("invalid receipt reached model input", err)
					}
				} else {
					check := pendingStageReviewCorrections(d, nodeID)
					if check == nil || len(check.Corrections) != 1 || len(check.Corrections[0].EditTargets) != 1 || len(check.WriteContract) == 0 {
						t.Fatal("combined item lost per-owner writes", nodeID)
					}
					kind := "frozen_design_material"
					if nodeID == "report_drafting" {
						kind = "report_authored_body"
					}
					if check.Corrections[0].EditTargets[0] != writeObjectID(t, contract, kind) {
						t.Fatal("combined item got another owner's write", nodeID)
					}
				}
			}
		})
	}
}

func TestNonDesignReviewRejectsUnboundWriteIDsBeforeRetry(t *testing.T) {
	d := recommendationFixture(t)
	output := decodeObject(d.AIExecutions[0].Output)
	output["revisionPlan"].([]any)[0].(map[string]any)["editTargets"] = []string{"edit-unoffered"}
	if err := validateResearchRevisionPlan(mustJSON(output), d.Steps[4].Input); err == nil {
		t.Fatal("unbound IDs accepted until retry")
	}
	delete(output["revisionPlan"].([]any)[0].(map[string]any), "editTargets")
	if err := validateResearchRevisionPlan(mustJSON(output), d.Steps[4].Input); err != nil {
		t.Fatal("ordinary legacy/non-design plan changed", err)
	}
}

func TestDesignMaterialEchoExemptsOnlyExactHostBytes(t *testing.T) {
	node := CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion + designMaterialPromptSuffix, OutputSchema: dynamicReportSchema()}
	input := bindReportComposition(RunDetail{}, node, mustJSON(map[string]any{"context": map[string]any{"status": "research_design_not_empirical_result", "deliverables": []any{map[string]any{"title": "Draft", "markdown": strings.Repeat("文", 50000)}}}}))
	value := mustJSON(map[string]any{"markdown": strings.Repeat("文", 40000), "claimSummary": []string{}, "methodSummary": "Text", "limitations": []string{}, "confidence": "low"})
	full, _, err := enrichReportProvenance(value, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= maxStageJSONCandidateBytes {
		t.Fatal("fixture did not cross raw byte ceiling")
	}
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(full), node, input); err != nil {
		t.Fatal("exact already-bound host bytes were treated as authored content", err)
	}
	for _, text := range []string{string(full) + " trailing-invalid", strings.Replace(string(full), "### Draft", "### Wrong", 1)} {
		if _, _, err := normalizeWorkflowAIStageSubmissionForInput(text, node, input); err == nil {
			t.Fatal("nonexact suffix escaped parser limits")
		}
	}
}
