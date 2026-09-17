package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func trackingFixture() (json.RawMessage, map[string]any) {
	input := mustJSON(map[string]any{"context": map[string]any{"markdown": "Result: 5. Methods omit baseline."}, "computedResults": map[string]any{"mean": 3}, "reviewHistory": reviewHistory{IssuePrefix: "issue-1-", Findings: []trackedReviewFinding{}, VerifiedClaims: []string{}}})
	finding := trackedReviewFinding{ID: "issue-1-1", Status: "open", Category: "numericIssues", Summary: "Mean differs", Location: "Results", Basis: []reviewBasis{{"/context/markdown", "Result: 5"}, {"/computedResults/mean", "3"}}, Reason: "Reported mean differs from computed result", Origin: "new", ChangeReason: "First review", CorrectionIndex: 0}
	output := map[string]any{"approved": false, "reviewedInputSha256": hashJSON(input), "verifiedClaims": []string{}, "unsupportedClaims": []string{}, "citationIssues": []string{}, "numericIssues": []string{"Mean differs"}, "methodIssues": []string{}, "requiredCorrections": []string{"Use computed mean"}, "confidence": "medium", "limitations": []string{}, "reviewFindings": []trackedReviewFinding{finding}, "suggestions": []string{}}
	return input, output
}

func TestTrackedReviewRequiresEvidenceAndOneToOneCorrections(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"invented quote", func(o map[string]any) {
			o["reviewFindings"].([]trackedReviewFinding)[0].Basis[0].Quote = "Never reported"
		}},
		{"prior opinion as fact", func(o map[string]any) {
			o["reviewFindings"].([]trackedReviewFinding)[0].Basis[0] = reviewBasis{"/reviewHistory/issuePrefix", "issue-1-"}
		}},
		{"missing finding", func(o map[string]any) { o["reviewFindings"] = []trackedReviewFinding{} }},
		{"wrong correction", func(o map[string]any) { o["reviewFindings"].([]trackedReviewFinding)[0].CorrectionIndex = 1 }},
		{"no new basis", func(o map[string]any) { o["reviewFindings"].([]trackedReviewFinding)[0].ChangeReason = " " }},
		{"renumber old round", func(o map[string]any) { o["reviewFindings"].([]trackedReviewFinding)[0].ID = "issue-3-1" }},
		{"untracked category", func(o map[string]any) { o["methodIssues"] = []string{"Missing method"} }},
		{"false approval", func(o map[string]any) { o["approved"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, output := trackingFixture()
			if err := validateTrackedReview(mustJSON(output), input); err != nil {
				t.Fatal(err)
			}
			tc.edit(output)
			if validateTrackedReview(mustJSON(output), input) == nil {
				t.Fatal("invalid review accepted")
			}
		})
	}
}

