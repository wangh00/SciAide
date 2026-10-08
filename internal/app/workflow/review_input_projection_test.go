package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestIndexedAcceptedPrettyJSONRetainsExactAuditAndUncitedRanges(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	step.Input = raw(`{"documentIds":["doc"],"candidates":[],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
	step.InputSHA256, e.InputSHA256 = hashJSON(step.Input), hashJSON(step.Input)
	d.Steps[2] = step
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(indexedSynthesis(calls.calls[0].Result.Citations[0]), selectedEvidencePhaseNode(node, step.Input), step.Input)
	if err != nil {
		t.Fatal(err)
	}
	var pretty strings.Builder
	// Models commonly submit indented JSON, which RawMessage embedding compacts.
	var obj any
	_ = json.Unmarshal(value, &obj)
	obj.(map[string]any)["summary"] = "Literal <method> & citation " + calls.calls[0].Result.Citations[0].Reference
	enc := json.NewEncoder(&pretty)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(obj); err != nil {
		t.Fatal(err)
	}
	e.OutputText = pretty.String()
	unused := calls.calls[0].Result.Citations[0]
	unused.Locator = "page:3"
	// Same snapshot identity can only describe its actual source; give the
	// uncited observed range its own properly signed original chunk.
	unused.ChunkID = "unused"
	unused.Reference = citation.KnowledgeReference(e.ChatRunID, unused.IndexVersionID, unused.ChunkID, unused.QuoteSHA256)
	calls.calls[0].Result.Citations = append(calls.calls[0].Result.Citations, unused)
	repo := &indexedAcceptedFixture{}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err != nil || repo.failure != "" || len(repo.repairs) != 0 || repo.queued {
		t.Fatal("pretty model synthesis not accepted", err, repo.failure)
	}
	d.AIExecutions, d.Events = []AIExecution{repo.execution}, []RuntimeEvent{repo.completedEvent}
	step.Status, step.Output = StepCompleted, repo.output
	d.Steps[2] = step
	analysis, _ := outputPort(step.Output, "analysis")
	if hashJSON(analysis) == repo.execution.OutputSHA256 {
		t.Fatal("fixture failed to exercise RawMessage projection")
	}
	refs, sourceHash, ok := acceptedIndexedEvidence(d, step)
	if !ok || len(refs) != 1 || sourceHash != repo.execution.OutputSHA256 || len(indexedReadRanges(d)) != 2 {
		t.Fatal("embedding lost accepted evidence/ranges", len(refs), sourceHash, ok)
	}
	for _, id := range []string{"method_selection", "report_drafting", "independent_review"} {
		input := addResearchSourceContext(d, CompiledNode{ID: id}, raw(`{}`))
		gap := evidenceGap{Field: "bounded gap", CheckedScope: []evidenceCheckedSource{{DocumentID: "doc", Locators: []string{"page:3"}}}, Assessment: "Only returned blocks were checked"}
		if err := validateCheckedEvidenceScope(gap, input); err != nil {
			t.Fatal(id, err)
		}
	}
	// Persisted restart and real byte hashes remain authoritative.
	executionJSON := string(d.AIExecutions[0].Output)
	if err := json.Unmarshal(mustJSON(d), &d); err != nil {
		t.Fatal(err)
	}
	// SQLite stores execution.output_json as its original string, independently
	// of step.output_json; serializing a transport RunDetail is not DB recovery.
	d.AIExecutions[0].Output = raw(executionJSON)
	if _, _, ok := acceptedIndexedEvidence(d, d.Steps[2]); !ok {
		t.Fatal("restart lost pretty evidence")
	}
	for _, change := range []func(*RunDetail){
		func(d *RunDetail) { d.AIExecutions[0].OutputSHA256 = "forged" },
		func(d *RunDetail) { d.AIExecutions[0].InputSHA256 = "other-input" },
		func(d *RunDetail) { d.AIExecutions[0].Attempt++ },
		func(d *RunDetail) {
			v := decodeObject(d.Steps[2].Output)
			a := decodeObject(mustJSON(v["analysis"]))
			a["summary"] = "modified scientific result"
			v["analysis"] = a
			d.Steps[2].Output = mustJSON(v)
		},
		func(d *RunDetail) { d.Events = nil },
	} {
		var bad RunDetail
		_ = json.Unmarshal(mustJSON(d), &bad)
		bad.AIExecutions[0].Output = raw(executionJSON)
		if _, _, ok := acceptedIndexedEvidence(bad, bad.Steps[2]); !ok {
			t.Fatal("tamper baseline invalid")
		}
		change(&bad)
		if _, _, ok := acceptedIndexedEvidence(bad, bad.Steps[2]); ok {
			t.Fatal("tampering accepted")
		}
	}
	// Human adoption uses the verified original execution hash, not the
	// compact step analysis hash, and binds the new result to its own decision.
	v := decodeObject(d.Steps[2].Output)
	a := decodeObject(mustJSON(v["analysis"]))
	a["summary"] = "User adopts a revised bounded finding"
	v["analysis"], v["evidenceSourceOutputSha256"] = a, sourceHash
	d.Steps[2].Output = mustJSON(v)
	adopted, _ := outputPort(d.Steps[2].Output, "analysis")
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.human_decided", Payload: mustJSON(map[string]any{"stepId": step.ID, "approved": true, "outputSha256": hashJSON(adopted), "evidenceSourceOutputSha256": sourceHash, "evidenceCandidatesSha256": hashJSON(mustJSON(refs))})})
	if _, hash, ok := acceptedIndexedEvidence(d, d.Steps[2]); !ok || hash != sourceHash || len(indexedReadRanges(d)) != 2 {
		t.Fatal("manual adoption lost exact original hash/read ranges")
	}
}

func TestEmbeddedEvidenceProjectionDoesNotCoerceScientificValues(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"n":9007199254740992}`, `{"n":9007199254740993}`},
		{`{"n":1}`, `{"n":1.0}`},
		{`{"text":"literal"}`, `{"text":"changed"}`},
	} {
		if sameEmbeddedEvidenceOutput(raw(pair[0]), raw(pair[1])) {
			t.Fatal("scientific value coerced", pair)
		}
	}
	if !sameEmbeddedEvidenceOutput(raw("{\n  \"text\": \"<x>&\", \"n\": 9007199254740993\n}"), raw(`{"text":"\u003cx\u003e\u0026","n":9007199254740993}`)) {
		t.Fatal("Go RawMessage projection differs")
	}
}

