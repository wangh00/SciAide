package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reportRevisionFixture() (RunDetail, ResearchRevisionProposal) {
	code := raw(`{"analysis":{"code":"print('unchanged computation')","methodSummary":"original frozen method"}}`)
	results := raw(`{"structured":{"value":{"rawBaseline":0.6852791878172588,"rawModel":0.5169376078243285}},"artifacts":[{"workspacePath":"analysis-output/metrics.json","sha256":"original-result-hash"}]}`)
	var resultRecord map[string]json.RawMessage
	_ = json.Unmarshal(results, &resultRecord)
	resultRecord["structured"] = rawObject(map[string]any{"status": "success", "codeSha256": hashStrings("print('unchanged computation')"), "value": raw(`{"rawBaseline":0.6852791878172588,"rawModel":0.5169376078243285}`)})
	results = mustJSON(resultRecord)
	report := raw(`{"markdown":"# Prior report\nKeep this exact scientific content.","limitations":["bounded evidence"],"confidence":"medium"}`)
	prior := rawObject(map[string]any{"analysis": report, "text": "```json\n" + string(report) + "\n```", "implementationReview": nil})
	p := ResearchRevisionProposal{ID: "confirmed-id", NodeID: "report_drafting", Summary: "Write thesis conclusions", Changes: []string{"Keep the analysis unchanged; explain the result"}, AffectedStages: []string{"Report", "Review", "Gate"}}
	d := RunDetail{Run: Run{ID: "run", Inputs: raw(`{}`), Compilation: Compilation{Nodes: []CompiledNode{
		{ID: "method_implementation", Kind: NodeAgentStage},
		{ID: "python_analysis", Kind: NodePython, SideEffect: true},
		{ID: "report_drafting", Kind: NodeAgentStage, PromptVersion: researchReportVersion, OutputSchema: dynamicReportSchema()},
		{ID: "independent_review", Kind: NodeAIAnalysis, OutputSchema: trackedResearchReviewSchema()},
		{ID: "delivery_gate", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}},
	}, Edges: []Edge{{FromNode: "method_implementation", FromPort: "analysis", ToNode: "report_drafting", ToPort: "implementationContext"}, {FromNode: "python_analysis", FromPort: "structured", ToNode: "report_drafting", ToPort: "computedResults"}}}}, Steps: []Step{
		{NodeID: "method_implementation", Ordinal: 0, Status: StepCompleted, Output: code},
		{NodeID: "python_analysis", Ordinal: 1, Status: StepCompleted, Output: results},
		{NodeID: "report_drafting", Ordinal: 2, Status: StepQueued},
		{NodeID: "independent_review", Ordinal: 3, Status: StepQueued},
		{NodeID: "delivery_gate", Ordinal: 4, Status: StepQueued},
	}}
	d.Events = []RuntimeEvent{{Type: "workflow.user_revision_queued", Payload: rawObject(map[string]any{"proposal": p, "startOrdinal": 2, "priorOutput": prior, "comparisonBaseline": researchComparisonBaseline(d)})}}
	return d, p
}

