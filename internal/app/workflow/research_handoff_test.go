package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func handoffFixture() (RunDetail, Step, CompiledNode) {
	detail, step := evidenceNavigationFixture()
	step.NodeID, step.ID = "report_drafting", "report-step"
	fields := decodeObject(step.Input)
	evidence := fields["evidenceContext"].([]any)
	for i, item := range evidence {
		e := item.(map[string]any)
		e["reference"] = []string{"[K-AAAAAAAAAAAA]", "[K-BBBBBBBBBBBB]"}[i]
		e["indexVersionId"], e["chunkId"], e["quoteSha256"] = "index", []string{"a", "b"}[i], "quotehash"
	}
	bib := fields["researchSourceContext"].(map[string]any)["bibliography"].([]any)
	bib[1].(map[string]any)["authors"] = []any{map[string]any{"name": "SHUHAO LIN"}}
	bib[1].(map[string]any)["doi"] = "10.2337/db22-1418-p"
	step.Input = mustJSON(fields)
	choice := &detail.Steps[1]
	choice.NodeID = "evidence_extraction"
	choice.Output = mustJSON(map[string]any{"citations": evidence})
	detail.Run.Compilation = Compilation{
		Nodes: []CompiledNode{{ID: choice.NodeID, Kind: NodeCitationSelection}},
		Edges: []Edge{{FromNode: choice.NodeID, ToNode: step.NodeID}},
	}
	detail.Steps = append(detail.Steps, step)
	node := CompiledNode{ID: step.NodeID, Kind: NodeAgentStage, PromptVersion: researchReportVersion, OutputSchema: dynamicReportSchema()}
	return detail, step, node
}

func TestResearchHandoffSavesFactsAndGeneratesActualReferenceBibliography(t *testing.T) {
	detail, step, node := handoffFixture()
	before := mustJSON(detail)
	input := addResearchHandoff(detail, node, step.Input)
	stage, nav, ok := parseResearchHandoff(input)
	if !ok || len(stage.Handoff.ExcludedRecords) != 1 || stage.Handoff.ExcludedRecords[0].Reason != "wrong population" || nav.Screening.RecordCount != 3 || nav.Screening.RecordLocation.OutputSHA256 != hashJSON(detail.Steps[0].Output) {
		t.Fatal("lost saved exclusions or source identity", string(input))
	}
	step.Input = input
	prompt := buildAIStagePrompt(detail, step, node)
	if !strings.Contains(prompt, "wrong population") || !strings.Contains(prompt, researchHandoffInstruction) || strings.Count(prompt, `"decisionCounts"`) != 1 || !strings.Contains(reportExecutionPromptVersion(node, prompt), researchHandoffVersion) {
		t.Fatal("facts still require opening extra records, or duplicated index")
	}
	report := raw(`{"markdown":"# Finding\nActual source [K-AAAAAAAAAAAA].","claimSummary":[],"methodSummary":"bounded","limitations":["overlap unknown"],"confidence":"low"}`)
	syntax, _, err := normalizeWorkflowAIStageSubmissionForInput(string(report), node, input)
	if err != nil || strings.Contains(string(syntax), bibliographyAppendixHeading) {
		t.Fatal("source rendering ran before citation restoration", err)
	}
	value, changes, err := enrichReportProvenance(syntax, input)
	if err != nil || len(changes) != 1 || changes[0].Rule != "append_frozen_research_records" {
		t.Fatal(changes, err)
	}
	text := decodeObject(value)["markdown"].(string)
	for _, want := range []string{screeningAppendixHeading, "wrong population", "SHUHAO LIN", "10.2337/db22-1418-p", "overlap"} {
		if !strings.Contains(string(value), want) {
			t.Fatal("missing fact", want)
		}
	}
	if strings.Contains(text, "547") || strings.Contains(text, "[K-BBBBBBBBBBBB]") {
		t.Fatal("unused or similar bibliography included", text)
	}
	if err := validateWorkflowAIStageOutput(node, value, input); err != nil {
		t.Fatal(err)
	}
	again, changes, err := enrichReportProvenance(value, input)
	if err != nil || len(changes) != 0 || !rawJSONEqual(value, again) {
		t.Fatal("not idempotent", err)
	}
	if !reflect.DeepEqual(before, mustJSON(detail)) {
		t.Fatal("frozen state mutated")
	}
	legacy, changes, err := enrichReportProvenance(report, stepInputWithoutHandoff(t, input))
	if err != nil || len(changes) != 0 || !rawJSONEqual(legacy, report) {
		t.Fatal("legacy snapshot changed", err)
	}
	if _, _, err := enrichReportProvenance(raw(strings.Replace(string(value), "wrong population", "tampered", 1)), input); err == nil {
		t.Fatal("altered frozen reasons silently replaced")
	}
}