func TestRejectedReviewCanReportFrozenUpstreamScopeDefect(t *testing.T) {
	input, output := acceptanceFixture(t)
	v := decodeObject(input)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	v["requireEvidenceGapCheck"], v["acceptanceCriteria"] = true, appendEvidenceGapCriterion(criteria)
	gap := evidenceGap{Field: "method", Status: "not_discussed_in_checked_scope", Required: false, CheckedScope: []evidenceCheckedSource{{DocumentID: "doc", Locators: []string{"page:4"}}}, Assessment: "Unproven old range declaration"}
	v["evidenceScreening"] = map[string]any{"evidenceGaps": []evidenceGap{gap}}
	output["approved"], output["requiredCorrections"] = false, []string{"核验上游范围或将结论改为尚未核验"}
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "checked"}, {"criterion-2", "met", "checked"}, {"criterion-3", "not_met", "Upstream range has no recorded read"}}
	if err := validateResearchAcceptance(mustJSON(output), mustJSON(v)); err != nil {
		t.Fatal("rejection trapped by frozen upstream input", err)
	}
	output["acceptanceChecks"].([]ResearchAcceptanceCheck)[2].Status = "met"
	if validateResearchAcceptance(mustJSON(output), mustJSON(v)) == nil {
		t.Fatal("invalid upstream range called met")
	}
	output["approved"], output["requiredCorrections"] = true, []string{}
	if validateResearchAcceptance(mustJSON(output), mustJSON(v)) == nil {
		t.Fatal("unread upstream scope approved")
	}
	v["researchSourceContext"] = map[string]any{"indexedReadRanges": []map[string]any{{"documentId": "other", "locator": "page:4"}}}
	if validateResearchAcceptance(mustJSON(output), mustJSON(v)) == nil {
		t.Fatal("other document accepted as checked source")
	}
	v["researchSourceContext"] = map[string]any{"indexedReadRanges": []map[string]any{{"documentId": "doc", "locator": "page:4"}}}
	if err := validateResearchAcceptance(mustJSON(output), mustJSON(v)); err != nil {
		t.Fatal("actual bounded read ignored", err)
	}
}

