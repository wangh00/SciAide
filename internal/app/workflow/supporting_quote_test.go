package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// Opt-in offline replay of a selected failure. Captured user materials stay
// outside source control; no user database or model is accessed by this test.
func TestPDFQuoteCapturedSubmissionReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_PDF_QUOTE_REPLAY_DIR")
	if dir == "" {
		t.Skip("no captured PDF submission supplied")
	}
	read := func(name string) []byte {
		value, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	input, schema, text := read("input.json"), read("schema.json"), read("turn-1.txt")
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	var identity struct {
		ChatRunID string `json:"chat_run_id"`
	}
	if json.Unmarshal(input, &in) != nil || json.Unmarshal(read("identity.json"), &identity) != nil || identity.ChatRunID == "" {
		t.Fatal("invalid replay identity/input")
	}
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, ReviewPolicy: AIReviewAuto, PromptVersion: selectedEvidenceVersion, OutputSchema: schema, OutputSchemaSHA256: hashJSON(schema)}
	raw, _, err := normalizeWorkflowAIStageSubmissionForInput(string(text), node, input)
	if err != nil {
		t.Fatal(err)
	}
	bound := restoreWorkflowCitationMarkers(raw, in.Candidates, identity.ChatRunID)
	if err := validateSelectedEvidence(bound, input); err == nil {
		t.Fatal("capture no longer reproduces the original quote failure")
	}
	canonical := canonicalSupportingQuotes(bound, input)
	if err := (tool.JSONSchemaValidator{}).Validate(schema, canonical); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(node, canonical, input); err != nil {
		t.Fatal(err)
	}
	decode := func(value []byte) map[string]any {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var object map[string]any
		if err := decoder.Decode(&object); err != nil {
			t.Fatal(err)
		}
		return object
	}
	before, after := decode(bound), decode(canonical)
	sources := map[string]string{}
	for _, c := range in.Candidates {
		sources[c.Reference] = c.Quote
	}
	changed := 0
	for i, item := range before["citationAssessments"].([]any) {
		a, b := item.(map[string]any), after["citationAssessments"].([]any)[i].(map[string]any)
		if a["supportingQuote"] != b["supportingQuote"] {
			changed++
		}
		if a["decision"] != "exclude" && !strings.Contains(sources[b["reference"].(string)], b["supportingQuote"].(string)) {
			t.Fatal("replayed quote is not an exact continuous span")
		}
		a["supportingQuote"] = b["supportingQuote"]
	}
	if !reflect.DeepEqual(before, after) || changed == 0 {
		t.Fatal("changed scientific fields beyond supportingQuote or did not exercise repair")
	}
	// Exercise the actual read-only submission path with an isolated binding.
	service, repo, _ := submissionValidationFixture(t)
	step := &repo.detail.Steps[4]
	step.NodeID, step.Input, step.InputSHA256 = node.ID, input, hashJSON(input)
	repo.detail.Run.Compilation.Nodes = []CompiledNode{{ID: "search", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, node}
	repo.detail.Run.Compilation.Edges = []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}
	repo.detail.Steps[0] = Step{NodeID: "search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": in.Candidates})}
	repo.detail.AIExecutions = []AIExecution{{WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ChatRunID: identity.ChatRunID}}
	repo.detail.Run.Compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(repo.detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	repo.detail.Run.CompilationSHA256 = hashBytes(encoded)
	repo.detail.Run.Compilation.CompilationSHA256 = repo.detail.Run.CompilationSHA256
	beforeState, _ := json.Marshal(repo.detail)
	accepted, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, string(text))
	if err != nil || !rawJSONEqual(accepted, canonical) {
		t.Fatalf("submission path: %v", err)
	}
	afterState, _ := json.Marshal(repo.detail)
	if !bytes.Equal(beforeState, afterState) {
		t.Fatal("read-only replay mutated state")
	}
	// The final commit and restart projection must use the same quote rules.
	callRepo := &quoteReplayCalls{calls: []tool.Call{{ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: in.Candidates}}}}
	commitRepo := &submissionRuntimeRepository{}
	commitService := submissionRuntimeService(commitRepo)
	commitService.tools = tool.NewService(callRepo, tool.JSONSchemaValidator{})
	execution := repo.detail.AIExecutions[0]
	execution.OutputText = string(text)
	if err := commitService.completeAIExecution(context.Background(), repo.detail, *step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if commitRepo.failure != "" || len(commitRepo.repairs) != 0 || commitRepo.execution.Status != "completed" || !rawJSONEqual(commitRepo.execution.Output, canonical) || commitRepo.execution.OutputText != string(text) {
		t.Fatal("commit rejected the canonical result, requested a retry or lost the raw draft")
	}
	recoveryRepo := &submissionRuntimeRepository{}
	recoveryService := submissionRuntimeService(recoveryRepo)
	recoveryService.tools = tool.NewService(callRepo, tool.JSONSchemaValidator{})
	if err := recoveryService.projectFinishedAIExecution(context.Background(), repo.detail, *step, node, commitRepo.execution); err != nil {
		t.Fatal(err)
	}
	if recoveryRepo.failure != "" || len(recoveryRepo.repairs) != 0 || !rawJSONEqual(recoveryRepo.output, commitRepo.output) {
		t.Fatal("restart changed or rejected the completed projection")
	}
	if err := os.WriteFile(filepath.Join(dir, "derived-canonical-turn-1.json"), accepted, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured first submission passes schema, read-only validation, commit and restart projection; %d quote spans restored, all other fields unchanged; no model call", changed)
}

type quoteReplayCalls struct {
	tool.Repository
	tool.SubjectRepository
	calls []tool.Call
}

func (r *quoteReplayCalls) ListBySubject(context.Context, tool.SubjectKind, string) ([]tool.Call, error) {
	return r.calls, nil
}

func TestSupportingQuoteTypography(t *testing.T) {
	for _, tc := range []struct {
		source, quote string
		valid         bool
	}{
		{"nighttime‐specific smartphone use", "nighttime-specific smartphone use", true},
		{"nighttime‑specific use", "nighttime-specific use", true},
		{"中文：nighttime‐specific 结果。", "nighttime-specific 结果。", true},
		{"Reported\nsevere\u00a0sleep disturbance.", "Reported severe sleep disturbance.", true},
		{"In the experi-\r\nments, we used Allstate-10K.", "In the experiments, we used Allstate-10K.", true},
		{"A scalable end-\r\nto-end system and weighted quan-\r\ntile sketch.", "A scalable end-to-end system and weighted quantile sketch.", true},
		{"The justiﬁed eﬀect is suﬃcient.", "The justified effect is sufficient.", true},
		{"Kaggle’s blog reports results.", "Kaggle's blog reports results.", true},
		{"中文：justiﬁed experi-\nments。", "justified experiments。", true},
		{"Allstate-10K has 10K instances.", "Allstate10K has 10K instances.", false},
		{"state-of-the-art results", "stateoftheart results", false},
		{"effect -\r\n0.3", "effect 0.3", false},
		{"10-\n20 participants", "1020 participants", false},
		{"experi-\n\nments", "experiments", false},
		{"experi- ments", "experiments", false},
		{"experi-\nments; experi‐\nments", "experiments", false},
		{"eﬀect", "fect", false},
		{"justiﬁed", "justif", false},
		{"effect ²", "effect 2", false},
		{"experi-\nments; experiments", "experi\nments", false},
		{"OR 1.41", "OR 1.59", false},
		{"effect −0.3", "effect -0.3", false},
		{"effect ‐0.3", "effect -0.3", false},
		{"10–20 participants", "10-20 participants", false},
		{"No benefit was observed.", "Benefit was observed.", false},
		{"first result; other text; last result", "first result last result", false},
		{"nighttime‐specific; nighttime‑specific", "nighttime-specific", false},
	} {
		q, ok := exactSupportingQuote(tc.source, tc.quote)
		if ok != tc.valid {
			t.Fatalf("%+v got=%q ok=%v", tc, q, ok)
		}
		if ok && !strings.Contains(tc.source, q) {
			t.Fatalf("returned text is not exact source: %q", q)
		}
	}
}

func TestCanonicalPDFQuotesRestoreOnlySupportingSpans(t *testing.T) {
	sources := []string{
		"Header. We used four datasets in our experi-\r\nments with Allstate-10K. Footer.",
		"A scalable end-\nto-end system with a weighted quan-\ntile sketch.",
		"Kaggle’s blog reported a theoretically justiﬁed eﬀect.",
	}
	quotes := []string{
		"We used four datasets in our experiments with Allstate-10K.",
		"A scalable end-to-end system with a weighted quantile sketch.",
		"Kaggle's blog reported a theoretically justified effect.",
	}
	candidates := []tool.CitationRef{}
	assessments := []map[string]any{}
	for i, ref := range []string{"[K-A]", "[K-B]", "[K-C]"} {
		candidates = append(candidates, tool.CitationRef{Reference: ref, Quote: sources[i], QuoteSHA256: citation.QuoteSHA256(sources[i])})
		assessments = append(assessments, map[string]any{"reference": ref, "decision": "support", "supportingQuote": quotes[i]})
	}
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": candidates})
	output := mustJSON(map[string]any{"summary": "Unchanged conclusion", "recommendation": "proceed", "recommendedReferences": []string{"[K-A]", "[K-B]", "[K-C]"}, "citationAssessments": assessments})
	beforeInput := string(input)
	if err := validateSelectedEvidence(output, input); err == nil {
		t.Fatal("fixture must reproduce the raw quote failure")
	}
	got := canonicalCitationSubmission(output, RunDetail{}, Step{Input: input}, CompiledNode{ID: "evidence_screening"}, "")
	if err := validateSelectedEvidence(got, input); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Summary     string                                        `json:"summary"`
		Assessments []struct{ Reference, SupportingQuote string } `json:"citationAssessments"`
	}
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatal(err)
	}
	if result.Summary != "Unchanged conclusion" || len(result.Assessments) != len(sources) || string(input) != beforeInput {
		t.Fatal("changed conclusions or frozen input")
	}
	for i, assessment := range result.Assessments {
		if !strings.Contains(sources[i], assessment.SupportingQuote) || assessment.Reference != candidates[i].Reference {
			t.Fatal("quote is not a continuous span in its exact source")
		}
	}
	if string(canonicalSupportingQuotes(got, input)) != string(got) {
		t.Fatal("PDF canonicalization is not idempotent")
	}
	for _, bad := range []struct {
		index int
		quote string
	}{
		{0, "We used five datasets in our experiments with Allstate-10K."},
		{0, quotes[1]},
		{0, "We used four datasets Footer."},
		{2, "Kaggle's blog reported a theoretically unjustified effect."},
	} {
		assessments[bad.index]["supportingQuote"] = bad.quote
		candidate := mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{candidates[bad.index].Reference}, "citationAssessments": []map[string]any{assessments[bad.index]}})
		if err := validateSelectedEvidence(canonicalSupportingQuotes(candidate, input), input); err == nil {
			t.Fatalf("changed/cross-source/stitched evidence accepted: %q", bad.quote)
		}
	}
}