func stepInputWithoutHandoff(t *testing.T, input json.RawMessage) json.RawMessage {
	t.Helper()
	fields := decodeObject(input)
	delete(fields, "researchHandoff")
	return mustJSON(fields)
}

func TestResearchHandoffReissuedCitationsMissingIdentityAndUntrustedText(t *testing.T) {
	detail, step, node := handoffFixture()
	step.Input = addResearchHandoff(detail, node, step.Input)
	reissued := citation.KnowledgeReference("current-chat", "index", "a", "quotehash")
	value := raw(`{"markdown":"Result ` + reissued + `.","claimSummary":[],"methodSummary":"bounded","limitations":[],"confidence":"low"}`)
	canonical := canonicalCitationSubmission(value, detail, step, node, "current-chat")
	result, _, err := enrichReportProvenance(canonical, step.Input)
	if err != nil || !strings.Contains(string(result), "10.2337/db22-1418-p") || strings.Contains(string(result), reissued) {
		t.Fatal("rendered wrong citation domain", err, string(result))
	}
	for _, status := range []string{"ambiguous_import", "missing_bibliography", "conflicting_identity"} {
		fields := decodeObject(step.Input)
		handoff := fields["researchHandoff"].(map[string]any)
		nav := handoff["navigation"].(map[string]any)
		binding := nav["bindings"].([]any)[0].(map[string]any)
		binding["status"] = status
		value, _, err := enrichReportProvenance(canonical, mustJSON(fields))
		if err != nil || !strings.Contains(decodeObject(value)["markdown"].(string), researchTableText(status)) || strings.Contains(string(value), "10.2337") {
			t.Fatal("missing identity guessed", status, err, string(value))
		}
	}
	grouped := raw(strings.Replace(string(canonical), "Result [K-AAAAAAAAAAAA].", "Result [K-AAAAAAAAAAAA] [K-BBBBBBBBBBBB].", 1))
	value, _, err = enrichReportProvenance(grouped, step.Input)
	if err != nil || strings.Count(decodeObject(value)["markdown"].(string), "10.2337/db22-1418-p") != 1 || !strings.Contains(decodeObject(value)["markdown"].(string), "| [K-AAAAAAAAAAAA] [K-BBBBBBBBBBBB] |") {
		t.Fatal("multiple excerpts lost or duplicated a bibliography", err)
	}
	detail.Steps[0].Output = raw(`{"evidenceManifest":[{"candidateId":"x|\n[K-CCCCCCCCCCCC]","decision":"exclude","reason":"<img src=x> | [K-DDDDDDDDDDDD] **reason** 独立审查曾要求"}]}`)
	input := addResearchHandoff(detail, node, step.Input)
	value, _, err = enrichReportProvenance(canonical, input)
	text := decodeObject(value)["markdown"].(string)
	if err != nil || strings.Contains(text, "<img") || strings.Contains(text, "[K-DDDDDDDDDDDD]") || strings.Contains(text, "**reason**") || !strings.Contains(text, "&#124;") {
		t.Fatal("source injected markdown/citation", err, text)
	}
	if _, _, err := enrichReportProvenance(value, input); err != nil {
		t.Fatal("escaped record broke idempotence", err)
	}
	if err := validateWorkflowAIStageOutput(node, value, input); err != nil {
		t.Fatal("stored untrusted reason mistaken for authored process commentary", err)
	}
}