func TestRejectedUpstreamDefectPersistsAndReachesDeliveryGate(t *testing.T) {
	input, output := acceptanceFixture(t)
	v := decodeObject(input)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	v["requireEvidenceGapCheck"], v["acceptanceCriteria"] = true, appendEvidenceGapCriterion(criteria)
	v["evidenceScreening"] = map[string]any{"evidenceGaps": []evidenceGap{{Field: "method", Status: "not_discussed_in_checked_scope", Required: true, CheckedScope: []evidenceCheckedSource{{DocumentID: "doc", Locators: []string{"page:4"}}}, Assessment: "Old upstream range missing actual read"}}}
	input = mustJSON(v)
	output["approved"], output["requiredCorrections"] = false, []string{"核验范围或明确尚未核验"}
	output["reviewedInputSha256"] = hashJSON(input)
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "checked"}, {"criterion-2", "met", "checked"}, {"criterion-3", "not_met", "Frozen upstream scope not verified"}}
	node := CompiledNode{ID: "independent_review", Kind: NodeAIAnalysis, OutputSchema: researchAcceptanceReviewSchema()}
	gate := CompiledNode{ID: "delivery_gate", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}}
	step := Step{ID: "review", NodeID: node.ID, Status: StepRunning, Attempt: 1, Input: input, InputSHA256: hashJSON(input)}
	d := RunDetail{Run: Run{ID: "fixture", Compilation: Compilation{Nodes: []CompiledNode{node, gate}, Edges: []Edge{{FromNode: node.ID, FromPort: "analysis", ToNode: gate.ID, ToPort: "review"}}}}, Steps: []Step{step, {ID: "gate", NodeID: gate.ID, Ordinal: 1, Status: StepQueued}}}
	repo := &indexedAcceptedFixture{}
	s := &RuntimeService{repository: repo, now: time.Now, newID: func() (string, error) { return "event", nil }}
	e := AIExecution{ID: "review-execution", WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, OutputText: string(mustJSON(output))}
	if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err != nil || len(repo.repairs) != 0 || repo.failure != "" || len(repo.output) == 0 {
		t.Fatal("valid rejection scheduled repair or was lost", err, repo.failure)
	}
	d.Steps[0].Status, d.Steps[0].Output = StepCompleted, repo.output
	bound, err := bindNodeInput(d, gate)
	if err != nil {
		t.Fatal("rejection cannot reach delivery gate", err)
	}
	var args struct {
		Subject json.RawMessage `json:"subject"`
		Review  struct {
			Approved    bool     `json:"approved"`
			Corrections []string `json:"requiredCorrections"`
		} `json:"review"`
	}
	_ = json.Unmarshal(bound, &args)
	if !rawJSONEqual(args.Subject, input) || args.Review.Approved || len(args.Review.Corrections) != 1 {
		t.Fatal("gate lost exact rejected context/corrections")
	}
}