func TestCanonicalQuoteWithReissuedMarker(t *testing.T) {
	source := "Observed nighttime‐specific use."
	ref := tool.CitationRef{Reference: "[K-ORIGINAL]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: citation.QuoteSHA256(source), Quote: source}
	compilation := Compilation{Nodes: []CompiledNode{{ID: "search", Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, {ID: "evidence_screening"}}, Edges: []Edge{{FromNode: "search", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"}}}
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{ref}})
	step := Step{NodeID: "evidence_screening", Ordinal: 1, Input: input}
	detail := RunDetail{Run: Run{Compilation: compilation}, Steps: []Step{{NodeID: "search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": []tool.CitationRef{ref}})}}}
	marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	output := mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{marker}, "citationAssessments": []map[string]any{{"reference": marker, "decision": "support", "supportingQuote": "Observed nighttime-specific use."}}})
	got := canonicalCitationSubmission(output, detail, step, compilation.Nodes[1], "chat")
	if err := validateSelectedEvidence(got, input); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), marker) || !strings.Contains(string(got), source) {
		t.Fatalf("marker/quote was not restored: %s", got)
	}
}

// Optional read-only replay; private research records are never checked in.
func TestSupportingQuoteLocalReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_QUOTE_REPLAY_DIR")
	if dir == "" {
		t.Skip("no local replay fixture supplied")
	}
	input, err := os.ReadFile(filepath.Join(dir, "step-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(filepath.Join(dir, "execution-output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(string(output)), "```json"), "```")))
	value = restoreWorkflowCitationMarkers(value, in.Candidates, os.Getenv("SCIAIDE_QUOTE_REPLAY_CHAT_RUN"))
	if err := validateSelectedEvidence(value, input); err == nil {
		t.Fatal("fixture must reproduce original failure")
	} else {
		t.Logf("before: %v", err)
	}
	value = canonicalSupportingQuotes(value, input)
	if err := validateSelectedEvidence(value, input); err != nil {
		t.Fatal(err)
	}
	t.Log("original failing submission now passes evidence validation")
}

