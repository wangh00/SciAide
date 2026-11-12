package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func TestReviewBasisFailureKeepsBudgetAndDoesNotClaimScientificRejection(t *testing.T) {
	response := `{"items":["unchanged review"],"state":"ready"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(response), submissionScript(response), submissionScript(response))
	bindSubmissionRepair(loop, state)
	private := "PRIVATE ORIGINAL SCIENTIFIC MATERIAL"
	basisErrors := &workflow.ReviewBasisErrors{Problems: []string{"issue-1-1 invalid pointer; quote=" + private}}
	validationErr := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: errors.Join(errors.New("review schema error"), basisErrors)}
	loop.research = &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{validationErr, validationErr, validationErr}}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 || state.messages[1].Parts[0].Text != "" {
		t.Fatal("repair count changed or invalid output committed", got, state.run)
	}
	message := state.run.ErrorMessage
	if !strings.Contains(message, "独立审查的依据定位") || !strings.Contains(message, "不代表报告已被判定不合格") || !strings.Contains(message, "无需新建任务") || strings.Contains(message, private) {
		t.Fatal("failure banner unclear or leaking evidence", message)
	}
	requests := provider.Requests()
	if requests[1].ForcedTool != stageSubmissionTool || !strings.Contains(submissionRequestText(requests[1]), private) || !strings.Contains(state.run.ErrorDetails, private) {
		t.Fatal("complete correction diagnostics lost")
	}
}
