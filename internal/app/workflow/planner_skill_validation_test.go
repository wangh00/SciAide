package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/skillrun"
)

func TestPlannerSkillClaimsUseActualLoadsNotCatalogOrSummary(t *testing.T) {
	plan := plannerRegressionPayload(t, false)
	plan["selectedSkills"] = []any{map[string]any{"name": "exploratory-data-analysis", "role": "读取数据", "limitations": []any{}}}
	routes := plan["routes"].([]any)
	stages := routes[0].(map[string]any)["stagePlans"].([]any)
	stages[0].(map[string]any)["skillNames"] = []any{"seaborn", "scientific-visualization"}
	data, _ := json.Marshal(plan)
	names, err := ResearchPlannerSkillClaims(string(data), semanticResearchStarterSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"exploratory-data-analysis", "scientific-visualization", "seaborn"}) {
		t.Fatalf("lost stage claims: %v", names)
	}
	loaded := []skillrun.Snapshot{{Name: "exploratory-data-analysis", ContentHash: "content", PackageHash: "package"}}
	err = ValidateResearchPlannerSkillClaims(names, loaded)
	if err == nil || !strings.Contains(err.Error(), "scientific-visualization") || !strings.Contains(err.Error(), "seaborn") {
		t.Fatalf("accepted catalog candidates: %v", err)
	}
	for _, name := range names[1:] {
		loaded = append(loaded, skillrun.Snapshot{Name: name, ContentHash: "content", PackageHash: "package"})
	}
	if err = ValidateResearchPlannerSkillClaims(names, loaded); err != nil {
		t.Fatal(err)
	}
	loaded[1].PackageHash = ""
	if err = ValidateResearchPlannerSkillClaims(names, loaded); err == nil {
		t.Fatal("accepted incomplete snapshot")
	}
	if names, err = ResearchPlannerSkillClaims(`{"text":"regular chat"}`, raw(`{"type":"object"}`)); err != nil || len(names) != 0 {
		t.Fatal("ordinary output affected")
	}
}