func TestResearchHandoffExtremePromptIdentityIsStable(t *testing.T) {
	prompt := reviewScopeInstruction + reportConfidenceInstruction + researchHandoffInstruction + researchInputSharingInstruction + implementationDecisionInstruction + ImplementationPartsInstruction + ImplementationPatchInstruction + implementationEfficiencyInstruction + computationAuditInstruction + kernelReviewNavigationInstruction
	for _, id := range []string{"independent_review", "report_drafting", "method_implementation"} {
		node := CompiledNode{ID: id, PromptVersion: strings.Repeat("r", 128), OutputSchema: trackedResearchReviewSchema()}
		before := mustJSON(node)
		version := reportExecutionPromptVersion(node, prompt)
		if len(version) > 128 || !strings.Contains(version, "-rh1") && !strings.Contains(version, "-"+researchHandoffVersion) {
			t.Fatal("version cannot persist handoff identity", version)
		}
		if id == "method_implementation" && (!strings.Contains(version, "-"+chat.ImplementationPartsVersion) || !strings.Contains(version, "-"+chat.ImplementationPatchVersion)) {
			t.Fatal("lost implementation protocol identity", version)
		}
		node.PromptVersion = version
		if reportExecutionPromptVersion(node, prompt) != version {
			t.Fatal("version not idempotent", version)
		}
		node.PromptVersion = strings.Repeat("r", 128)
		if !rawJSONEqual(before, mustJSON(node)) {
			t.Fatal("frozen node changed")
		}
	}
}

func TestResearchHandoffPrecommitCompletionAndRecoveryUseIdenticalSources(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint("explicit_assembly_", explicit), func(t *testing.T) {
			testResearchHandoffCommitPaths(t, explicit)
		})
	}
}

func testResearchHandoffCommitPaths(t *testing.T, explicit bool) {
	detail, step, node := handoffFixture()
	step.Status, step.Attempt = StepRunning, 1
	step.Input = addResearchHandoff(detail, node, step.Input)
	if explicit {
		step.Input = bindReportComposition(detail, node, step.Input)
	}
	step.InputSHA256 = hashJSON(step.Input)
	node.OutputSchemaSHA256 = hashBytes(node.OutputSchema)
	detail.Run.ID, detail.Run.ProjectID, detail.Run.ConversationID, detail.Run.Status, detail.Run.CurrentStep = "run", "project", "conversation", RunRunning, step.Ordinal
	detail.Run.Inputs, detail.Run.InputsSHA256 = raw(`{}`), hashJSON(raw(`{}`))
	detail.Run.Compilation.Nodes = append(detail.Run.Compilation.Nodes, node)
	detail.Run.Compilation.CompilationSHA256 = ""
	compilation, _ := canonicalJSON(detail.Run.Compilation)
	detail.Run.CompilationSHA256 = hashBytes(compilation)
	detail.Run.Compilation.CompilationSHA256 = detail.Run.CompilationSHA256
	detail.Steps[len(detail.Steps)-1] = step
	reissued := citation.KnowledgeReference("report-chat", "index", "a", "quotehash")
	text := `{"markdown":"# Report\nSource ` + reissued + `.","claimSummary":[],"methodSummary":"Review","limitations":["Overlap unknown"],"confidence":"low"}`
	execution := AIExecution{WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ChatRunID: "report-chat", OutputText: text}
	detail.AIExecutions = []AIExecution{execution}
	before := mustJSON(detail)
	service := &RuntimeService{repository: &submissionValidationRepository{detail: detail}}
	precommit, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, text)
	if err != nil || !strings.Contains(string(precommit), "10.2337/db22-1418-p") || strings.Contains(string(precommit), reissued) {
		t.Fatal("live precommit failed canonical binding", err)
	}
	if !rawJSONEqual(before, mustJSON(detail)) {
		t.Fatal("precommit mutated source state")
	}
	var refs []tool.CitationRef
	_ = json.Unmarshal(mustJSON(decodeObject(step.Input)["evidenceContext"]), &refs)
	repository := &submissionRuntimeRepository{}
	complete := submissionRuntimeService(repository)
	complete.tools = tool.NewService(&handoffCitationCalls{calls: []tool.Call{{ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: refs}}}}, tool.JSONSchemaValidator{})
	if err := complete.completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil || repository.failure != "" {
		t.Fatal("final completion failed", err, repository.failure)
	}
	if !rawJSONEqual(precommit, repository.execution.Output) || repository.execution.OutputText != text {
		t.Fatal("precommit/final differ or raw text lost")
	}
	if err := complete.completeAIExecution(context.Background(), detail, step, node, repository.execution, false); err != nil || repository.failure != "" {
		t.Fatal("restart changed frozen derived appendix", err, repository.failure)
	}
}