func TestReportOnlyRevisionRemovesOnlyHistoricalDuplicates(t *testing.T) {
	d, p := reportRevisionFixture()
	before := mustJSON(d)
	if !reportOnlyRevision(d, p) {
		t.Fatal("safe report-only suffix not recognized")
	}
	node := compilationNodeMap(d.Run.Compilation)["report_drafting"]
	bound, err := bindNodeInput(d, node)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]json.RawMessage
	_ = json.Unmarshal(bound, &input)
	var revision map[string]json.RawMessage
	_ = json.Unmarshal(input["_userRevision"], &revision)
	if len(revision["comparisonBaseline"]) != 0 || !strings.Contains(string(revision["reuseContext"]), `"revisionId":"confirmed-id"`) || strings.Contains(string(revision["priorOutput"]), `"text":`) {
		t.Fatal("duplicate baseline/submission still present or history navigation absent", string(input["_userRevision"]))
	}
	originalReport, err := outputPort(priorUserRevisionOutput(d, node.ID), "analysis")
	if err != nil {
		t.Fatal(err)
	}
	retainedReport, err := outputPort(revision["priorOutput"], "analysis")
	if err != nil || !bytes.Equal(originalReport, retainedReport) {
		t.Fatal("previous structured report content changed", err)
	}
	implementation, _ := outputPort(d.Steps[0].Output, "analysis")
	computed, _ := outputPort(d.Steps[1].Output, "structured")
	if !bytes.Equal(input["implementationContext"], implementation) || !bytes.Equal(input["computedResults"], computed) {
		t.Fatal("current method or executed precision removed")
	}
	step := Step{Input: bound, InputSHA256: hashJSON(bound)}
	prompt := buildAIStagePrompt(d, step, node)
	for _, want := range []string{reportRevisionReuseInstruction, researchNumericPrecisionInstruction, researchReportScopeInstruction, reportConfidenceInstruction, "<stage_input>\n" + string(bound) + "\n</stage_input>"} {
		if !strings.Contains(prompt, want) {
			t.Error("missing full new input or generation guidance", want)
		}
	}
	if !bytes.Equal(before, mustJSON(d)) || len(priorResearchArtifacts(d)) != 1 || !strings.Contains(trustedWorkspaceFilesInstruction(d), "analysis-output/metrics.json") {
		t.Fatal("durable history/artifact access changed")
	}
	if reportRevisionReusePrompt(RunDetail{}, node, input) != "" {
		t.Fatal("untrusted reuse flag became authoritative without host confirmation")
	}
	forged := map[string]json.RawMessage{"_userRevision": raw(`{"proposalId":"other","reuseContext":{"kind":"report_only","revisionId":"other"}}`)}
	if reportRevisionReusePrompt(d, node, forged) != "" {
		t.Fatal("foreign revision reuse metadata accepted")
	}
	review := compilationNodeMap(d.Run.Compilation)["independent_review"]
	context := userRevisionStageContext(d, review, p)
	if _, exists := context["comparisonBaseline"]; exists || context["reuseContext"] == nil || context["priorOutput"] != nil || len(context["changes"].([]string)) != 1 {
		t.Fatal("downstream duplication or lost requirements", context)
	}
}

