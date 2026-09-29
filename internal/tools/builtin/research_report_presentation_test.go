package builtin

import (
	"github.com/wangh00/SciAide/internal/app/tool"
	"strings"
	"testing"
)

func TestReportKeepsProseSourcesAndArtifactsWithoutInternalJSON(t *testing.T) {
	body := "# Report\n\n| Mode | Effect |\n| --- | --- |\n| Yoga | -0.55 |\n\n[K-A]"
	report := composeWorkflowReport(body, []tool.CitationRef{{Reference: "[K-A]", SourceName: "paper.pdf", Locator: "page:8", Quote: "Exact evidence."}}, []tool.ArtifactRef{{Name: "results.csv", WorkspacePath: "results.csv"}})
	for _, term := range []string{body, "[K-A] paper.pdf，page:8：Exact evidence.", "results.csv"} {
		if !strings.Contains(report, term) {
			t.Fatal("lost content", term)
		}
	}
	for _, term := range []string{"分析执行摘要", "```json", "methodSummary", "claimSummary"} {
		if strings.Contains(report, term) {
			t.Fatal("internal audit leaked", term)
		}
	}
}
