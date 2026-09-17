package workflow

import (
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestHistoricalResourceActivitiesKeepLabelsBeyondLiveWindow(t *testing.T) {
	var primary, fallback []AIStageToolActivity
	for i := 0; i < 42; i++ {
		id := string(rune('A' + i))
		call := tool.Call{ToolName: "builtin.resource.open", Result: &tool.Result{Structured: json.RawMessage(`{"label":"读取 Skill 章节 · statistical-analysis / Python Libraries"}`)}}
		item := AIStageToolActivity{ID: id, Summary: workflowToolSummary(call)}
		fallback = append(fallback, item)
		if i >= 10 {
			primary = append(primary, item)
		}
	}
	merged := mergeAIStageToolActivities(primary, fallback)
	if len(merged) != 42 {
		t.Fatalf("lost calls: %d", len(merged))
	}
	for _, item := range merged {
		if item.Summary != "读取 Skill 章节 · statistical-analysis / Python Libraries" {
			t.Fatal(item.Summary)
		}
	}
}

func TestWorkflowToolSummaryDoesNotTreatResearchAsSearch(t *testing.T) {
	if got := workflowToolSummary(tool.Call{ToolName: "builtin.research.workflow.review.gate"}); got != "核验研究交付条件" {
		t.Fatalf("review gate summary = %q", got)
	}
	if got := workflowToolSummary(tool.Call{ToolName: "builtin.research.workflow.search"}); got != "检索研究资料" {
		t.Fatalf("research search summary = %q", got)
	}
}