func TestReportOnlyRevisionNeverInfersScopeFromWordingOrWeakensCustomSuffix(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*RunDetail, *ResearchRevisionProposal)
	}{
		{"method target", func(d *RunDetail, p *ResearchRevisionProposal) { p.NodeID = "method_implementation" }},
		{"side effects", func(d *RunDetail, p *ResearchRevisionProposal) { p.RepeatsSideEffects = true }},
		{"source selection", func(d *RunDetail, p *ResearchRevisionProposal) { p.SourceVersion = &ResearchSourceVersion{} }},
		{"data selection", func(d *RunDetail, p *ResearchRevisionProposal) { p.DataVersion = &ResearchDataVersion{} }},
		{"new material", func(d *RunDetail, p *ResearchRevisionProposal) {
			p.Materials = []RevisionMaterial{{AttachmentID: "file"}}
		}},
		{"unknown suffix", func(d *RunDetail, p *ResearchRevisionProposal) { d.Steps[4].NodeID = "unknown" }},
		{"new computation suffix", func(d *RunDetail, p *ResearchRevisionProposal) {
			d.Steps = append(d.Steps, Step{NodeID: "python_analysis", Ordinal: 5})
		}},
		{"custom output", func(d *RunDetail, p *ResearchRevisionProposal) {
			d.Run.Compilation.Nodes[2].PromptVersion = "custom-report-v1"
		}},
		{"custom review side effect", func(d *RunDetail, p *ResearchRevisionProposal) { d.Run.Compilation.Nodes[3].SideEffect = true }},
		{"later upstream repair", func(d *RunDetail, p *ResearchRevisionProposal) {
			d.Events = append(d.Events, RuntimeEvent{Type: "workflow.upstream_revision_queued", Payload: raw(`{"producerNodeId":"method_implementation"}`)})
		}},
		{"later broader review repair", func(d *RunDetail, p *ResearchRevisionProposal) {
			d.Events = append(d.Events, RuntimeEvent{Type: "workflow.review_revision_queued", Payload: raw(`{"producerNodeId":"method_implementation"}`)})
		}},
		{"unknown later repair", func(d *RunDetail, p *ResearchRevisionProposal) {
			d.Events = append(d.Events, RuntimeEvent{Type: "workflow.review_revision_queued", Payload: raw(`{}`)})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, p := reportRevisionFixture()
			p.Summary = "Only wording, do not recalculate"
			tc.edit(&d, &p)
			if reportOnlyRevision(d, p) {
				t.Fatal("unsafe or unsupported scope optimized from intent wording")
			}
			node := CompiledNode{ID: "report_drafting", Kind: NodeAgentStage}
			if value := userRevisionStageContext(d, node, p); value["comparisonBaseline"] == nil || value["reuseContext"] != nil {
				t.Fatal("other revisions lost baseline", value)
			}
		})
	}
	d, p := reportRevisionFixture()
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.review_revision_queued", Payload: raw(`{"producerNodeId":"report_drafting"}`)})
	if !reportOnlyRevision(d, p) {
		t.Fatal("later report-only correction needlessly regained a duplicate method baseline")
	}
}

func TestReportRevisionPriorOutputPreservesDistinctOrImpreciseText(t *testing.T) {
	for _, text := range []string{"Extra commentary\n```json\n{\"value\":1}\n```", "{\"value\":9007199254740992}", "{\"value\":9007199254740993} trailing", "unparsed raw response"} {
		prior := rawObject(map[string]any{"analysis": raw(`{"value":9007199254740993}`), "text": text, "unknownField": "preserve"})
		if !bytes.Equal(prior, reportRevisionPriorOutput(prior)) {
			t.Fatal("distinct prior evidence removed", text)
		}
	}
	prior := rawObject(map[string]any{"analysis": raw(`{"value":9007199254740993,"unusual":1.234567890123456789e-42}`), "text": "{\"unusual\":1.234567890123456789e-42,\"value\":9007199254740993}", "unknownField": "preserve"})
	result := reportRevisionPriorOutput(prior)
	if strings.Contains(string(result), `"text":`) || !strings.Contains(string(result), "9007199254740993") || !strings.Contains(string(result), "1.234567890123456789e-42") || !strings.Contains(string(result), `"unknownField":"preserve"`) {
		t.Fatal("lossy numeric decode or changed unrelated fields", string(result))
	}
}

