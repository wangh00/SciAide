package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// The manually chosen spans are protocol replay inputs, not model output or
// scientific approval. The real capture remains outside the repository.
func TestCapturedBatchQuoteSubmissionReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_MULTI_QUOTE_REPLAY_DIR")
	if dir == "" {
		t.Skip("no multi-quote submission replay selected")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	input, schema := read("input.json"), read("schema.json")
	text := string(read("derived-batch-replay-result.json"))
	var refs []tool.CitationRef
	var identity struct {
		ChatRunID string `json:"chat_run_id"`
	}
	if json.Unmarshal(read("citation-seeds.json"), &refs) != nil || json.Unmarshal(read("identity.json"), &identity) != nil || identity.ChatRunID == "" {
		t.Fatal("invalid captured binding")
	}
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, ReviewPolicy: AIReviewAuto, PromptVersion: selectedEvidenceVersion, OutputSchema: schema, OutputSchemaSHA256: hashJSON(schema)}
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(text, node, input)
	if err != nil {
		t.Fatal(err)
	}
	canonical := canonicalSupportingQuotes(restoreWorkflowCitationMarkers(value, refs, identity.ChatRunID), input)
	if err := (tool.JSONSchemaValidator{}).Validate(schema, canonical); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(node, canonical, input); err != nil {
		t.Fatal(err)
	}
	// Use the original frozen search output to bind compact coverage candidates.
	service, repo, _ := submissionValidationFixture(t)
	step := &repo.detail.Steps[4]
	step.NodeID, step.Input, step.InputSHA256 = node.ID, input, hashJSON(input)
	repo.detail.Run.Compilation.Nodes = []CompiledNode{{ID: "search", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, node}
	repo.detail.Run.Compilation.Edges = []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}
	repo.detail.Steps[0] = Step{NodeID: "search", Status: StepCompleted, Output: read("search-output.json")}
	repo.detail.AIExecutions = []AIExecution{{WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ChatRunID: identity.ChatRunID}}
	repo.detail.Run.Compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(repo.detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	repo.detail.Run.CompilationSHA256 = hashBytes(encoded)
	repo.detail.Run.Compilation.CompilationSHA256 = repo.detail.Run.CompilationSHA256
	before, _ := json.Marshal(repo.detail)
	// The uncorrected final candidate must still reproduce both real issues.
	_, err = service.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, string(read("derived-merged-turn-3.json")))
	var issues *CitationQuoteErrors
	if !errors.As(err, &issues) || len(issues.Issues) != 2 {
		t.Fatalf("original failure no longer reproduced: %v", err)
	}
	accepted, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, text)
	if err != nil || !rawJSONEqual(accepted, canonical) {
		t.Fatalf("read-only submission replay: %v", err)
	}
	after, _ := json.Marshal(repo.detail)
	if !bytes.Equal(before, after) {
		t.Fatal("submission replay mutated frozen state")
	}
	callRepo := &quoteReplayCalls{calls: []tool.Call{{ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: refs}}}}
	commitRepo := &submissionRuntimeRepository{}
	commitService := submissionRuntimeService(commitRepo)
	commitService.tools = tool.NewService(callRepo, tool.JSONSchemaValidator{})
	execution := repo.detail.AIExecutions[0]
	execution.OutputText = text
	if err := commitService.completeAIExecution(context.Background(), repo.detail, *step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if commitRepo.failure != "" || len(commitRepo.repairs) != 0 || commitRepo.execution.Status != "completed" || !rawJSONEqual(commitRepo.execution.Output, canonical) || commitRepo.execution.OutputText != text {
		t.Fatal("commit rejected merged replay, queued a retry or lost draft")
	}
	recoveryRepo := &submissionRuntimeRepository{}
	recoveryService := submissionRuntimeService(recoveryRepo)
	recoveryService.tools = tool.NewService(callRepo, tool.JSONSchemaValidator{})
	if err := recoveryService.projectFinishedAIExecution(context.Background(), repo.detail, *step, node, commitRepo.execution); err != nil {
		t.Fatal(err)
	}
	if recoveryRepo.failure != "" || len(recoveryRepo.repairs) != 0 || !rawJSONEqual(recoveryRepo.output, commitRepo.output) {
		t.Fatal("restart projection changed or rejected the replay")
	}
	if err := os.WriteFile(filepath.Join(dir, "derived-batch-canonical-submission.json"), accepted, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("manually selected two spans pass full frozen schema, live read-only submission, isolated commit and restart projection; original two issues still rejected; no real model or user DB writes")
}

func TestSelectedEvidenceCollectsAllQuoteIssuesWithoutAcceptingOmissions(t *testing.T) {
	refs := []tool.CitationRef{
		{Reference: "[K-A]", Quote: "First result. Intervening qualification. Last result."},
		{Reference: "[K-B]", Quote: "TITLE: Fixture\r\nEXTRACTION: preserved line\r\nDERIVED: not an independent study."},
		{Reference: "[K-C]", Quote: "Two variables x\r\n1 and x 2 carry information."},
		{Reference: "[K-D]", Quote: "Correct continuous evidence."},
	}
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": refs})
	output := mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{"[K-A]", "[K-B]", "[K-C]", "[K-D]"}, "citationAssessments": []map[string]any{
		{"reference": "[K-A]", "decision": "support", "supportingQuote": "First result. ... Last result."},
		{"reference": "[K-B]", "decision": "support", "supportingQuote": "TITLE: Fixture\nDERIVED: not an independent study."},
		{"reference": "[K-C]", "decision": "support", "supportingQuote": "Two variables x1 and x 2 carry information."},
		{"reference": "[K-D]", "decision": "support", "supportingQuote": refs[3].Quote},
	}})
	var issues *CitationQuoteErrors
	err := validateSelectedEvidence(canonicalSupportingQuotes(output, input), input)
	if !errors.As(err, &issues) || len(issues.Issues) != 3 {
		t.Fatalf("did not expose all rejected quotes: %v", err)
	}
	var first *CitationQuoteError
	if !errors.As(err, &first) || first.Reference != "[K-A]" {
		t.Fatal("lost single-issue compatibility")
	}
	for i := range issues.Issues {
		if issues.Issues[i].Reference != refs[i].Reference || issues.Issues[i].CandidateQuote != refs[i].Quote {
			t.Fatal("changed issue order, binding or source text")
		}
	}
	for i := range refs {
		refs[i].IndexVersionID, refs[i].ChunkID = "index", refs[i].Reference
		refs[i].QuoteSHA256 = citation.QuoteSHA256(refs[i].Quote)
	}
	visible := modelVisibleQuoteErrors(issues, refs, "chat")
	for i := range visible.Issues {
		if visible.Issues[i].Reference != citation.KnowledgeReference("chat", refs[i].IndexVersionID, refs[i].ChunkID, refs[i].QuoteSHA256) || visible.Issues[i].CandidateQuote != refs[i].Quote || issues.Issues[i].Reference != refs[i].Reference {
			t.Fatal("batch diagnostics lost model markers or mutated original")
		}
	}
	encodedSource, _ := json.Marshal(refs[2].Quote)
	if strings.Contains(visible.Error(), refs[0].Reference) || !strings.Contains(visible.Error(), string(encodedSource)) {
		t.Fatal("batch diagnostic lost source data or exposed old marker")
	}
}