func TestTrackedReviewThreeRoundsResolveWithdrawAndReopen(t *testing.T) {
	input, output := trackingFixture()
	prior := output["reviewFindings"].([]trackedReviewFinding)[0]
	for _, status := range []string{"resolved", "withdrawn"} {
		stage := decodeObject(input)
		stage["context"] = map[string]any{"markdown": "Result: 3. Methods omit baseline."}
		stage["reviewHistory"] = reviewHistory{IssuePrefix: "issue-2-", PreviousAttempt: 1, Findings: []trackedReviewFinding{prior}}
		current := prior
		current.Status, current.Origin, current.CorrectionIndex = status, "existing", -1
		current.Basis = []reviewBasis{{"/context/markdown", "Result: 3"}}
		current.ChangeReason = "Current statement uses actual mean; evidence supports the decision"
		output["reviewFindings"], output["approved"], output["numericIssues"], output["requiredCorrections"] = []trackedReviewFinding{current}, true, []string{}, []string{}
		output["suggestions"] = []string{"Optional: shorter heading"}
		if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err != nil {
			t.Fatal(err)
		}
		output["reviewFindings"] = []trackedReviewFinding{}
		if validateTrackedReview(mustJSON(output), mustJSON(stage)) == nil {
			t.Fatal("forgot previous finding")
		}
		stage["reviewHistory"] = reviewHistory{IssuePrefix: "issue-3-", PreviousAttempt: 2, Findings: []trackedReviewFinding{current}}
		current.Status, current.Origin, current.CorrectionIndex = "open", "existing", 0
		output["reviewFindings"], output["approved"], output["numericIssues"], output["requiredCorrections"] = []trackedReviewFinding{current}, false, []string{current.Summary}, []string{"Recheck result"}
		if validateTrackedReview(mustJSON(output), mustJSON(stage)) == nil {
			t.Fatal("reopened silently")
		}
		current.Origin = "reopened"
		output["reviewFindings"] = []trackedReviewFinding{current}
		if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrackedReviewSnapshotBindingAndGateProjection(t *testing.T) {
	input, output := acceptanceFixture(t)
	output["reviewFindings"], output["suggestions"], output["revisionPlan"] = []trackedReviewFinding{}, []string{"Optional style"}, []any{}
	full := mustJSON(output)
	node := CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}
	if err := ValidateAIStageSchema(node.OutputSchema); err != nil {
		t.Fatal(err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, full); err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{Steps: []Step{{ID: "review", NodeID: node.ID, Attempt: 2, Status: StepQueued}}, AIExecutions: []AIExecution{{WorkflowStepID: "review", Attempt: 1, Status: "completed", Output: full, OutputSHA256: hashJSON(full), InputSHA256: hashJSON(input)}, {WorkflowStepID: "review", Attempt: 2, Status: "failed"}}}
	history, err := trackedReviewHistory(detail, node)
	if err != nil || history.PreviousAttempt != 1 || history.IssuePrefix != "issue-3-" || history.PreviousOutputSHA256 != hashJSON(full) {
		t.Fatalf("%+v %v", history, err)
	}
	core, err := reviewCoreForGate(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(independentReviewSchema(), core); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(full), "suggestions") || strings.Contains(string(core), "suggestions") {
		t.Fatal("gate projection changed audit or leaked extension")
	}
	detail.AIExecutions[0].OutputSHA256 = strings.Repeat("a", 64)
	if _, err := trackedReviewHistory(detail, node); err == nil {
		t.Fatal("tampered history accepted")
	}
}

func TestReviewEvidencePointerEscapesAndArrayBounds(t *testing.T) {
	input := decodeObject(raw(`{"context":{"a/b":[{"x~y":"exact"}]}}`))
	value, ok := reviewEvidenceAt(input, "/context/a~1b/0/x~0y")
	if !ok || value != "exact" {
		t.Fatal(value, ok)
	}
	for _, path := range []string{"/context/a~1b/-1", "/context/a~1b/01", "/context/a~1b/3", "/_reviewRevision/anything", "context"} {
		if _, ok := reviewEvidenceAt(input, path); ok {
			t.Fatal(path)
		}
	}
}

func TestTrackedReviewLiveSubmissionCanonicalizesChatCitationQuotes(t *testing.T) {
	s, repository, _ := submissionValidationFixture(t)
	detail := &repository.detail
	node := &detail.Run.Compilation.Nodes[4]
	node.OutputSchema = trackedResearchReviewSchema()
	node.OutputSchemaSHA256 = hashBytes(node.OutputSchema)
	ref := tool.CitationRef{Reference: "[K-AAAAAAAAAAAA]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: strings.Repeat("b", 64)}
	chatReference := citation.KnowledgeReference("review-chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	detail.Run.Compilation.Nodes[2].Kind = NodeCitationSelection
	detail.Steps[2].Output = mustJSON(map[string]any{"citations": []tool.CitationRef{ref}})
	input, output := acceptanceFixture(t)
	stage := decodeObject(input)
	stage["context"] = map[string]any{"markdown": "Finding " + ref.Reference}
	stage["reviewHistory"] = reviewHistory{IssuePrefix: "issue-3-", Findings: []trackedReviewFinding{}}
	stage["revisionTargets"] = []ResearchRevisionTarget{{NodeID: "report_drafting"}}
	step := &detail.Steps[4]
	step.Input, step.InputSHA256 = mustJSON(stage), hashJSON(mustJSON(stage))
	detail.AIExecutions = []AIExecution{{WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ChatRunID: "review-chat"}}
	detail.Run.Compilation.CompilationSHA256 = ""
	encoded, _ := canonicalJSON(detail.Run.Compilation)
	detail.Run.CompilationSHA256 = hashBytes(encoded)
	detail.Run.Compilation.CompilationSHA256 = detail.Run.CompilationSHA256
	output["approved"], output["reviewedInputSha256"] = false, step.InputSHA256
	output["methodIssues"], output["requiredCorrections"] = []string{"Missing method"}, []string{"Describe method"}
	output["reviewFindings"] = []trackedReviewFinding{{ID: "issue-3-1", Status: "open", Category: "methodIssues", Summary: "Missing method", Location: "Methods", Basis: []reviewBasis{{"/context/markdown", "Finding " + chatReference}}, Reason: "Method not described", Origin: "new", ChangeReason: "First detection", CorrectionIndex: 0}}
	output["suggestions"] = []string{}
	output["revisionPlan"] = []researchRevisionPlanItem{{0, "report_drafting", "Missing description", "This is the report", false, ""}}
	text := string(mustJSON(output))
	value, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, text)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(value), chatReference) || !strings.Contains(string(value), ref.Reference) {
		t.Fatal("quote was not canonicalized")
	}
	if !strings.Contains(text, chatReference) {
		t.Fatal("raw response lost")
	}
	// The completion and confirmation paths use the same projection.
	canonical := canonicalCitationSubmission(raw(text), *detail, *step, *node, "review-chat")
	if err := validateWorkflowAIStageOutput(*node, canonical, step.Input); err != nil {
		t.Fatal(err)
	}
}

func TestReportCompletionAndRecoveryKeepDerivedAppendixAndRawText(t *testing.T) {
	detail, step, node, execution := repairRecoveryFixture()
	node.ID, node.PromptVersion, node.OutputSchema = "report_drafting", researchReportVersion, dynamicReportSchema()
	step.NodeID = node.ID
	step.Input = raw(`{"researchContract":{"query":"planned"},"researchSourceContext":{"retrieval":{"queries":["actual"],"sources":[],"candidateCount":3,"firstPageOnly":true}}}`)
	step.InputSHA256 = hashJSON(step.Input)
	detail.Run.Compilation.Nodes, detail.Steps = []CompiledNode{node}, []Step{step}
	execution.InputSHA256 = step.InputSHA256
	execution.OutputText = `{"markdown":"# Findings","claimSummary":[],"methodSummary":"Review","limitations":["Limited evidence"],"confidence":"low"}`
	repository := &submissionRuntimeRepository{}
	s := submissionRuntimeService(repository)
	if err := s.completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if repository.execution.Status != "completed" || len(repository.repairs) > 0 || !strings.Contains(string(repository.execution.Output), retrievalAppendixHeading) || repository.execution.OutputText != execution.OutputText {
		t.Fatalf("%+v", repository)
	}
	committed := repository.execution
	if err := s.completeAIExecution(context.Background(), detail, step, node, committed, false); err != nil {
		t.Fatal(err)
	}
	if repository.failure != "" {
		t.Fatal(repository.failure)
	}
}
