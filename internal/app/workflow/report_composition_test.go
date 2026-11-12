package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func composedReportFixture(t *testing.T) (RunDetail, CompiledNode, json.RawMessage) {
	t.Helper()
	d, step, report := handoffFixture()
	fields := decodeObject(step.Input)
	fields["researchSourceContext"].(map[string]any)["retrieval"] = map[string]any{"candidateCount": 68, "queries": []string{"TRE"}, "firstPageOnly": true}
	step.Input = bindReportComposition(d, report, addResearchHandoff(d, report, mustJSON(fields)))
	step.InputSHA256 = hashJSON(step.Input)
	step.Status, step.Attempt = StepCompleted, 2
	value := raw(`{"markdown":"# 中文报告 🧪\n无法核实匹配，保留不确定性。[K-AAAAAAAAAAAA]","claimSummary":[],"methodSummary":"有限综述","limitations":["缺少实际摄入"],"confidence":"low"}`)
	output, _, err := enrichReportProvenance(value, step.Input)
	if err != nil {
		t.Fatal(err)
	}
	step.Output = rawObject(map[string]any{"analysis": output})
	d.Steps[len(d.Steps)-1] = step
	review := CompiledNode{ID: "independent_review", Kind: NodeAgentStage, OutputSchema: trackedResearchReviewSchema()}
	d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, report, review)
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: report.ID, FromPort: "analysis", ToNode: review.ID, ToPort: "context"})
	input := rawObject(map[string]any{"context": output, "researchSourceContext": researchSourceContext(d)})
	return d, review, input
}

func TestReportCompositionOwnershipIsExactAndDoesNotRewriteScience(t *testing.T) {
	d, node, input := composedReportFixture(t)
	before := mustJSON(d)
	bound := bindReportComposition(d, node, input)
	c := currentReportComposition(d, node, bound)
	if c == nil || len(c.Sections) != 3 || c.Attempt != 2 || c.Pointer != "/context/markdown" {
		t.Fatalf("composition=%+v", c)
	}
	body := decodeObject(mustJSON(decodeObject(input)["context"]))["markdown"].(string)
	for _, section := range c.Sections {
		actual := string([]rune(body)[section.Start:section.End])
		if !strings.HasPrefix(actual, section.Heading) || hashBytes([]byte(actual)) != section.SHA256 || !strings.Contains(actual, "程序管理的记录附录") {
			t.Fatal("incorrect exact range", section)
		}
	}
	if strings.Contains(body, "合并去重候选数") || !strings.Contains(body, "保存的候选记录数：68") || !strings.Contains(body, "无法核实匹配") {
		t.Fatal(body)
	}
	assembled, ok := reviewAssembledInput(bound)
	if !ok || !rawJSONEqual(mustJSON(decodeObject(assembled)["context"]), mustJSON(decodeObject(input)["context"])) || !reflect.DeepEqual(before, mustJSON(d)) {
		t.Fatal("changed science or saved state")
	}
	step := Step{NodeID: node.ID, Input: bound}
	if err := validateReviewDependencies(d, step); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buildAIStagePrompt(d, step, node), reportCompositionReviewInstruction) {
		t.Fatal("review ownership instruction absent")
	}
	if _, _, err := reviewBasisAt(decodeObject(bound), "/reportComposition/hostManagedSections/0/heading"); err == nil {
		t.Fatal("ownership metadata became scientific evidence")
	}
	// Mutating a real dependency fails; losing provenance never becomes a
	// reason to silently trust an author-supplied heading.
	d.Steps[len(d.Steps)-1].InputSHA256 = "wrong"
	if currentReportComposition(d, node, input) != nil || validateReviewDependencies(d, step) == nil {
		t.Fatal("stale ownership accepted")
	}
}

func TestReportAssemblyVersionPreservesFrozenReportsAndRejectsFakeAppendix(t *testing.T) {
	d, review, input := composedReportFixture(t)
	step := &d.Steps[len(d.Steps)-1]
	report := compilationNodeMap(d.Run.Compilation)[step.NodeID]
	original := decodeObject(step.Input)
	delete(original, "reportAssembly")
	legacy := mustJSON(original)
	oldReport := raw(`{"markdown":"# Body [K-AAAAAAAAAAAA]","claimSummary":[],"methodSummary":"methods","limitations":[],"confidence":"low"}`)
	old, _, err := enrichReportProvenance(oldReport, legacy)
	if err != nil || !strings.Contains(string(old), "合并去重候选数") {
		t.Fatal(err)
	}
	again, changes, err := enrichReportProvenance(old, legacy)
	if err != nil || len(changes) != 0 || !rawJSONEqual(old, again) || validateReportProvenance(report, old, legacy) != nil {
		t.Fatal("old frozen report invalidated", err)
	}
	step.Input, step.InputSHA256, step.Output = legacy, hashJSON(legacy), rawObject(map[string]any{"analysis": old})
	root := decodeObject(input)
	root["context"] = old
	if c := currentReportComposition(d, review, mustJSON(root)); c == nil || len(c.Sections) != 3 {
		t.Fatal("exact historical appendix unrecognized")
	}
	// Heading by itself is not proof of ownership.
	bad := raw(strings.Replace(string(old), "保存的排除理由", "invented reason", 1))
	step.Output = rawObject(map[string]any{"analysis": bad})
	root["context"] = bad
	if currentReportComposition(d, review, mustJSON(root)) != nil {
		t.Fatal("altered appendix assigned to host")
	}
	if _, _, err := enrichReportProvenance(bad, legacy); err == nil {
		t.Fatal("fake appendix accepted")
	}
}