func TestLiveBatchQuoteDiagnosticsUseEveryChatMarker(t *testing.T) {
	refs := []tool.CitationRef{}
	for _, id := range []string{"A", "B", "C"} {
		source := "Exact evidence " + id + "."
		refs = append(refs, tool.CitationRef{Reference: "[K-WORKFLOW-" + id + "]", IndexVersionID: "index", ChunkID: id, QuoteSHA256: citation.QuoteSHA256(source), Quote: source})
	}
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, OutputSchema: json.RawMessage(`{"type":"object","properties":{}}`), OutputSchemaSHA256: "schema"}
	compilation := Compilation{Nodes: []CompiledNode{{ID: "search", Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, node}, Edges: []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}}
	encoded, _ := canonicalJSON(compilation)
	compilation.CompilationSHA256 = hashBytes(encoded)
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": refs})
	step := Step{ID: "step", NodeID: node.ID, Ordinal: 1, Attempt: 1, Status: StepRunning, Input: input, InputSHA256: hashJSON(input)}
	detail := RunDetail{Run: Run{ID: "run", ProjectID: "project", ConversationID: "conversation", Status: RunRunning, CurrentStep: 1, Inputs: raw(`{}`), InputsSHA256: hashJSON(raw(`{}`)), Compilation: compilation, CompilationSHA256: compilation.CompilationSHA256}, Steps: []Step{{NodeID: "search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": refs})}, step}, AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, InputSHA256: step.InputSHA256, ChatRunID: "chat"}}}
	repo := &submissionValidationRepository{detail: detail}
	service := &RuntimeService{repository: repo}
	assessments := []map[string]any{}
	markers := []string{}
	for _, ref := range refs {
		marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
		markers = append(markers, marker)
		assessments = append(assessments, map[string]any{"reference": marker, "decision": "support", "supportingQuote": "Invented evidence."})
	}
	text := string(mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": markers, "citationAssessments": assessments}))
	before, _ := json.Marshal(repo.detail)
	_, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", "step", text)
	var issues *CitationQuoteErrors
	if !errors.As(err, &issues) || len(issues.Issues) != 3 {
		t.Fatalf("live host did not preserve batch: %v", err)
	}
	for i, issue := range issues.Issues {
		if issue.Reference != markers[i] || issue.CandidateQuote != refs[i].Quote {
			t.Fatal("live host restored only the first diagnostic marker")
		}
	}
	after, _ := json.Marshal(repo.detail)
	if string(before) != string(after) {
		t.Fatal("batch preflight changed frozen snapshots")
	}
}

