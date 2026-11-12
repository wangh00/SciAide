package agent

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestWorkflowBrokenRevisionWriteBaselineNeverAsksModelToCorrect(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript(`{"items":["value"],"state":"ready"}`))
	bindSubmissionRepair(loop, state)
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{&apperr.Error{Code: "WORKFLOW_REVISION_WRITE_BASELINE_INVALID", UserMessage: "Host history is not available"}}}
	loop.research = host
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_REVISION_WRITE_BASELINE_INVALID" {
		t.Fatalf("host baseline error blamed model: %s %s", got, state.run.ErrorCode)
	}
	if len(provider.Requests()) != 1 || len(host.validated) != 1 {
		t.Fatal("broken baseline triggered another model correction request")
	}
}
