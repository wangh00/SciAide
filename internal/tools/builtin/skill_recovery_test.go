package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
)

type recoverySkillFixture struct {
	snapshot opensciskill.Snapshot
	err      error
	loads    int
	chunk    opensciskill.Chunk
}

func (f *recoverySkillFixture) Catalog(context.Context, string) (opensciskill.Snapshot, error) {
	return f.snapshot, nil
}
func (f *recoverySkillFixture) Browse(_ context.Context, _, category string) ([]opensciskill.Info, error) {
	values := []opensciskill.Info{}
	for _, info := range f.snapshot.Skills {
		if info.Callable() && strings.EqualFold(info.Category, category) {
			values = append(values, info)
		}
	}
	return values, nil
}
func (f *recoverySkillFixture) LoadStructuredForRun(_ context.Context, _, _, _, name, _ string, _, _ int) (opensciskill.Info, opensciskill.Chunk, bool, error) {
	f.loads++
	return opensciskill.Info{Name: name}, f.chunk, false, f.err
}

func TestResearchCategoryMistakeReturnsCatalogNotUnavailableSuggestion(t *testing.T) {
	f := &recoverySkillFixture{
		snapshot: opensciskill.Snapshot{
			Categories: []opensciskill.Category{{Name: "research", Count: 2}},
			Skills: []opensciskill.Info{
				{Name: "research-workflows", Category: "research", Enabled: true, Entry: true, Capability: opensciskill.CapabilityUnavailable},
				{Name: "research-methods", Category: "research", Enabled: true, Entry: true, Description: "设计研究方法"},
			},
		},
		err: fmt.Errorf("resolve: %w", opensciskill.ErrSkillNotFound),
	}
	loader := NewSkillLoad(f)
	definitionBefore, _ := loader.Definition(context.Background())
	// v1 contract used by already frozen routes; description is not identity.
	if tool.DefinitionFingerprint(definitionBefore) != "8fd6dc9ff32f80ad6aadc5761cc61a41c90068b5a8a889075cf58f44eb6d2ab2" {
		t.Fatal("v1 frozen Skill contract changed")
	}
	// Exact arguments from the failed sleep-study opening; wrong section and
	// limit must not transfer to the corrected catalog read.
	result, err := loader.Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{"category":"research","limit":7500,"name":"research","offset":0,"section":"section-0"}`)})
	if err != nil || result.Status != tool.ResultSuccess || strings.Contains(result.Text, "research-workflows") || !strings.Contains(result.Text, "research-methods") || !strings.Contains(result.Text, "是分类") {
		t.Fatalf("bad recovery: %+v %v", result, err)
	}
	var metadata skillLoadResult
	if err = json.Unmarshal(result.Structured, &metadata); err != nil || metadata.Loaded || metadata.Mode != "category" || metadata.Section != "" || metadata.Offset != 0 || len(metadata.Skills) != 1 {
		t.Fatalf("false load/paging evidence: %+v %v", metadata, err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(definitionBefore.OutputSchema, result.Structured); err != nil {
		t.Fatal("frozen output contract changed", err)
	}
	definitionAfter, _ := loader.Definition(context.Background())
	if tool.DefinitionFingerprint(definitionBefore) != tool.DefinitionFingerprint(definitionAfter) || definitionAfter.Version != "1" {
		t.Fatal("runtime recovery changed the frozen contract")
	}
}

func TestSkillSuggestionsFilterCandidatesAndKeepStableOrder(t *testing.T) {
	values := []opensciskill.Info{
		{Name: "research-workflows", Enabled: true, Entry: true, Capability: opensciskill.CapabilityUnavailable},
		{Name: "research-disabled", Entry: true},
		{Name: "research-reference", Enabled: true},
		{Name: "research-bb", Enabled: true, Entry: true, Capability: opensciskill.CapabilityRequiresDependency},
		{Name: "research-aa", Enabled: true, Entry: true, Capability: opensciskill.CapabilityRequiresExternalService},
	}
	if got := strings.Join(skillNameSuggestions("research", values), ","); got != "research-aa,research-bb" {
		t.Fatalf("unsafe/unstable suggestions: %s", got)
	}
	if len(skillNameSuggestions("", values)) != 0 {
		t.Fatal("empty query recommended arbitrary procedures")
	}
}

func TestActualSkillNameTakesPrecedenceOverCategoryAndNeedsNoExtraConfirmation(t *testing.T) {
	f := &recoverySkillFixture{snapshot: opensciskill.Snapshot{
		Categories: []opensciskill.Category{{Name: "research", Count: 1}},
		Skills:     []opensciskill.Info{{Name: "research", Category: "research", Enabled: true, Entry: true}},
	}, chunk: opensciskill.Chunk{Mode: "full", Content: "Actual research Skill instructions"}}
	result, err := NewSkillLoad(f).Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{"name":"research","category":"research"}`)})
	var metadata skillLoadResult
	if err != nil || json.Unmarshal(result.Structured, &metadata) != nil || !metadata.Loaded || metadata.Mode != "skill" || f.loads != 1 || !strings.Contains(result.Text, "Actual research Skill instructions") {
		t.Fatalf("actual Skill silently changed into category: %+v %v", result, err)
	}
}