type handoffCitationCalls struct {
	tool.Repository
	calls []tool.Call
}

func (f *handoffCitationCalls) ListBySubject(context.Context, tool.SubjectKind, string) ([]tool.Call, error) {
	return f.calls, nil
}

type handoffAdoptionRepository struct {
	submissionValidationRepository
	output   json.RawMessage
	decision HumanDecision
}

func (r *handoffAdoptionRepository) RecordDecision(_ context.Context, decision HumanDecision, output json.RawMessage, _ int, _ bool, _ json.RawMessage, _ time.Time, _ RuntimeEvent) error {
	r.output, r.decision = cloneRaw(output), decision
	return nil
}

type handoffAdoptionExecutor struct {
	AIStageExecutor
	state AIStageState
}

func (e handoffAdoptionExecutor) Latest(context.Context, string) (AIStageState, bool, error) {
	return e.state, true, nil
}

func TestResearchHandoffHumanAdoptionRestoresCitationBeforeBibliography(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint("explicit_assembly_", explicit), func(t *testing.T) {
			testResearchHandoffAdoptionPath(t, explicit)
		})
	}
}

func testResearchHandoffAdoptionPath(t *testing.T, explicit bool) {
	detail, step, node := handoffFixture()
	step.Status, step.Attempt = StepWaitingHumanConfirmation, 1
	step.Input = addResearchHandoff(detail, node, step.Input)
	if explicit {
		step.Input = bindReportComposition(detail, node, step.Input)
	}
	step.InputSHA256 = hashJSON(step.Input)
	node.OutputSchemaSHA256 = hashBytes(node.OutputSchema)
	detail.Run.ID, detail.Run.ProjectID, detail.Run.ConversationID = "run", "project", "conversation"
	detail.Run.Status, detail.Run.CurrentStep = RunWaitingHumanConfirmation, step.Ordinal
	detail.Run.Inputs, detail.Run.InputsSHA256 = raw(`{}`), hashJSON(raw(`{}`))
	detail.Run.Compilation.Nodes = append(detail.Run.Compilation.Nodes, node)
	detail.Run.Compilation.CompilationSHA256 = ""
	compilation, _ := canonicalJSON(detail.Run.Compilation)
	detail.Run.CompilationSHA256 = hashBytes(compilation)
	detail.Run.Compilation.CompilationSHA256 = detail.Run.CompilationSHA256
	detail.Steps[len(detail.Steps)-1] = step
	reissued := citation.KnowledgeReference("adoption-chat", "index", "a", "quotehash")
	text := `{"markdown":"# Adopted report\nSource ` + reissued + `.","claimSummary":[],"methodSummary":"Review","limitations":["Overlap unknown"],"confidence":"low"}`
	var refs []tool.CitationRef
	_ = json.Unmarshal(mustJSON(decodeObject(step.Input)["evidenceContext"]), &refs)
	r := &handoffAdoptionRepository{submissionValidationRepository: submissionValidationRepository{detail: detail}}
	s := &RuntimeService{
		repository: r, permissions: permission.NewEngine(nil),
		projects: fixedProjectLoader{value: project.Project{WorkspacePath: t.TempDir()}},
		tools:    tool.NewService(&handoffCitationCalls{calls: []tool.Call{{ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: refs}}}}, tool.JSONSchemaValidator{}),
		ai:       handoffAdoptionExecutor{state: AIStageState{Status: "completed", ChatRunID: "adoption-chat", Text: text}},
		now:      time.Now, newID: func() (string, error) { return "id", nil }, closed: true,
	}
	before := mustJSON(detail)
	if _, err := s.Decide(context.Background(), HumanDecisionCommand{ProjectID: "project", RunID: "run", StepID: step.ID, Approved: true}); err != nil {
		t.Fatal("original adoption path failed", err)
	}
	var adopted struct {
		Analysis json.RawMessage `json:"analysis"`
		Text     string          `json:"text"`
	}
	if json.Unmarshal(r.output, &adopted) != nil || adopted.Text != text || strings.Contains(string(adopted.Analysis), reissued) || !strings.Contains(string(adopted.Analysis), "[K-AAAAAAAAAAAA] | SHUHAO LIN") || !strings.Contains(string(adopted.Analysis), "10.2337/db22-1418-p") {
		t.Fatal("adopted result lost original text or canonical source binding", string(r.output))
	}
	if err := validateWorkflowAIStageOutput(node, adopted.Analysis, step.Input); err != nil {
		t.Fatal("adopted appendix differs from regular completion", err)
	}
	if !rawJSONEqual(before, mustJSON(detail)) || r.decision.Context == nil || !strings.Contains(string(r.decision.Context), hashJSON(adopted.Analysis)) {
		t.Fatal("adoption changed frozen records or lost output identity")
	}
}

