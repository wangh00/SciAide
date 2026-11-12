package builtin

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/researchworkflow"
)

func TestPublicationBindingErrorsDoNotRequestScientificRevision(t *testing.T) {
	for _, err := range []error{researchworkflow.ErrReviewReportMismatch, fmt.Errorf("%w: appendix hash mismatch", researchworkflow.ErrReviewReportInvalid), researchworkflow.ErrReviewGateUnavailable, fmt.Errorf("research Workflow report requires a valid independent review gate")} {
		mapped := workflowReportError(err)
		safe, ok := mapped.(interface{ UserFacingMessage() string })
		if !ok || !errors.Is(mapped, err) || mapped.Error() != err.Error() {
			t.Fatal("diagnostic cause lost", mapped)
		}
		message := safe.UserFacingMessage()
		if !strings.Contains(message, "发布") || strings.Contains(message, "请重新执行审查阶段") || strings.Contains(message, "报告未通过") {
			t.Fatal(message)
		}
	}
}