func TestKnownSkillUsageErrorsAreActionableAndInternalErrorsStayPrivate(t *testing.T) {
	for _, usage := range []*opensciskill.UsageError{
		{Kind: opensciskill.UsageName, Name: "research methods"},
		{Kind: opensciskill.UsageUnavailable, Name: "research-workflows", Reason: "需要 SciAide 会话或 Workflow 宿主能力；对应工具尚未注册"},
		{Kind: opensciskill.UsageDisabled, Name: "research-disabled"},
		{Kind: opensciskill.UsageSection, Name: "research-methods", Section: "section-0", Sections: []opensciskill.InstructionSection{{ID: "section-1"}, {ID: "section-2"}}},
		{Kind: opensciskill.UsageOffset, Name: "research-methods", Section: "section-1", Maximum: 20},
		{Kind: opensciskill.UsageLimit, Name: "research-methods", Maximum: 7500},
	} {
		t.Run(usage.Kind, func(t *testing.T) {
			f := &recoverySkillFixture{err: fmt.Errorf("load: %w", usage)}
			_, err := NewSkillLoad(f).Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{"name":"research-workflows","section":"section-0"}`)})
			var public interface{ UserFacingMessage() string }
			if !errors.As(err, &public) || public.UserFacingMessage() == "工具执行失败" {
				t.Fatalf("generic failure: %v", err)
			}
			if usage.Kind == opensciskill.UsageSection && !strings.Contains(public.UserFacingMessage(), "section-1, section-2") {
				t.Fatal("missing actual section choices")
			}
		})
	}
}

func TestSkillVisibleCategoriesMatchCallableCandidatesNotSettingsCatalog(t *testing.T) {
	f := &recoverySkillFixture{snapshot: opensciskill.Snapshot{
		Categories: []opensciskill.Category{{Name: "blocked-only", Count: 20}, {Name: "research", Count: 2}},
		Skills: []opensciskill.Info{
			{Name: "blocked", Category: "blocked-only", Enabled: true, Entry: true, Capability: opensciskill.CapabilityUnavailable},
			{Name: "research-off", Category: "research", Entry: true},
			{Name: "research-on", Category: "research", Enabled: true, Entry: true},
		},
	}}
	loader := NewSkillLoad(f)
	definition, _ := loader.Definition(context.Background())
	if strings.Contains(definition.Description, "blocked-only") || strings.Contains(definition.Description, "research (2)") || !strings.Contains(definition.Description, "Call with {}") {
		t.Fatalf("definition advertises unavailable choices: %s", definition.Description)
	}
	result, err := loader.Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{}`)})
	if err != nil || strings.Contains(result.Text, "blocked-only") {
		t.Fatalf("catalog mismatch: %s %v", result.Text, err)
	}
	var metadata skillLoadResult
	if err = json.Unmarshal(result.Structured, &metadata); err != nil || metadata.Count != 1 || metadata.Total != 1 || metadata.Loaded {
		t.Fatalf("category counts mismatch: %+v %v", metadata, err)
	}
}

func TestSectionContinuationIncludesSameSectionAndShortPageHasNoGuess(t *testing.T) {
	f := &recoverySkillFixture{chunk: opensciskill.Chunk{Mode: "section", Section: "section-2", Content: "partial", Truncated: true, NextOffset: 100, TotalRunes: 300}}
	result, err := NewSkillLoad(f).Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{"name":"research-methods","section":"section-2","limit":100}`)})
	if err != nil || !strings.Contains(result.Text, `offset=100 and section="section-2"`) {
		t.Fatalf("continuation loses section: %s %v", result.Text, err)
	}
}

type failedResourceFixture struct{ err error }

func (f failedResourceFixture) ReadResource(context.Context, string, string, string, int, int) (opensciskill.Resource, error) {
	return opensciskill.Resource{}, f.err
}
func (f failedResourceFixture) ListResources(context.Context, string, string) (opensciskill.ResourceList, error) {
	return opensciskill.ResourceList{}, f.err
}
func (f failedResourceFixture) LoadStructuredForRun(context.Context, string, string, string, string, string, int, int) (opensciskill.Info, opensciskill.Chunk, bool, error) {
	return opensciskill.Info{}, opensciskill.Chunk{}, false, f.err
}

func TestSkillResourceFailuresUseSharedRecoveryButKeepInternalErrorsPrivate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		public bool
	}{
		{"not-loaded", opensciskill.ErrRunSkillNotFound, true},
		{"wrong-section", &opensciskill.UsageError{Kind: opensciskill.UsageSection, Name: "fixture", Section: "section-0"}, true},
		{"private", errors.New("database not found at private/location"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := failedResourceFixture{err: tc.err}
			for _, call := range []struct {
				implementation tool.Tool
				arguments      string
			}{
				{NewListSkillResources(fixture), `{"name":"fixture"}`},
				{NewReadSkillResource(fixture), `{"name":"fixture","path":"references/method.md"}`},
				{NewReadSkillResource(fixture), `{"name":"fixture","path":"section-0.md"}`},
			} {
				_, err := call.implementation.Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(call.arguments)})
				var public interface{ UserFacingMessage() string }
				if errors.As(err, &public) != tc.public {
					t.Fatalf("wrong error classification: %v", err)
				}
			}
		})
	}
}
