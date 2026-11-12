package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

// Production loop, deterministic model/validator fixture. The oversized frozen
// input needs paging. Two rejected candidates then exercise both repair forms,
// with/without measured usage, while the scientific decision remains rejected.
func TestStageContextViewStableAcrossCorrectionTurns(t *testing.T) {
	for _, reported := range []bool{false, true} {
		for _, mode := range []string{"local", "full", "exhausted"} {
			t.Run(fmt.Sprintf("usage=%t/%s", reported, mode), func(t *testing.T) {
				input, _ := json.Marshal(map[string]any{"originalRequest": "Keep the exact research question", "acceptanceCriteria": []string{"Do not approve unverified claims"}, "context": map[string]any{"markdown": strings.Repeat("Current report claim. ", 3600)}, "evidenceWork": map[string]any{"workProduct": strings.Repeat("Original verified source passage. ", 6000)}})
				prompt := "Frozen input\n<stage_input>\n" + string(input) + "\n</stage_input>"
				contract := stageBudgetContract(input, prompt)
				contract.OutputSchema = reviewRepairTestSchema
				diagnostic := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: errors.Join(&workflow.ReviewBasisErrors{Problems: []string{"quote mismatch"}, RepairPaths: []string{"/reviewFindings/0/basis/0/quote"}, Sources: []workflow.ReviewBasisSource{{Cell: "/reviewFindings/0/basis/0/quote", Pointer: "/context/markdown", InputSHA256: contract.InputSHA256}}}, reviewRepairDiagnostic())}
				if mode == "full" {
					diagnostic = &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: errors.Join(diagnostic, errors.New("additional full result defect"))}
				}
				base := strings.Replace(reviewRepairTestBase, "Key result not verified", strings.Repeat("Key result not verified. ", 750), 1)
				script := func() []fake.Step {
					steps := submissionScript(base)
					if reported {
						steps = append([]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 35000}}}}, steps...)
					}
					return steps
				}
				loop, state, provider := newLoopFixture(t, nil, script(), script(), script())
				state.workflowAI = true
				state.messages[0].Parts = []conversation.MessagePart{{Type: "text", Text: prompt}}
				loop.runs = workflowContractRuns{loopState: state, contract: contract, found: true}
				loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(150000, 135000, modelcap.ContextWindowSourceManual)}
				def := tool.Definition{QualifiedName: workflow.ResearchTaskReadTool, Description: "Read saved immutable input", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
				if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: def, invoke: func(tool.Invocation) tool.Result {
					t.Error("correction unexpectedly reread material")
					return tool.Result{Status: tool.ResultSuccess}
				}}); err != nil {
					t.Fatal(err)
				}
				faults := []error{diagnostic, diagnostic}
				if mode == "exhausted" {
					faults = append(faults, diagnostic)
				}
				host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{AllowedToolNames: []string{workflow.ResearchTaskReadTool}, ExecutionSystemContext: strings.Repeat("Lifecycle rules. ", 1300), StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}, errors: faults, accepted: json.RawMessage(base)}
				loop.research = host
				outcome := loop.Run(context.Background(), state.run.ID)
				want := OutcomeCompleted
				if mode == "exhausted" {
					want = OutcomeFailed
				}
				requests := provider.Requests()
				if outcome != want || len(requests) != 3 || len(host.validated) != 3 {
					t.Fatalf("outcome=%s requests=%d validated=%d code=%s detail=%s", outcome, len(requests), len(host.validated), state.run.ErrorCode, state.run.ErrorDetails)
				}
				if mode == "exhausted" && state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" {
					t.Fatal("scientific rejection bypassed", state.run.ErrorCode)
				}
				first := ""
				for i, r := range requests {
					found := false
					for _, m := range r.Messages {
						if m.HostToolReferences && strings.Contains(m.Content, "<stage_input>\n") {
							if first == "" {
								first = m.Content
							}
							if m.Content != first {
								t.Fatal("correction repaged already read input", i, len(first), len(m.Content))
							}
							found = true
						}
					}
					if !found {
						t.Fatal("lost original stage view", i)
					}
					if i > 0 {
						for _, d := range r.Tools {
							if d.Name != stageSubmissionTool && d.Name != stageRepairTool {
								t.Fatal("correction reopened exploration", d.Name)
							}
						}
					}
				}
				if first == prompt || !strings.Contains(first, `"$read"`) || !strings.Contains(first, "Keep the exact research question") || !strings.Contains(first, "Do not approve unverified claims") {
					t.Fatal("fixture did not page or lost contract")
				}
				if conversationText(state.messages[0]) != prompt || len(state.calls) != 0 {
					t.Fatal("durable source changed or completed work replayed")
				}
				if !strings.Contains(host.validated[2][3], `"approved":false`) {
					t.Fatal("scientific decision lost")
				}
			})
		}
	}
}
