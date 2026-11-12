package workflow

import (
	"bytes"
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
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func sharingFixture(extra string) json.RawMessage {
	queries := []any{strings.Repeat("实际检索😀", 100), "methods and full-text scope NOT proven"}
	documents := []any{}
	for i := 0; i < 12; i++ {
		documents = append(documents, map[string]any{"id": i, "queries": queries, "fullTextRead": false})
	}
	return mustJSON(map[string]any{
		"researchSourceContext": map[string]any{"a~/b": queries, "documents": documents, "distinctQueries": []any{strings.Repeat("实际检索😀", 100), "different study"}},
		"context":               map[string]any{"markdown": "完整当前稿件" + extra}, "reviewHistory": map[string]any{"findings": []any{"不能丢弃历史审查"}}, "originalRequest": "保持目标不变",
	})
}

// Reconstruct the transport as specified to the model, without coercing number
// spellings. This decoder is test-only; host validators always use original.
func expandSharingForTest(t *testing.T, raw json.RawMessage, instruction ...string) any {
	t.Helper()
	decode := func(raw json.RawMessage) any {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	root := decode(raw).(map[string]any)
	paths := []string{}
	if len(instruction) > 0 && instruction[0] != "" {
		encoded := strings.Split(strings.Split(instruction[0], "<stage_input_shared_paths>\n")[1], "\n</stage_input_shared_paths>")[0]
		if err := json.Unmarshal([]byte(encoded), &paths); err != nil {
			t.Fatal(err)
		}
	}
	var walk func(any) any
	walk = func(v any) any {
		switch value := v.(type) {
		case map[string]any:
			if id, ok := value[researchInputSameAsKey].(json.Number); ok {
				n, err := id.Int64()
				if err != nil || n < 0 || n >= int64(len(paths)) {
					t.Fatal("bad shared index", value)
				}
				target := paths[n]
				if len(value) != 1 || !strings.HasPrefix(target, "/researchSourceContext/") {
					t.Fatal("reference escaped its host source scope", value)
				}
				resolved, _, err := reviewBasisAt(root, target)
				if err != nil || researchInputContainsSharingKey(mustJSON(resolved)) {
					t.Fatal("non-literal target/chain", target, err)
				}
				return decode(mustJSON(resolved))
			}
			copy := map[string]any{}
			for k, w := range value {
				copy[k] = walk(w)
			}
			return copy
		case []any:
			copy := make([]any, len(value))
			for i, w := range value {
				copy[i] = walk(w)
			}
			return copy
		default:
			return v
		}
	}
	return walk(root)
}

func TestResearchInputSharingIsLosslessScopedAndDeterministic(t *testing.T) {
	input := sharingFixture("")
	before := append(json.RawMessage(nil), input...)
	projected, instruction := researchInputSharedProjection(input)
	if instruction == "" || len(projected) >= len(input) || !strings.Contains(instruction, "/researchSourceContext/a~0~1b") {
		t.Fatal("no useful exact shared query projection")
	}
	var expected any
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	_ = d.Decode(&expected)
	if !reflect.DeepEqual(expected, expandSharingForTest(t, projected, instruction)) || !bytes.Equal(before, input) {
		t.Fatal("transport changed original evidence/order/numbers")
	}
	var original, wire map[string]json.RawMessage
	_ = json.Unmarshal(input, &original)
	_ = json.Unmarshal(projected, &wire)
	for _, k := range []string{"context", "reviewHistory", "originalRequest"} {
		if !bytes.Equal(original[k], wire[k]) {
			t.Fatal("changed non-source field", k)
		}
	}
	for i := 0; i < 10; i++ {
		next, nextInstruction := researchInputSharedProjection(input)
		if !bytes.Equal(projected, next) || nextInstruction != instruction {
			t.Fatal("nondeterministic projection")
		}
	}
}

func TestResearchInputSharingDoesNotInterpretUntrustedMarkersOrSmallInputs(t *testing.T) {
	for _, input := range []json.RawMessage{raw(`{}`), raw(`{"context":"no source"}`), raw(`{"researchSourceContext":{"repeated":["tiny","tiny"]}}`), raw(`{"researchSourceContext":null}`), raw(`not json`), bytes.Replace(sharingFixture(""), []byte(`"originalRequest":"保持目标不变"`), []byte(`"originalRequest":{"\u0024same":"/researchSourceContext"}`), 1)} {
		projected, instruction := researchInputSharedProjection(input)
		if instruction != "" || !bytes.Equal(projected, input) {
			t.Fatal("enabled ambiguous/unprofitable sharing", string(input))
		}
	}
}

func TestResearchInputSharingDoesNotMergeUnequalNumbersOrSourceClaims(t *testing.T) {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(sharingFixture(""), &input)
	var source map[string]json.RawMessage
	_ = json.Unmarshal(input["researchSourceContext"], &source)
	padding := strings.Repeat("x", 500)
	source["precise"] = raw(`[{"number":9007199254740993,"unit":"` + padding + `"},{"number":9007199254740992,"unit":"` + padding + `"},{"number":1.00,"unit":"` + padding + `"},{"number":1,"unit":"` + padding + `"}]`)
	input["researchSourceContext"] = mustJSON(source)
	original := mustJSON(input)
	projected, instruction := researchInputSharedProjection(original)
	if !reflect.DeepEqual(expandSharingForTest(t, original), expandSharingForTest(t, projected, instruction)) {
		t.Fatal("number precision/scope changed")
	}
}

func TestResearchInputSharingAmortizesShortValuesAndKeepsLiteralArrayTargets(t *testing.T) {
	// A frequently repeated 36-character identifier repays one path table;
	// one pair of short strings below a huge field name must not be shared.
	identifier := "11111111-2222-3333-4444-555555555555"
	queries := []string{strings.Repeat("same complete query ", 40), "query range remains limited"}
	entries := []any{}
	for i := 0; i < 100; i++ {
		entries = append(entries, map[string]any{"id": i, "projectId": identifier, "queries": queries})
	}
	input := mustJSON(map[string]any{"researchSourceContext": map[string]any{"a": entries, strings.Repeat("long-field-", 100): []any{identifier + "-unique", identifier + "-unique"}}})
	projected, instruction := researchInputSharedProjection(input)
	if instruction == "" || !strings.Contains(instruction, "/researchSourceContext/a/0/projectId") || !strings.Contains(instruction, "/researchSourceContext/a/0/queries") || strings.Contains(instruction, "/queries/0") || strings.Contains(instruction, "long-field-") {
		t.Fatal("failed amortization or encoded a shared target's child", instruction)
	}
	if !reflect.DeepEqual(expandSharingForTest(t, input), expandSharingForTest(t, projected, instruction)) {
		t.Fatal("exact IDs/queries not reconstructed")
	}
}

func TestResearchInputSharingDoesNotMakeNavigationOrInventedQuoteReviewEvidence(t *testing.T) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	stage["researchSourceContext"] = decodeObject(sharingFixture(""))["researchSourceContext"]
	input = mustJSON(stage)
	_, instruction := researchInputSharedProjection(input)
	if instruction == "" {
		t.Fatal("source sharing fixture not active")
	}
	findings := output["reviewFindings"].([]trackedReviewFinding)
	findings[0].Basis = append(findings[0].Basis, reviewBasis{"/researchSourceContext/documents/5/queries/1", "methods and full-text scope NOT proven"})
	output["reviewFindings"] = findings
	if err := validateTrackedReview(mustJSON(output), input); err != nil {
		t.Fatal("valid rejection with original shared scalar lost", err)
	}
	for _, basis := range []reviewBasis{{"/researchSourceContext/documents/5/queries/1", "invented whole paper reading"}, {"/researchSourceContext/documents/5/queries/$same", "0"}, {"/stage_input_shared_paths/0", "/researchSourceContext"}} {
		findings[0].Basis = []reviewBasis{basis}
		output["reviewFindings"] = findings
		if validateTrackedReview(mustJSON(output), input) == nil {
			t.Fatal("sharing navigation or invented scientific assertion accepted", basis)
		}
	}
}

func TestSharingPromptKeepsFrozenIdentityAndOriginalBasis(t *testing.T) {
	input := sharingFixture("")
	step := Step{NodeID: "independent_review", Input: input, InputSHA256: hashJSON(input)}
	node := CompiledNode{ID: step.NodeID, PromptVersion: "sharing-fixture-v1", OutputSchema: trackedResearchReviewSchema()}
	prompt := buildAIStagePrompt(RunDetail{}, step, node)
	if !strings.Contains(prompt, researchInputSharingInstruction) || !strings.Contains(prompt, step.InputSHA256) || !strings.HasSuffix(reportExecutionPromptVersion(node, prompt), "-"+researchInputSharingVersion) {
		t.Fatal("projection lost original review hash/effective prompt identity")
	}
	value, _, err := reviewBasisAt(decodeObject(input), "/researchSourceContext/documents/5/queries/1")
	if err != nil || value != "methods and full-text scope NOT proven" || hashJSON(step.Input) != step.InputSHA256 {
		t.Fatal("original scientific basis changed", value, err)
	}
	for _, base := range []string{"long-" + strings.Repeat("x", 400), strings.Repeat("研", 100)} {
		node.PromptVersion = base
		v := reportExecutionPromptVersion(node, prompt)
		node.PromptVersion = v
		if len(v) > 128 || reportExecutionPromptVersion(node, prompt) != v {
			t.Fatal("long identity not bounded/idempotent", v)
		}
	}
	// The extreme mixed guidance combination already requires compact aliases;
	// adding sharing must preserve required transport markers and idempotence.
	node.ID, node.PromptVersion = "method_implementation", strings.Repeat("p", 100)
	allGuidance := prompt + implementationDecisionInstruction + ImplementationPartsInstruction + ImplementationPatchInstruction + implementationEfficiencyInstruction + computationAuditInstruction + kernelReviewNavigationInstruction
	v := reportExecutionPromptVersion(node, allGuidance)
	node.PromptVersion = v
	if len(v) > 128 || reportExecutionPromptVersion(node, allGuidance) != v || !strings.Contains(v, "-ss1") || !strings.Contains(v, "-"+chat.ImplementationPartsVersion) || !strings.Contains(v, "-"+chat.ImplementationPatchVersion) {
		t.Fatal("mixed guidance identity lost required marker/bound", v)
	}
}

func TestSharedInputTaskReaderKeepsAllOriginalPagesAndHash(t *testing.T) {
	s, f := stageMemoryService()
	f.detail.Steps[0].Input = sharingFixture("\r\n完整正文😀")
	f.detail.Steps[0].InputSHA256 = hashJSON(f.detail.Steps[0].Input)
	original := append(json.RawMessage(nil), f.detail.Steps[0].Input...)
	projected, instruction := researchInputSharedProjection(original)
	if instruction == "" || bytes.Equal(projected, original) {
		t.Fatal("fixture did not share source metadata")
	}
	command := ResearchTaskReadCommand{Section: "stage_input"}
	var content strings.Builder
	pages := 0
	for {
		raw, err := s.ReadResearchTask(context.Background(), "current-chat", command)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Content string `json:"content"`
			Hash    string `json:"sha256"`
			Next    int    `json:"nextOffset"`
			More    bool   `json:"hasMore"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Hash != hashJSON(original) || page.Next <= command.Offset {
			t.Fatal("wrong frozen page/hash", string(raw))
		}
		content.WriteString(page.Content)
		pages++
		if !page.More {
			break
		}
		command.Offset = page.Next
	}
	if content.String() != string(original) || pages < 2 || strings.Contains(content.String(), researchInputSameAsKey) {
		t.Fatal("task reader lost original data", pages)
	}
	if _, err := s.ReadResearchTask(context.Background(), "foreign-chat", command); err == nil {
		t.Fatal("source sharing widened task access")
	}
}

func TestCapturedRepairedReviewSharesSourcesWithoutLosingHistory(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REPAIRED_REVIEW_CAPTURE")
	if dir == "" {
		t.Skip("no scoped repaired-review incident capture")
	}
	read := func(name string) []byte {
		v, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	original := read("prompt-original.txt")
	start := bytes.Index(original, []byte("\n<stage_input>\n")) + len("\n<stage_input>\n")
	end := bytes.Index(original[start:], []byte("\n</stage_input>")) + start
	input := original[start:end]
	projected, instruction := researchInputSharedProjection(input)
	if instruction == "" || len([]rune(string(original))) != 236841 || len(input)-len(projected) < 12000 {
		t.Fatal("original incident/repeated source catalog changed", len([]rune(string(original))), len(input)-len(projected), len(instruction))
	}
	if !reflect.DeepEqual(expandSharingForTest(t, input), expandSharingForTest(t, projected, instruction)) {
		t.Fatal("lost original source facts")
	}
	prompt := string(original[:start]) + string(projected) + string(original[end:])
	prompt = strings.Replace(prompt, "\n<stage_input>\n", instruction+"\n<stage_input>\n", 1)
	for _, text := range []string{"reviewHistory", "_reviewCorrections", "allKnownCorrections", "review-scope-v4", "evidence-navigation-v1"} {
		if !strings.Contains(prompt, text) {
			t.Fatal("lost current history/scope", text)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt-shared.txt"), []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured original prompt=%d shared prompt=%d; complete sources/history reconstructed losslessly", len([]rune(string(original))), len([]rune(prompt)))
}

// Build a new execution through the production prompt builder, rather than
// only replacing stage_input inside the previously frozen prompt. Source
// markers in the captured execution were legitimately reissued by its bridge;
// this pre-bridge build uses the original frozen markers and input SHA.
func TestCapturedRepairedReviewProductionPromptPreservesOriginalIdentity(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REPAIRED_REVIEW_CAPTURE")
	if dir == "" {
		t.Skip("no scoped repaired-review incident capture")
	}
	b, err := os.ReadFile(filepath.Join(dir, "formal-state-before.json"))
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Run   []map[string]json.RawMessage `json:"run"`
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	if json.Unmarshal(b, &captured) != nil || len(captured.Run) != 1 || len(captured.Steps) != 15 {
		t.Fatal("capture identity changed")
	}
	str := func(v json.RawMessage) string {
		var s string
		if json.Unmarshal(v, &s) != nil {
			t.Fatal("invalid captured string", string(v))
		}
		return s
	}
	r := captured.Run[0]
	detail := RunDetail{Run: Run{ID: str(r["id"]), ProjectID: str(r["project_id"]), ResearchTaskID: str(r["research_task_id"]), Status: RunStatus(str(r["status"])), Inputs: raw(str(r["inputs_json"])), InputsSHA256: str(r["inputs_sha256"]), CompilationSHA256: str(r["compilation_sha256"])}}
	_ = json.Unmarshal(r["current_step_ordinal"], &detail.Run.CurrentStep)
	if err := json.Unmarshal([]byte(str(r["compilation_json"])), &detail.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	var current Step
	for _, s := range captured.Steps {
		step := Step{ID: str(s["id"]), WorkflowRunID: str(s["workflow_run_id"]), NodeID: str(s["node_id"]), NodeKind: NodeKind(str(s["node_kind"])), Status: StepStatus(str(s["status"])), Input: raw(str(s["input_json"])), InputSHA256: str(s["input_sha256"]), Output: raw(str(s["output_json"]))}
		_ = json.Unmarshal(s["ordinal"], &step.Ordinal)
		_ = json.Unmarshal(s["attempt"], &step.Attempt)
		if string(s["completed_at"]) != "null" {
			when, err := time.Parse(time.RFC3339Nano, str(s["completed_at"]))
			if err != nil {
				t.Fatal(err)
			}
			step.CompletedAt = &when
		}
		detail.Steps = append(detail.Steps, step)
		if step.NodeID == "independent_review" {
			current = step
		}
	}
	if current.ID != "86f3bde8-ba81-408f-99ca-95550933e789" || hashJSON(current.Input) != current.InputSHA256 {
		t.Fatal("original stage hash/identity changed")
	}
	node := compilationNodeMap(detail.Run.Compilation)[current.NodeID]
	before, _ := json.Marshal(detail)
	prompt := buildAIStagePrompt(detail, current, node)
	start := strings.LastIndex(prompt, "\n<stage_input>\n") + len("\n<stage_input>\n")
	end := strings.Index(prompt[start:], "\n</stage_input>") + start
	if !reflect.DeepEqual(expandSharingForTest(t, current.Input), expandSharingForTest(t, raw(prompt[start:end]), prompt)) || !strings.Contains(prompt, current.InputSHA256) {
		t.Fatal("new production prompt changed evidence/history/original SHA")
	}
	after, _ := json.Marshal(detail)
	if !bytes.Equal(before, after) || !strings.Contains(reportExecutionPromptVersion(node, prompt), researchInputSharingVersion) {
		t.Fatal("new prompt changed stored task or lost encoding identity")
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt-built.txt"), []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	// Validate the existing explicit "retry current stage" operation with the
	// captured frozen compilation/input. Launch is disabled and the fake store
	// records only this review ID: no formal database or report rerun.
	retryRepo := &sharingCapturedRetryRepository{stageMemoryFixture: stageMemoryFixture{detail: detail}}
	retry := &RuntimeService{repository: retryRepo, projects: fixedProjectLoader{value: project.Project{WorkspacePath: t.TempDir()}}, permissions: permission.NewEngine(nil), tools: tool.NewService(&indexedCallsFixture{}, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "captured-sharing-retry", nil }, closed: true}
	if _, err := retry.Retry(context.Background(), RetryCommand{ProjectID: detail.Run.ProjectID, RunID: detail.Run.ID, StepID: current.ID}); err != nil || retryRepo.resetID != current.ID {
		t.Fatal("current failed review cannot be retried independently", err, retryRepo.resetID)
	}
	// Reconstruct first-turn lifecycle guidance from the same captured record;
	// no Start/Retry, database or model request. This tests the production
	// guidance builder, not a guessed number of fixed-context characters.
	executing := detail
	executing.Run.Status, executing.Run.CurrentStep = RunRunning, current.Ordinal
	for i := range executing.Steps {
		if executing.Steps[i].ID == current.ID {
			executing.Steps[i].Status = StepRunning
		}
	}
	repository := &sharingCapturedGuidanceRepository{stageMemoryFixture: stageMemoryFixture{detail: executing}}
	guidance, found, err := (&RuntimeService{repository: repository}).GuidanceForConversation(context.Background(), "captured-task-conversation")
	if err != nil || !found || guidance.ExecutionSystemContext == "" || guidance.ExecutionDynamicState == "" {
		t.Fatal("production execution guidance unavailable", err)
	}
	// These execution-only fields intentionally have json:"-" on the public
	// view. Capture them explicitly for the local admission replay, not via
	// the presentation JSON that would silently omit the fixed guidance.
	executionGuidance := map[string]any{"executionSystemContext": guidance.ExecutionSystemContext, "executionDynamicState": guidance.ExecutionDynamicState, "executionTaskReadSchema": guidance.ExecutionTaskReadSchema, "skillDiscovery": guidance.SkillDiscovery}
	if err := os.WriteFile(filepath.Join(dir, "guidance-built.json"), mustJSON(executionGuidance), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("production pre-bridge prompt=%d chars; original stage input/SHA and all 15 steps unchanged", len([]rune(prompt)))
}

type sharingCapturedGuidanceRepository struct{ stageMemoryFixture }

func (f *sharingCapturedGuidanceRepository) GetRunByConversation(context.Context, string) (RunDetail, bool, error) {
	return f.detail, true, nil
}

type sharingCapturedRetryRepository struct {
	stageMemoryFixture
	resetID string
}

func (f *sharingCapturedRetryRepository) ResetStepForRetry(_ context.Context, runID, stepID string, decision *HumanDecision, _ time.Time, event RuntimeEvent, _ RetryGuard) error {
	if runID != f.detail.Run.ID || decision != nil || event.Type != "workflow.step_retry_queued" {
		return fmt.Errorf("unexpected retry scope")
	}
	f.resetID = stepID
	return nil
}
