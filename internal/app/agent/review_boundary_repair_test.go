package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

func TestMixedReviewRetainsCurrentNavigationThroughFullResubmission(t *testing.T) {
	source := reviewSelectionFixture().sources[0]
	basis := &workflow.ReviewBasisErrors{Problems: []string{"quote mismatch"}, RepairPaths: []string{source.Cell}, Sources: []workflow.ReviewBasisSource{source}}
	mixed := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: errors.Join(basis, errors.New("revisionPlan correspondence requires full replacement"))}
	if newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, mixed) != nil {
		t.Fatal("mixed errors must not become a patch")
	}
	for _, progress := range []bool{false, true} {
		scripts := [][]fake.Step{submissionScript(reviewRepairTestBase)}
		if progress {
			scripts = append(scripts, submissionScript("I will fix the plan and exact quote."))
		}
		scripts = append(scripts, submissionScript("```json\n"+reviewRepairTestBase+"\n```"))
		loop, state, provider := newLoopFixture(t, nil, scripts...)
		state.workflowAI = true
		host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}, errors: []error{mixed}, accepted: json.RawMessage(reviewRepairTestBase)}
		loop.research = host
		if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
			t.Fatal(got, state.run.ErrorMessage)
		}
		if len(host.validated) != 2 || len(state.calls) != 0 {
			t.Fatal("protocol text reached validator or tools replayed")
		}
		for _, req := range provider.Requests()[1:] {
			var prompt strings.Builder
			for _, m := range req.Messages {
				prompt.WriteString(m.Content)
			}
			if len(req.Tools) != 1 || req.Tools[0].Name != stageSubmissionTool || strings.Count(prompt.String(), "<review_basis_sources>") != 1 || !strings.Contains(prompt.String(), "Current. ") || !strings.Contains(prompt.String(), "revisionPlan correspondence") {
				t.Fatal("full correction lost navigation/diagnostic or gained patch tool", prompt.String())
			}
		}
		if progress && state.journalDrafts[2] != "I will fix the plan and exact quote." {
			t.Fatal("raw progress audit lost")
		}
	}
}

func TestReviewProgressSharesContentBudgetAndKeepsPatchBase(t *testing.T) {
	r := newReviewRepair(reviewRepairTestBase, reviewRepairTestSchema, reviewRepairDiagnostic())
	patch := reviewRepairValidPatch(r)
	merged, _ := r.merge(patch)
	for _, exhaust := range []bool{false, true} {
		last := patch
		if exhaust {
			last = "I will submit later again."
		}
		loop, state, provider := newLoopFixture(t, nil, submissionScript(reviewRepairTestBase), submissionScript("I will correct it."), submissionScript(last))
		state.workflowAI = true
		host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}, errors: []error{reviewRepairDiagnostic()}, accepted: json.RawMessage(merged)}
		loop.research = host
		out := loop.Run(context.Background(), state.run.ID)
		if len(provider.Requests()) != 3 || len(state.calls) != 0 {
			t.Fatal("unbounded retries or repeated research")
		}
		if exhaust {
			if out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(host.validated) != 1 {
				t.Fatal("shared allowance bypassed", state.run)
			}
		} else if out != OutcomeCompleted || len(host.validated) != 2 || host.validated[1][3] != merged {
			t.Fatal("repair base lost", state.run)
		}
		for _, req := range provider.Requests()[1:] {
			if len(req.Tools) != 2 || !strings.Contains(string(req.Tools[1].InputSchema), r.hash) {
				t.Fatal("progress reset repair base")
			}
		}
		if state.journalDrafts[2] != "I will correct it." {
			t.Fatal("progress original not audited")
		}
	}
}

func TestSubmissionFenceAndTextRecoveryConsistentAtFirstDedicatedTurn(t *testing.T) {
	valid := `{"items":["Literal <tool_call> is data"],"state":"blocked"}`
	for _, first := range []string{"```json\n" + valid + "\n```", "I will submit later.", "<tool_call>invented</tool_call>", "```json\n{invalid}\n```"} {
		scripts := [][]fake.Step{submissionScript(first)}
		if !stageSubmissionEnvelope(first) {
			scripts = append(scripts, submissionToolScript(valid))
		}
		loop, state, provider := newLoopFixture(t, nil, scripts...)
		bindSubmissionRepair(loop, state)
		state.run.ModelTurns = 12
		loop.resources = &resourceViewFixture{}
		guidance := loop.research.(*staticResearchGuidance)
		guidance.value.SkillDiscovery = true
		guidance.value.AllowedToolNames = []string{"builtin.resource.open"}
		if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted || len(provider.Requests()) != len(scripts) || len(state.calls) != 0 || state.journalDrafts[13] != first {
			t.Fatal("inconsistent first submission", first, state.run)
		}
		for _, req := range provider.Requests() {
			if len(req.ProviderTurns) != 0 || req.ForcedTool != stageSubmissionTool {
				t.Fatal("recovery escaped submission")
			}
		}
	}
	for _, text := range []string{"```json\n{}\n```\n```json\n{}\n```", "```json\n{}\ntrailing garbage\n```"} {
		if _, err := stageSubmissionCandidate(nil, text); err == nil {
			t.Fatal("ambiguous/malformed fence accepted", text)
		}
	}
	if recoverableSubmissionText(errors.New("storage failed"), nil) || recoverableSubmissionText(&apperr.Error{Code: "MODEL_SUBMISSION_MISSING"}, []model.ToolCall{{Name: "unexpected"}}) {
		t.Fatal("non-text errors entered content retry")
	}
}

func TestSubmissionTextRecoveryDoesNotHideAuditFailure(t *testing.T) {
	for _, draftFails := range []bool{false, true} {
		loop, state, provider := newLoopFixture(t, nil, submissionScript("I will submit later."), submissionToolScript(`{"items":[],"state":"ready"}`))
		bindSubmissionRepair(loop, state)
		state.run.ModelTurns = 12
		loop.resources = &resourceViewFixture{}
		guidance := loop.research.(*staticResearchGuidance)
		guidance.value.SkillDiscovery = true
		guidance.value.AllowedToolNames = []string{"builtin.resource.open"}
		loop.runs = &submissionAuditFault{loopState: state, failDraft: draftFails}
		if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_SUBMISSION_AUDIT_FAILED" || len(provider.Requests()) != 1 {
			t.Fatal("audit failure triggered model correction", state.run)
		}
	}
}