func TestResearchHandoffDOIObservationsDoNotInventVerdictOrRewriteReport(t *testing.T) {
	detail, step, _ := handoffFixture()
	step.NodeID = "independent_review"
	detail.Steps[len(detail.Steps)-1] = step
	node := CompiledNode{ID: step.NodeID, OutputSchema: trackedResearchReviewSchema()}
	fields := decodeObject(step.Input)
	wrong := "| Ezpeleta 547-P | 10.2337/db22-547-p | [K-AAAAAAAAAAAA] |"
	fields["context"] = map[string]any{"markdown": wrong + "\nLin 10.2337/db22-1418-p [K-BBBBBBBBBBBB]."}
	input := addResearchHandoff(detail, node, mustJSON(fields))
	stage, _, ok := parseResearchHandoff(input)
	if !ok || len(stage.Handoff.Attribution) != 1 || stage.Handoff.Attribution[0].ReportLine != 1 || stage.Handoff.Attribution[0].ReportPointer != "/context/markdown" || stage.Handoff.Attribution[0].BoundDOI != "10.2337/db22-1418-p" || stage.Handoff.Attribution[0].WrittenDOIs[0] != "10.2337/db22-547-p" {
		t.Fatal("identity mismatch not delivered to reviewer", string(input))
	}
	if !rawJSONEqual(mustJSON(decodeObject(input)["context"]), mustJSON(fields["context"])) {
		t.Fatal("host rewrote scientific report")
	}
	if _, _, err := reviewBasisAt(decodeObject(input), "/researchHandoff/attributionObservations/0/reportLine"); err == nil {
		t.Fatal("host suspicion accepted as scientific evidence")
	}
	if text, _, err := reviewBasisAt(decodeObject(input), "/researchHandoff/excludedRecords/0/reason"); err != nil || text != "wrong population" {
		t.Fatal("original reason inaccessible", text, err)
	}
	// A contradiction observation never changes a review verdict or auto-picks
	// a repair. Scientific judgement still belongs to the reviewer.
	for _, upstreamError := range []bool{false, true} {
		d := recommendationFixture(t)
		mutateRecommendationReview(t, &d, func(output map[string]any) {
			target := "report_drafting"
			if upstreamError {
				target = "method_selection"
			}
			output["requiredCorrections"] = []string{"Correct the identified result"}
			output["revisionPlan"] = []researchRevisionPlanItem{{0, target, "Actual result defect", "Latest sufficient owner", false, "", nil}}
		})
		recommendation := researchRevisionRecommendation(d)
		wanted := "report_drafting"
		if upstreamError {
			wanted = "method_selection"
		}
		if recommendation == nil || recommendation.NodeID != wanted {
			t.Fatal("host guessed repair scope", recommendation)
		}
	}
}