// Opt-in task-scoped capture replay. Does not open the user's DB, run any
// model, adopt its old review or mutate the task. Private text stays untracked.
func TestCapturedReviewInputProjectionAndRejection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_INPUT_REPLAY")
	if dir == "" {
		t.Skip("no scoped capture supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Run                                       map[string]any
		Steps, Events, Executions, Calls, Results []map[string]any
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	str := func(r map[string]any, k string) string { s, _ := r[k].(string); return s }
	num := func(r map[string]any, k string) int { n, _ := r[k].(float64); return int(n) }
	d := RunDetail{Run: Run{ID: str(c.Run, "id"), ProjectID: str(c.Run, "project_id"), Inputs: raw(str(c.Run, "inputs_json")), ResearchTaskID: str(c.Run, "research_task_id")}}
	_ = json.Unmarshal([]byte(str(c.Run, "compilation_json")), &d.Run.Compilation)
	for _, r := range c.Steps {
		d.Steps = append(d.Steps, Step{ID: str(r, "id"), NodeID: str(r, "node_id"), Ordinal: num(r, "ordinal"), Attempt: num(r, "attempt"), Status: StepStatus(str(r, "status")), Input: raw(str(r, "input_json")), InputSHA256: str(r, "input_sha256"), Output: raw(str(r, "output_json"))})
	}
	for _, r := range c.Events {
		d.Events = append(d.Events, RuntimeEvent{Type: str(r, "event_type"), Payload: raw(str(r, "payload_json"))})
	}
	for _, r := range c.Executions {
		d.AIExecutions = append(d.AIExecutions, AIExecution{ID: str(r, "id"), WorkflowStepID: str(r, "workflow_step_id"), Attempt: num(r, "attempt"), ChatRunID: str(r, "chat_run_id"), Status: str(r, "status"), InputSHA256: str(r, "input_sha256"), Output: raw(str(r, "output_json")), OutputSHA256: str(r, "output_sha256"), OutputText: str(r, "output_text")})
	}
	results := map[string]map[string]any{}
	for _, r := range c.Results {
		results[str(r, "tool_call_id")] = r
	}
	calls := &indexedCallsFixture{}
	for _, r := range c.Calls {
		call := tool.Call{ID: str(r, "id"), RunID: str(r, "run_id"), ToolName: str(r, "tool_name"), Status: tool.CallStatus(str(r, "status"))}
		if r, ok := results[call.ID]; ok {
			call.Result = &tool.Result{Status: tool.ResultStatus(str(r, "status")), Structured: raw(str(r, "structured_json"))}
			if err := json.Unmarshal([]byte(str(r, "citations_json")), &call.Result.Citations); err != nil {
				t.Fatal(err)
			}
		}
		calls.calls = append(calls.calls, call)
	}
	screen := *findStepByNode(d.Steps, "evidence_screening")
	refs, sourceHash, ok := acceptedIndexedEvidence(d, screen)
	if !ok {
		t.Fatal("actual frozen synthesis/candidates still rejected")
	}
	analysis, _ := outputPort(screen.Output, "analysis")
	if hashJSON(analysis) == sourceHash {
		t.Fatal("capture does not reproduce byte/embedding hash difference")
	}
	s := &RuntimeService{tools: tool.NewService(calls, tool.JSONSchemaValidator{})}
	if _, err := s.verifiedIndexedEvidence(context.Background(), d); err != nil {
		t.Fatal("actual successful call audit", err)
	}
	review := *findStepByNode(d.Steps, "independent_review")
	var e AIExecution
	for _, execution := range d.AIExecutions {
		if execution.WorkflowStepID == review.ID && execution.Attempt == review.Attempt {
			e = execution
		}
	}
	node := compilationNodeMap(d.Run.Compilation)[review.NodeID]
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(e.OutputText, node, review.Input)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(node, value, review.Input); err != nil {
		t.Fatal("actual honest rejection still refused", err)
	}
	// Normal explicit retry rebinds source context without changing historical
	// review input or the user's accepted 15 citations.
	selected := cloneRaw(findStepByNode(d.Steps, "evidence_extraction").Output)
	before := string(review.Input)
	fresh, err := bindNodeInput(d, node)
	if err != nil {
		t.Fatal("retry input", err)
	}
	fresh = addResearchSourceContext(d, node, fresh)
	var gaps struct {
		Gaps []evidenceGap `json:"evidenceGaps"`
	}
	_ = json.Unmarshal(analysis, &gaps)
	for _, g := range gaps.Gaps {
		if g.Status == "not_discussed_in_checked_scope" {
			if err := validateCheckedEvidenceScope(g, fresh); err != nil {
				t.Fatal("actual checked page lost", err)
			}
		}
	}
	if before != string(review.Input) || string(selected) != string(findStepByNode(d.Steps, "evidence_extraction").Output) {
		t.Fatal("historical input or user selection changed")
	}
	// An old response must not be reused on a new snapshot (including review
	// issue IDs and old provenance accusations). A real retry needs a new model
	// assessment, but its rejection can be persisted against frozen defects.
	if validateWorkflowAIStageOutput(node, value, fresh) == nil {
		t.Fatal("old review accepted on a different input")
	}
	if err := validateResearchAcceptance(value, fresh); err != nil {
		t.Fatal("rebound acceptance assessment", err)
	}
	t.Logf("actual capture: %d frozen candidates; %d observed ranges; original rejection accepted; scoped gaps reproduced from successful calls; user selection unchanged", len(refs), len(indexedReadRanges(d)))
}