func TestResearchNumericAndScopeGuidanceReachesDiscussionReportAndReview(t *testing.T) {
	// The fixture documents the actual last-digit discrepancy. No wine-specific
	// calculator/validator, output requirement or automatic report rewrite exists.
	var operands struct{ Baseline, Model float64 }
	if err := json.Unmarshal(raw(`{"Baseline":0.6852791878172588,"Model":0.5169376078243285}`), &operands); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%.4f", operands.Baseline-operands.Model) != "0.1683" || fmt.Sprintf("%.1f", (operands.Baseline-operands.Model)/operands.Baseline*100) != "24.6" || fmt.Sprintf("%.4f", math.Round(operands.Baseline*1e4)/1e4-math.Round(operands.Model*1e4)/1e4) != "0.1684" {
		t.Fatal("precision counterexample no longer reproduces")
	}
	for _, text := range []string{researchDiscussionSystemContext("saved state"), researchSystemContext("frozen state"), buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion}), buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, CompiledNode{ID: "review", OutputSchema: trackedResearchReviewSchema()})} {
		for _, want := range []string{researchNumericPrecisionInstruction, researchReportScopeInstruction, "round only the final displayed value", "zero/negative denominators", "NOT INCLUDED in this round", "concrete promised work still missing", "not an erroneous number"} {
			if !strings.Contains(text, want) {
				t.Error("precision or scope distinction missing", want)
			}
		}
	}
	for _, version := range []string{"dynamic-report-v6-claim-assessment-v1", "dynamic-report-v6-claim-assessment-v1-data-audit-v1", "dynamic-report-v6-claim-assessment-v2", "dynamic-report-v6-" + claimAssessmentVersion} {
		if !hasClaimAssessmentVersion(version) {
			t.Fatal("old claim-based history no longer identifiable", version)
		}
	}
	input, output := acceptanceFixture(t)
	output["approved"] = false
	output["methodIssues"] = []string{"Promised data collection plan is missing"}
	output["requiredCorrections"] = []string{"Provide the agreed data collection plan"}
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "Population/variables present"}, {"criterion-2", "not_met", "Promised data collection plan missing"}}
	node := CompiledNode{ID: "review", OutputSchema: researchAcceptanceReviewSchema()}
	before := mustJSON(output)
	if err := validateWorkflowAIStageOutput(node, before, input); err != nil {
		t.Fatal("valid scope-based rejection not accepted", err)
	}
	if prompt := buildAIStagePrompt(RunDetail{}, Step{Input: input}, node); !strings.Contains(prompt, researchReportScopeInstruction) || !bytes.Equal(before, mustJSON(output)) {
		t.Fatal("scope guidance rewrote a genuine missing requirement")
	}
	output["approved"] = true
	if err := validateWorkflowAIStageOutput(node, mustJSON(output), input); err == nil {
		t.Fatal("scope wording guidance weakened approval checks for promised missing work")
	}
}

func TestExecutionVersionAllMarkersCannotPanicOrLoseTransportIdentity(t *testing.T) {
	prompt := reviewScopeInstruction + reportConfidenceInstruction + implementationDecisionInstruction + ImplementationPartsInstruction + ImplementationPatchInstruction + implementationEfficiencyInstruction + computationAuditInstruction + kernelReviewNavigationInstruction
	// Exercise the extreme stored identity too, even if a normal node cannot
	// enable every kind of stage guidance simultaneously.
	markers := []string{reviewScopeVersion, claimAssessmentVersion, implementationDecisionVersion, "impl-parts-v1", "impl-patch-v1", implementationEfficiencyVersion, computationAuditVersion, reviewKernelPathVersion}
	node := CompiledNode{ID: "method_implementation", PromptVersion: strings.Repeat("v", 128) + "-" + strings.Join(markers, "-"), OutputSchema: trackedResearchReviewSchema()}
	version := reportExecutionPromptVersion(node, prompt)
	if len(version) > 128 || !strings.Contains(version, "impl-parts-v1") || !strings.Contains(version, "impl-patch-v1") || !hasClaimAssessmentVersion(version) {
		t.Fatal("effective identity lost consumer markers", version)
	}
	node.PromptVersion = version
	if next := reportExecutionPromptVersion(node, prompt); next != version {
		t.Fatal("compact alias identity not idempotent", version, next)
	}
}