func TestResearchHandoffReviewDependenciesDetectChangedScreeningWithoutTouchingLegacy(t *testing.T) {
	detail, step, _ := handoffFixture()
	step.NodeID = "independent_review"
	node := CompiledNode{ID: step.NodeID, OutputSchema: trackedResearchReviewSchema()}
	detail.Run.Compilation = Compilation{Nodes: []CompiledNode{node}}
	detail.Steps[len(detail.Steps)-1] = step
	fields := decodeObject(step.Input)
	fields["researchSourceContext"] = researchSourceContext(detail)
	step.Input = addResearchHandoff(detail, node, mustJSON(fields))
	before := mustJSON(detail)
	if err := validateReviewDependencies(detail, step); err != nil {
		t.Fatal("valid handoff dependency rejected", err)
	}
	if !rawJSONEqual(before, mustJSON(detail)) {
		t.Fatal("dependency check mutated history")
	}
	detail.Steps[0].Output = raw(strings.Replace(string(detail.Steps[0].Output), "wrong population", "changed exclusion reason", 1))
	if err := validateReviewDependencies(detail, step); err == nil {
		t.Fatal("stale screening audit handed to gate")
	}
	detail.Steps[0].Output = raw(strings.Replace(string(detail.Steps[0].Output), "changed exclusion reason", "wrong population", 1))
	// Loss of the only source cannot accidentally retain the old handoff when
	// no evidenceContext was bound in a report-only route.
	root := decodeObject(step.Input)
	delete(root, "evidenceContext")
	step.Input = addResearchHandoff(detail, node, mustJSON(root))
	detail.Steps[0].Status = StepQueued
	if err := validateReviewDependencies(detail, step); err == nil {
		t.Fatal("missing current screening kept stale handoff")
	}
	detail.Steps[0].Status = StepCompleted
	step.Input = stepInputWithoutHandoff(t, step.Input)
	if err := validateReviewDependencies(detail, step); err != nil {
		t.Fatal("legacy review gained new handoff check", err)
	}
}

func TestResearchStageReadExactStepIdentityRemainsSameTaskOnly(t *testing.T) {
	s, f := stageMemoryService()
	for _, section := range []string{"stage_input", "stage_output"} {
		value, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: section, NodeID: f.detail.Steps[0].ID})
		if err != nil || decodeObject(value)["nodeId"] != "implement" {
			t.Fatal("exact same-run step could not resolve", err)
		}
		if _, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: section, NodeID: "foreign-step-id"}); err == nil {
			t.Fatal("guessed foreign step")
		}
	}
}

