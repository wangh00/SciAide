package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model/fake"
)

// Private incident captures are opt-in and never read from the live database.
func TestCapturedSubmissionChainExposesQuoteSelection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_SUBMISSION_CHAIN_REPLAY_DIR")
	if dir == "" {
		t.Skip("no task-scoped submission capture")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	schema := json.RawMessage(read("schema.json"))
	text := string(read("turn-1.txt"))
	var issue workflow.CitationQuoteError
	if err := json.Unmarshal(read("quote-issue.json"), &issue); err != nil {
		t.Fatal(err)
	}
	r := newCitationRepairForError(text, schema, &issue)
	if r == nil || len(r.sources) == 0 {
		_, _, normalizeErr := workflow.NormalizeAIStageSubmission(text, schema)
		t.Fatalf("captured citation repair is unavailable: normalize=%v", normalizeErr)
	}
	ids := []string{}
	selecting := false
	for _, segment := range r.sources[0].Segments {
		if strings.Contains(segment.Text, "Results support the hypothesis") {
			selecting = true
		}
		if selecting {
			ids = append(ids, segment.ID)
		}
		if selecting && strings.Contains(segment.Text, "independent of the degree of prior learning") {
			break
		}
	}
	if len(ids) == 0 {
		t.Fatal("captured source has no selected results passage")
	}
	patch := sourceSegmentPatch(r, issue.Reference, ids...)
	merged, err := r.merge(patch)
	if err != nil || !strings.Contains(merged, "advantage oftesting over restudy") {
		t.Fatal("selection did not preserve original merged words", err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(schema, json.RawMessage(merged)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "merged-selection.json"), []byte(merged), 0600); err != nil {
		t.Fatal(err)
	}
	validation := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &issue}
	for _, mode := range []string{"selection", "repeated_then_refusal", "repeated_then_progress"} {
		t.Run(mode, func(t *testing.T) {
			texts := []string{text, patch}
			if mode != "selection" {
				texts = []string{text, string(read("turn-2.txt")), string(read("turn-3.txt"))}
				if mode == "repeated_then_progress" {
					texts[2] = "I will submit later."
				}
			}
			scripts := [][]fake.Step{}
			for _, value := range texts {
				scripts = append(scripts, submissionScript(value))
			}
			loop, state, provider := newLoopFixture(t, nil, scripts...)
			state.workflowAI = true
			host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: schema}}, accepted: json.RawMessage(merged), errors: []error{validation}}
			if mode != "selection" {
				host.errors = append(host.errors, validation)
			}
			loop.research = host
			out := loop.Run(context.Background(), state.run.ID)
			if len(provider.Requests()) != len(texts) || len(state.calls) != 0 {
				t.Fatal("unbounded retry or repeated exploration")
			}
			for i, value := range texts {
				if state.journalDrafts[i+1] != value {
					t.Fatal("raw incident response lost", i+1)
				}
			}
			if mode == "selection" {
				if out != OutcomeCompleted || len(host.validated) != 2 || host.validated[1][3] != merged {
					t.Fatal("selected correction did not reach full host validation", state.run)
				}
			} else {
				code := "WORKFLOW_AI_MODEL_REFUSED"
				if mode == "repeated_then_progress" {
					code = "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED"
				}
				if out != OutcomeFailed || state.run.ErrorCode != code || !strings.Contains(state.run.ErrorDetails, issue.Reference) || !strings.Contains(state.run.ErrorDetails, "unchanged rejectedSupportingQuote") {
					t.Fatal("last response hid the original quotation failure", state.run)
				}
			}
			for _, request := range provider.Requests()[1:] {
				if !request.RequireTool || request.ForcedTool != "" || len(request.Tools) != 2 {
					t.Fatal("correction choice became optional or full replacement was removed")
				}
			}
		})
	}
}