// Optional offline replay, never reads the app DB or calls a commercial model.
func TestCapturedReportOnlyRevisionContextReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REPORT_QUALITY_REPLAY")
	if dir == "" {
		t.Skip("no task-scoped offline capture supplied")
	}
	read := func(name string, v any) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, v); err != nil {
			t.Fatal(err)
		}
	}
	var run struct{ ID, Compilation, Inputs string }
	// SQL names are explicitly mapped; do not substitute generated fixture state.
	var sqlRun map[string]json.RawMessage
	read("run-original.json", &sqlRun)
	_ = json.Unmarshal(sqlRun["id"], &run.ID)
	_ = json.Unmarshal(sqlRun["compilation_json"], &run.Compilation)
	_ = json.Unmarshal(sqlRun["inputs_json"], &run.Inputs)
	detail := RunDetail{Run: Run{ID: run.ID, Inputs: json.RawMessage(run.Inputs)}}
	if err := json.Unmarshal([]byte(run.Compilation), &detail.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	var steps []struct {
		NodeID   string     `json:"node_id"`
		Ordinal  int        `json:"ordinal"`
		Status   StepStatus `json:"status"`
		Input    string     `json:"input_json"`
		InputSHA string     `json:"input_sha256"`
		Output   string     `json:"output_json"`
	}
	read("steps-original.json", &steps)
	for _, row := range steps {
		detail.Steps = append(detail.Steps, Step{NodeID: row.NodeID, Ordinal: row.Ordinal, Status: row.Status, Input: json.RawMessage(row.Input), InputSHA256: row.InputSHA, Output: json.RawMessage(row.Output)})
	}
	var events []struct {
		Type    string `json:"event_type"`
		Payload string `json:"payload_json"`
	}
	read("events-original.json", &events)
	for _, row := range events {
		detail.Events = append(detail.Events, RuntimeEvent{Type: row.Type, Payload: json.RawMessage(row.Payload)})
	}
	before := mustJSON(detail)
	metrics := map[string]any{}
	for _, id := range []string{"report_drafting", "independent_review"} {
		step := findStepByNode(detail.Steps, id)
		node := compilationNodeMap(detail.Run.Compilation)[id]
		p := pendingUserRevision(detail, id)
		if p == nil || !reportOnlyRevision(detail, *p) {
			t.Fatal("capture does not contain verified report-only suffix")
		}
		var original map[string]json.RawMessage
		_ = json.Unmarshal(step.Input, &original)
		originalRev := original["_userRevision"]
		original["_userRevision"] = mustJSON(userRevisionStageContext(detail, node, *p))
		derived := mustJSON(original)
		// All fields outside the revision context must remain identical.
		var got map[string]json.RawMessage
		_ = json.Unmarshal(derived, &got)
		for key, value := range original {
			if key != "_userRevision" && !bytes.Equal(value, got[key]) {
				t.Fatal("captured current evidence changed", id, key)
			}
		}
		if len(derived) >= len(step.Input) || bytes.Contains(derived, []byte(`"comparisonBaseline"`)) {
			t.Fatal("offline revision not slimmer", id)
		}
		derivedStep := *step
		derivedStep.Input, derivedStep.InputSHA256 = derived, hashJSON(derived)
		originalPrompt := buildAIStagePrompt(detail, *step, node)
		derivedPrompt := buildAIStagePrompt(detail, derivedStep, node)
		if !strings.Contains(derivedPrompt, "<stage_input>\n"+string(derived)+"\n</stage_input>") || !strings.Contains(derivedPrompt, reportRevisionReuseInstruction) {
			t.Fatal("new execution not bound to full derived input")
		}
		metrics[id] = map[string]any{"oldInputBytes": len(step.Input), "newInputBytes": len(derived), "oldRevisionBytes": len(originalRev), "newRevisionBytes": len(got["_userRevision"]), "sameBuildOldPromptBytes": len(originalPrompt), "newPromptBytes": len(derivedPrompt)}
		if err := os.WriteFile(filepath.Join(dir, id+"-input-derived.json"), derived, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s input %d -> %d bytes; comparable new-guidance prompts %d -> %d bytes", id, len(step.Input), len(derived), len(originalPrompt), len(derivedPrompt))
	}
	if !bytes.Equal(before, mustJSON(detail)) {
		t.Fatal("offline replay mutated original state")
	}
	if err := os.WriteFile(filepath.Join(dir, "context-replay-metrics.json"), mustJSON(metrics), 0600); err != nil {
		t.Fatal(err)
	}
}