// Scoped offline capture, never opens the production database or calls a model.
func TestCapturedResearchHandoffExclusionsConferenceBindingAndPrompt(t *testing.T) {
	dir := os.Getenv("SCIAIDE_HANDOFF_CAPTURE")
	if dir == "" {
		t.Skip("no task-scoped capture supplied")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var rows []map[string]json.RawMessage
	if json.Unmarshal(read("formal-before-workflow_runs.json"), &rows) != nil || len(rows) != 1 {
		t.Fatal("bad run capture")
	}
	str := func(v json.RawMessage) string {
		var s string
		if json.Unmarshal(v, &s) != nil {
			t.Fatal("invalid string")
		}
		return s
	}
	r := rows[0]
	detail := RunDetail{Run: Run{ID: str(r["id"]), ProjectID: str(r["project_id"]), ResearchTaskID: str(r["research_task_id"]), Inputs: raw(str(r["inputs_json"])), InputsSHA256: str(r["inputs_sha256"]), Status: RunFailed}}
	if json.Unmarshal([]byte(str(r["compilation_json"])), &detail.Run.Compilation) != nil {
		t.Fatal("bad compilation")
	}
	if json.Unmarshal(read("formal-before-workflow_steps.json"), &rows) != nil {
		t.Fatal("bad steps")
	}
	for _, row := range rows {
		s := Step{ID: str(row["id"]), NodeID: str(row["node_id"]), Status: StepStatus(str(row["status"])), Input: raw(str(row["input_json"])), InputSHA256: str(row["input_sha256"]), Output: raw(str(row["output_json"]))}
		_ = json.Unmarshal(row["ordinal"], &s.Ordinal)
		_ = json.Unmarshal(row["attempt"], &s.Attempt)
		detail.Steps = append(detail.Steps, s)
	}
	before := mustJSON(detail)
	for _, id := range []string{"report_drafting", "independent_review"} {
		step := *findStepByNode(detail.Steps, id)
		node := compilationNodeMap(detail.Run.Compilation)[id]
		input := addResearchHandoff(detail, node, step.Input)
		stage, nav, ok := parseResearchHandoff(input)
		if !ok || len(stage.Handoff.ExcludedRecords) != 13 || nav.Screening.RecordCount != 67 {
			t.Fatal("actual audit facts lost", id)
		}
		step.Input, step.InputSHA256 = input, hashJSON(input)
		prompt := buildAIStagePrompt(detail, step, node)
		if err := os.WriteFile(filepath.Join(dir, id+"-prompt-new.txt"), []byte(prompt), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+"-input-new.json"), input, 0600); err != nil {
			t.Fatal(err)
		}
		if id == "independent_review" {
			if len(stage.Handoff.Attribution) == 0 {
				t.Fatal("actual 547/1418 mismatch missed")
			}
			for _, o := range stage.Handoff.Attribution {
				t.Logf("observed DOI=%v bound=%s line=%d", o.WrittenDOIs, o.BoundDOI, o.ReportLine)
			}
		} else {
			// The original scientific body is retained verbatim, including the
			// mismatch. Only the source appendix is host-authored.
			report, err := outputPort(findStepByNode(detail.Steps, id).Output, "analysis")
			if err != nil {
				t.Fatal(err)
			}
			fields := decodeObject(report)
			body := fields["markdown"].(string)
			if at := strings.Index(body, retrievalAppendixHeading); at >= 0 {
				body = strings.TrimSpace(body[:at])
			}
			fields["markdown"] = body
			value, _, err := enrichReportProvenance(mustJSON(fields), input)
			if err != nil {
				t.Fatal(err)
			}
			text := decodeObject(value)["markdown"].(string)
			if !strings.HasPrefix(text, body+"\n\n") || !strings.Contains(text, "[K-EC9E79D69246] | SHUHAO LIN") || strings.Count(text, "保存的排除理由") != 1 {
				t.Fatal("capture source handoff wrong")
			}
			if err := os.WriteFile(filepath.Join(dir, "report-with-handoff.md"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s frozen input=%d bytes new=%d bytes; production prompt=%d chars exclusions=13, records=67", id, len(findStepByNode(detail.Steps, id).Input), len(input), len([]rune(prompt)))
	}
	// Capacity must cover the next review of the longer host-enriched report,
	// not only the first review before these appendices exist.
	review := *findStepByNode(detail.Steps, "independent_review")
	fields := decodeObject(review.Input)
	reportFields := fields["context"].(map[string]any)
	reportFields["markdown"] = string(read("report-with-handoff.md"))
	review.Input = addResearchHandoff(detail, compilationNodeMap(detail.Run.Compilation)[review.NodeID], mustJSON(fields))
	review.InputSHA256 = hashJSON(review.Input)
	prompt := buildAIStagePrompt(detail, review, compilationNodeMap(detail.Run.Compilation)[review.NodeID])
	if err := os.WriteFile(filepath.Join(dir, "independent_review-prompt-grown.txt"), []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("grown report review: input=%d bytes prompt=%d chars", len(review.Input), len([]rune(prompt)))
	if !reflect.DeepEqual(before, mustJSON(detail)) {
		t.Fatal("capture state mutated")
	}
}
