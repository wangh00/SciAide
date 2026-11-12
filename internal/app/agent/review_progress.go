package agent

import (
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

// One extra correction is allowed only for a successfully merged sparse
// review patch with a strict subset of the previous mechanical defects.
// Unknown/scientific/schema errors, regressions and repeats get no extension.
func reviewProgressKeys(err error) map[string]bool {
	keys := map[string]bool{}
	var visit func(error) bool
	visit = func(e error) bool {
		switch v := e.(type) {
		case *apperr.Error:
			return v.Code == "WORKFLOW_AI_SUBMISSION_INVALID" && visit(v.Cause)
		case *workflow.ReviewBasisErrors:
			if v.Truncated || len(v.RepairPaths) == 0 {
				return false
			}
			for _, p := range v.RepairPaths {
				keys["basis:"+p] = true
			}
			return true
		case *workflow.ReviewFindingStateErrors:
			if len(v.Problems) != len(v.RepairPaths) || len(v.RepairPaths) == 0 {
				return false
			}
			for _, p := range v.RepairPaths {
				keys["state:"+p] = true
			}
			return true
		case *workflow.ReviewMissingFindingError:
			keys["missing:"+v.ID] = true
			return true
		case interface{ Unwrap() []error }:
			for _, c := range v.Unwrap() {
				if c != nil && !visit(c) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	if !visit(err) || len(keys) == 0 {
		return nil
	}
	return keys
}

func strictReviewProgress(before, after map[string]bool) bool {
	if len(after) == 0 || len(after) >= len(before) {
		return false
	}
	for key := range after {
		if !before[key] {
			return false
		}
	}
	return true
}