// Selected captures are opt-in and stay outside checked-in fixtures.
func TestCapturedMultiQuoteDiagnostic(t *testing.T) {
	dir := os.Getenv("SCIAIDE_MULTI_QUOTE_REPLAY_DIR")
	if dir == "" {
		t.Skip("no multi-quote capture selected")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	input, schema := read("input.json"), read("schema.json")
	var refs []tool.CitationRef
	var identity struct {
		ChatRunID string `json:"chat_run_id"`
	}
	if json.Unmarshal(read("citation-seeds.json"), &refs) != nil || json.Unmarshal(read("identity.json"), &identity) != nil {
		t.Fatal("invalid replay capture")
	}
	node := CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion, OutputSchema: schema}
	for _, name := range []string{"turn-1.txt", "derived-merged-turn-3.json"} {
		value, _, err := normalizeWorkflowAIStageSubmissionForInput(string(read(name)), node, input)
		if err != nil {
			t.Fatal(err)
		}
		value = restoreWorkflowCitationMarkers(value, refs, identity.ChatRunID)
		value = canonicalSupportingQuotes(value, input)
		var issues *CitationQuoteErrors
		if !errors.As(validateWorkflowAIStageOutput(node, value, input), &issues) || len(issues.Issues) < 2 {
			t.Fatal("capture must expose multiple remaining issues")
		}
		visible := modelVisibleQuoteErrors(issues, refs, identity.ChatRunID)
		data, _ := json.MarshalIndent(visible, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "derived-issues-"+name+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: all %d remaining quote issues surfaced in one correction, no quote accepted by omission or symbol guessing", name, len(issues.Issues))
	}
}

func TestLiveQuoteDiagnosticUsesChatReferenceWithoutChangingSnapshots(t *testing.T) {
	ref := tool.CitationRef{Reference: "[K-WORKFLOW]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: citation.QuoteSHA256("Exact evidence."), Quote: "Exact evidence."}
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, OutputSchema: json.RawMessage(`{"type":"object","properties":{}}`), OutputSchemaSHA256: "schema"}
	compilation := Compilation{Nodes: []CompiledNode{{ID: "search", Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, node}, Edges: []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}}
	encoded, _ := canonicalJSON(compilation)
	compilation.CompilationSHA256 = hashBytes(encoded)
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{ref}})
	step := Step{ID: "step", NodeID: node.ID, Ordinal: 1, Attempt: 1, Status: StepRunning, Input: input, InputSHA256: hashJSON(input)}
	detail := RunDetail{Run: Run{ID: "run", ProjectID: "project", ConversationID: "conversation", Status: RunRunning, CurrentStep: 1, Inputs: raw(`{}`), InputsSHA256: hashJSON(raw(`{}`)), Compilation: compilation, CompilationSHA256: compilation.CompilationSHA256}, Steps: []Step{{NodeID: "search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": []tool.CitationRef{ref}})}, step}, AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, InputSHA256: step.InputSHA256, ChatRunID: "chat"}}}
	repo := &submissionValidationRepository{detail: detail}
	service := &RuntimeService{repository: repo}
	marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	text := string(mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{marker}, "citationAssessments": []map[string]any{{"reference": marker, "decision": "support", "supportingQuote": "Invented evidence."}}}))
	before, _ := json.Marshal(repo.detail)
	_, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", "step", text)
	var issue *CitationQuoteError
	if !errors.As(err, &issue) || issue.Reference != marker || issue.CandidateQuote != ref.Quote {
		t.Fatalf("%v", err)
	}
	if _, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", "step", strings.Replace(text, "Invented evidence.", ref.Quote, 1)); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(repo.detail)
	if string(before) != string(after) {
		t.Fatal("mutated frozen data")
	}
}

func TestQuoteErrorPreservesExcerptAndUsesModelMarker(t *testing.T) {
	// Reproduce the real failure: the indexed excerpt starts inside a word.
	source := "tions had higher drop-out rates than antidepressant interventions (risk ratio 1.31; 95% CI 1.09 to 1.57). Heterogeneity in the network was moderate (τ 2 =0.03; I 2 =46%)."
	ref := tool.CitationRef{Reference: "[K-D1265F722EEE]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: citation.QuoteSHA256(source), Quote: source}
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{ref}})
	makeOutput := func(q string) []byte {
		return mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{ref.Reference}, "citationAssessments": []map[string]any{{"reference": ref.Reference, "decision": "support", "sourceLevel": "abstract", "supportingQuote": q}}})
	}
	bad := "Exercise interven" + source
	err := validateSelectedEvidence(canonicalSupportingQuotes(makeOutput(bad), input), input)
	var issue *CitationQuoteError
	if !errors.As(err, &issue) || issue.CandidateQuote != source || issue.SupportingQuote != bad {
		t.Fatalf("%v", err)
	}
	visible := modelVisibleQuoteError(issue, []tool.CitationRef{ref}, "chat")
	marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	if visible.Reference != marker || strings.Contains(visible.Error(), ref.Reference) || issue.Reference != ref.Reference {
		t.Fatal("wrong marker or mutated original")
	}
	good := "Heterogeneity in the network was moderate (τ 2 =0.03; I 2 =46%)."
	if err := validateSelectedEvidence(makeOutput(good), input); err != nil {
		t.Fatal(err)
	}
	if err := validateSelectedEvidence(makeOutput(strings.Replace(good, "0.03", "0.04", 1)), input); err == nil {
		t.Fatal("changed numeric evidence accepted")
	}
}