func TestCanonicalSubmissionRestoresExactSourceQuote(t *testing.T) {
	source := "Four unique profiles for nighttime‐specific smartphone use were identified."
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []map[string]any{{"reference": "[K-AAA]", "quote": source}}})
	output := mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{"[K-AAA]"}, "citationAssessments": []map[string]any{{"reference": "[K-AAA]", "decision": "support", "supportingQuote": "Four unique profiles for nighttime-specific smartphone use were identified."}}})
	canonical := canonicalCitationSubmission(output, RunDetail{}, Step{Input: input}, CompiledNode{ID: "evidence_screening"}, "")
	if err := validateSelectedEvidence(canonical, input); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Assessments []struct {
			Quote string `json:"supportingQuote"`
		} `json:"citationAssessments"`
	}
	json.Unmarshal(canonical, &got)
	if got.Assessments[0].Quote != source {
		t.Fatalf("did not restore source: %s", canonical)
	}
	if string(canonicalSupportingQuotes(canonical, input)) != string(canonical) {
		t.Fatal("canonicalization is not idempotent")
	}
}

func TestCanonicalQuotePreservesBoundaries(t *testing.T) {
	output := json.RawMessage(`{"count":9007199254740993,"citationAssessments":[{"reference":"[K-A]","decision":"support","supportingQuote":"nighttime-specific use"}]}`)
	for _, candidates := range []any{
		[]map[string]any{{"reference": "[K-B]", "quote": "nighttime‐specific use"}},
		[]map[string]any{{"reference": "[K-A]", "quote": "nighttime‐specific use"}, {"reference": "[K-A]", "quote": "different source"}},
		[]map[string]any{{"reference": "[K-A]", "quote": "nighttime‐specific" + strings.Repeat(" ", 701) + "use"}},
	} {
		input := mustJSON(map[string]any{"candidates": candidates})
		if got := canonicalSupportingQuotes(output, input); string(got) != string(output) {
			t.Fatalf("must not repair wrong/ambiguous/oversized quote: %s", got)
		}
	}
	input := mustJSON(map[string]any{"candidates": []map[string]any{{"reference": "[K-A]", "quote": "nighttime‐specific use"}}})
	got := canonicalSupportingQuotes(output, input)
	if !strings.Contains(string(got), "9007199254740993") || !strings.Contains(string(got), "nighttime‐specific use") {
		t.Fatalf("lost exact numeric value or failed repair: %s", got)
	}
}
