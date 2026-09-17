package builtin

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
	"strings"
	"testing"
)

type compatibleSkillFixture struct {
	missingSkillFixture
	names  []opensciskill.Info
	loaded string
}

func (f *compatibleSkillFixture) Catalog(context.Context, string) (opensciskill.Snapshot, error) {
	return opensciskill.Snapshot{Skills: f.names, Categories: []opensciskill.Category{{Name: "coding", Count: 2}, {Name: "biology", Count: 1}}}, nil
}
func (f *compatibleSkillFixture) LoadStructuredForRun(_ context.Context, _, _, _, name, _ string, _, _ int) (opensciskill.Info, opensciskill.Chunk, bool, error) {
	f.loaded = name
	return opensciskill.Info{Name: name, Origin: opensciskill.OriginUser}, opensciskill.Chunk{Mode: "skill", Content: "Instructions"}, true, nil
}
func TestEmptySkillCategoryReturnsActualChoicesWithoutFailingOrLoading(t *testing.T) {
	f := &compatibleSkillFixture{}
	loader := NewSkillLoad(f)
	result, err := loader.Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{"category":"statistics","limit":10}`)})
	if err != nil || result.Status != tool.ResultSuccess || f.loaded != "" {
		t.Fatalf("result=%#v err=%v loaded=%s", result, err, f.loaded)
	}
	var metadata skillLoadResult
	if err := json.Unmarshal(result.Structured, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Loaded || metadata.Count != 0 || metadata.Total != 0 || metadata.Category != "statistics" || len(metadata.Skills) != 0 {
		t.Fatalf("false catalog evidence: %#v", metadata)
	}
	if !strings.Contains(result.Text, "coding") || !strings.Contains(result.Text, "biology") || !strings.Contains(result.Text, "loaded=false") {
		t.Fatalf("missing choices: %s", result.Text)
	}
	definition, err := loader.Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(definition.OutputSchema, result.Structured); err != nil {
		t.Fatalf("frozen tool contract changed: %v", err)
	}
}
func TestSkillNameCompatibilityUsesUniqueActualChoice(t *testing.T) {
	f := &compatibleSkillFixture{names: []opensciskill.Info{{Name: "statistical-analysis", Enabled: true, Entry: true}}}
	result, err := NewSkillLoad(f).Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{"name":"  Statistical-Analysis  "}`)})
	if err != nil || f.loaded != "statistical-analysis" || !strings.Contains(result.Text, "唯一匹配") {
		t.Fatalf("load=%s result=%s err=%v", f.loaded, result.Text, err)
	}
	if strings.Contains(string(result.Structured), `"name":"Statistical-Analysis"`) {
		t.Fatal("noncanonical name persisted")
	}
}
func TestSkillChoiceDoesNotGuessOrEnableUnavailableItems(t *testing.T) {
	choices := []opensciskill.Info{{Name: "statistical-analysis", Enabled: true, Entry: true}}
	for _, query := range []string{"statistics", "统计分析", "statistical_analysis"} {
		if got := canonicalSkillChoice(query, choices); got != query {
			t.Fatalf("guessed %s -> %s", query, got)
		}
	}
	choices = append(choices, opensciskill.Info{Name: "Statistical-Analysis", Enabled: true, Entry: true})
	if got := canonicalSkillChoice("STATISTICAL-ANALYSIS", choices); got != "STATISTICAL-ANALYSIS" {
		t.Fatal("ambiguous match accepted")
	}
	choices = []opensciskill.Info{{Name: "hidden", Enabled: false, Entry: true}}
	if got := canonicalSkillChoice("HIDDEN", choices); got != "HIDDEN" {
		t.Fatal("disabled choice normalized")
	}
}