func TestCapturedReportRepairOwnershipAndCapabilities(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REPORT_REPAIR_CAPTURE")
	if dir == "" {
		t.Skip("no isolated capture supplied")
	}
	b, err := os.ReadFile(filepath.Join(dir, "detail.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d RunDetail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	// detail.json is navigation; restore raw JSON bytes from the SQLite capture
	// so its persisted byte hashes are not replaced by pretty-printed JSON.
	for _, file := range []string{"workflow_steps.json", "workflow_ai_executions.json"} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(data, &rows); err != nil {
			t.Fatal(err)
		}
		str := func(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
		for _, row := range rows {
			id := str(row["id"])
			for i := range d.Steps {
				if d.Steps[i].ID == id {
					d.Steps[i].Input, d.Steps[i].Output = raw(str(row["input_json"])), raw(str(row["output_json"]))
				}
			}
			for i := range d.AIExecutions {
				if d.AIExecutions[i].ID == id {
					d.AIExecutions[i].Output = raw(str(row["output_json"]))
				}
			}
		}
	}
	before := mustJSON(d)
	review := *findStepByNode(d.Steps, "independent_review")
	node := compilationNodeMap(d.Run.Compilation)[review.NodeID]
	gate := *findStepByNode(d.Steps, "delivery_gate")
	if _, _, err := currentRejectedReview(d, gate); err != nil {
		t.Fatal("saved review invalidated by the new assembly", err)
	}
	if c := currentReportComposition(d, node, review.Input); c == nil || len(c.Sections) != 3 {
		t.Fatalf("captured host origin not identified: %+v", c)
	}
	capabilities := revisionTargetCapabilities(d, revisionPlanTargetsForReview(d, node.ID))
	selection := capabilities["evidence_extraction"]
	screen := capabilities["evidence_screening"]
	if selection.Selected != 11 || selection.Recommended != 11 || selection.Missing != 0 || selection.Available != 62 || len(selection.LookupTools) != 0 || screen.Adoption == "" {
		t.Fatalf("capabilities=%+v", capabilities)
	}
	if screen.LocalSearch == nil || *screen.LocalSearch != 0 || !strings.Contains(screen.Limits, "本地关键词补查额度已用尽") {
		t.Fatal("spent local-search budget advertised as available", screen)
	}
	// The existing manual range control can repair this already-saved plan
	// without silently changing history or asking for a new planning round.
	producer, _, revision, err := reviewRevisionFromTarget(d, gate, "evidence_screening")
	if err != nil || producer.NodeID != "evidence_screening" || !strings.Contains(string(revision.IndependentReview), "issue-2-1") {
		t.Fatal("existing explicit upstream repair unavailable", err)
	}
	report := *findStepByNode(d.Steps, "report_drafting")
	reportNode := compilationNodeMap(d.Run.Compilation)[report.NodeID]
	var execution AIExecution
	for _, e := range d.AIExecutions {
		if e.WorkflowStepID == report.ID && e.Attempt == report.Attempt {
			execution = e
		}
	}
	// Replay raw authored output, not an edited scientific answer. Restore
	// citation identities through the production path before host assembly.
	nextInput := bindReportComposition(d, reportNode, report.Input)
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(execution.OutputText, reportNode, nextInput)
	if err != nil {
		t.Fatal(err)
	}
	value = canonicalCitationSubmission(value, d, report, reportNode, execution.ChatRunID)
	value, _, err = enrichReportProvenance(value, nextInput)
	if err != nil || validateWorkflowAIStageOutput(reportNode, value, nextInput) != nil {
		t.Fatal("reassembled report invalid", err)
	}
	if strings.Contains(string(value), "合并去重候选数") || !strings.Contains(string(value), "保存的候选记录数：68") || !strings.Contains(string(value), "仍无法确定") {
		t.Fatal("counter/science wrong")
	}
	for _, heading := range []string{screeningAppendixHeading, bibliographyAppendixHeading, retrievalAppendixHeading} {
		if strings.Count(string(value), heading) != 1 {
			t.Fatal("duplicate appendix", heading)
		}
	}
	// Full next-review dependency validation of the derived report, with the
	// production capture left untouched. No new verdict is manufactured.
	var next RunDetail
	if err := json.Unmarshal(mustJSON(d), &next); err != nil {
		t.Fatal(err)
	}
	nextReport := findStepByNode(next.Steps, report.NodeID)
	nextReport.Input, nextReport.InputSHA256 = nextInput, hashJSON(nextInput)
	nextReport.Output = rawObject(map[string]any{"analysis": value})
	nextReview := *findStepByNode(next.Steps, review.NodeID)
	nextRoot := decodeObject(nextReview.Input)
	nextRoot["context"] = value
	nextReview.Input = addResearchSourceContext(next, node, mustJSON(nextRoot))
	nextReview.InputSHA256 = hashJSON(nextReview.Input)
	if currentReportComposition(next, node, nextReview.Input) == nil {
		t.Fatal("new assembled report ownership lost")
	}
	if err := validateReviewDependencies(next, nextReview); err != nil {
		t.Fatal("new report cannot reach review gate", err)
	}
	root := decodeObject(review.Input)
	root["revisionTargetCapabilities"] = capabilities
	bound := bindReportComposition(d, node, mustJSON(root))
	review.Input, review.InputSHA256 = bound, hashJSON(bound)
	prompt := buildAIStagePrompt(d, review, node)
	for _, instruction := range []string{reportCompositionReviewInstruction, revisionTargetCapabilityInstruction} {
		if !strings.Contains(prompt, instruction) {
			t.Fatal("missing repair guidance")
		}
	}
	for name, data := range map[string][]byte{"reassembled-report.json": value, "review-input.json": bound, "review-prompt.txt": []byte(prompt)} {
		if err := os.WriteFile(filepath.Join(dir, "derived", name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, mustJSON(d)) {
		t.Fatal("capture mutated")
	}
	t.Log("real capture: 3 host appendices, 62 candidates / 11 recommended / 11 adopted; raw body preserved, old verdict NOT changed")
}

func TestRevisionCapabilitiesReflectToolsWithoutAddingPermissions(t *testing.T) {
	d, review, input := composedReportFixture(t)
	d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, AllowedTools: []ToolSnapshot{{QualifiedName: "builtin.resource.open"}}})
	before := mustJSON(d)
	targets := []ResearchRevisionTarget{{NodeID: "evidence_screening"}, {NodeID: "report_drafting"}}
	c := revisionTargetCapabilities(d, targets)
	if c["evidence_screening"].Adoption != "" || len(c["evidence_screening"].LookupTools) != 1 || !strings.Contains(c["report_drafting"].Limits, "不能通过改正文补造证据") {
		t.Fatal(c)
	}
	if !reflect.DeepEqual(before, mustJSON(d)) {
		t.Fatal("tools changed")
	}
	// Irrelevant long instructions are not injected into non-literature reviews.
	if strings.Contains(buildAIStagePrompt(d, Step{Input: input}, review), revisionTargetCapabilityInstruction) {
		t.Fatal("unrelated capability instruction")
	}
}

func TestReportAssemblyCollapsesOnlyExactConsecutiveHostSuffixes(t *testing.T) {
	d, _, _ := composedReportFixture(t)
	step := d.Steps[len(d.Steps)-1]
	output, _ := outputPort(step.Output, "analysis")
	fields := decodeObject(output)
	markdown := fields["markdown"].(string)
	parts, err := assembledReportAppendices(markdown, step.Input)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.Join(parts, "\n\n")
	for _, repeats := range []int{1, 2, 3} {
		fields["markdown"] = markdown + strings.Repeat("\n\n"+suffix, repeats)
		value, changes, err := enrichReportProvenance(mustJSON(fields), step.Input)
		if err != nil || len(changes) != 1 || changes[0].Rule != "deduplicate_identical_host_appendices" || !rawJSONEqual(value, output) {
			t.Fatal("duplicate host suffix not normalized", repeats, err)
		}
	}
	// Similar-looking content is not proven program output. Never remove it.
	altered := strings.Replace(suffix, "保存的候选记录数：68", "保存的候选记录数：999", 1)
	fields["markdown"] = strings.TrimSuffix(markdown, suffix) + altered + "\n\n" + suffix
	value, changes, err := enrichReportProvenance(mustJSON(fields), step.Input)
	if err != nil || len(changes) != 0 || !strings.Contains(string(value), "999") {
		t.Fatal("unproven authored content erased", err)
	}
}
