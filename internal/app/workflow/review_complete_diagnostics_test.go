package workflow

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestReviewAllBasisDiagnosticsVisibleInOneCorrection(t *testing.T) {
	for _, count := range []int{7, 8, 25, 400} {
		input := mustJSON(map[string]any{"context": map[string]any{"markdown": "Current energy intake is unverified."}})
		findings := []trackedReviewFinding{}
		for i := 0; i < count; i++ {
			if i%4 == 0 {
				findings = append(findings, trackedReviewFinding{ID: fmt.Sprintf("issue-1-%d", i/4+1)})
			}
			findings[i/4].Basis = append(findings[i/4].Basis, reviewBasis{"/context/markdown", strings.Repeat("Paraphrase ", 180)})
		}
		var diagnostic *ReviewBasisErrors
		if !errors.As(reviewBasisDiagnostics(findings, input), &diagnostic) || diagnostic.Truncated || len(diagnostic.Problems) != count || len(diagnostic.RepairPaths) != 2*count {
			t.Fatal("hidden diagnostics", count, diagnostic)
		}
		if !strings.Contains(diagnostic.Error(), fmt.Sprintf("/reviewFindings/%d/basis/%d/quote", (count-1)/4, (count-1)%4)) || strings.Count(diagnostic.Error(), "Paraphrase") != 0 {
			t.Fatal("missing output locator or duplicated long rejected quotes")
		}
		sourceRunes := 0
		for _, source := range diagnostic.Sources {
			for _, segment := range source.Segments {
				sourceRunes += len([]rune(segment.Text))
			}
		}
		if sourceRunes > 12000 {
			t.Fatal("source budget expanded with issue count")
		}
		for i := range findings {
			for j := range findings[i].Basis {
				findings[i].Basis[j].Quote = "Current energy intake is unverified."
			}
		}
		if err := reviewBasisDiagnostics(findings, input); err != nil {
			t.Fatal("correcting all reported cells still fails", err)
		}
	}
}

func TestReviewAllCorrespondenceErrorsVisibleAlongsideBasis(t *testing.T) {
	input, output := trackingFixture()
	findings := []trackedReviewFinding{}
	for i := 0; i < 25; i++ {
		f := output["reviewFindings"].([]trackedReviewFinding)[0]
		f.ID = fmt.Sprintf("issue-1-%d", i+1)
		f.CorrectionIndex = 99
		findings = append(findings, f)
	}
	output["reviewFindings"] = findings
	err := validateTrackedReview(mustJSON(output), input)
	if err == nil || !strings.Contains(err.Error(), "issue-1-25 必须唯一对应") {
		t.Fatal("late correspondence errors hidden", err)
	}
}
