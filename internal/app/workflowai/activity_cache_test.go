package workflowai

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestActivityCacheIsBoundedAndRevisionSpecific(t *testing.T) {
	b := &Bridge{}
	activity := workflow.AIStageActivity{ExecutionID: "e", ToolCalls: []workflow.AIStageToolActivity{{ID: "tool", Summary: "original"}}}
	b.saveActivity("run", "v1", activity)
	got, ok := b.loadActivity("run", "v1")
	if !ok || got.ExecutionID != "e" || len(got.ToolCalls) != 1 {
		t.Fatal("cache lost activity", got)
	}
	got.ToolCalls[0].Summary = "modified"
	next, _ := b.loadActivity("run", "v1")
	if next.ToolCalls[0].Summary != "original" {
		t.Fatal("shared cache mutation")
	}
	if _, ok := b.loadActivity("run", "v2"); ok {
		t.Fatal("stale version returned")
	}
	for i := 0; i < 70; i++ {
		b.saveActivity(fmt.Sprint(i), "version", activity)
	}
	if len(b.activityCache) != 64 {
		t.Fatal("unbounded entry count")
	}
	if _, ok := b.loadActivity("run", "v1"); ok {
		t.Fatal("old entry not evicted")
	}
	for i := 0; i < 70; i++ {
		activity.CurrentDraft = strings.Repeat("x", 100000)
		b.saveActivity(fmt.Sprint(i), "large", activity)
	}
	if b.activityBytes > 4*1024*1024 {
		t.Fatal("unbounded cached bytes")
	}
}
