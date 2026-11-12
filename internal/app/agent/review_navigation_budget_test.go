package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

// Optional actual revised prompt with the earlier incident's observed fixed
// context footprint. This is offline budget fitting, not a live wire replay.
func TestCapturedEvidenceNavigationPromptFitsOriginalBudgetAndSubmission(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVISION_OPTIMIZATION_REPLAY")
	if dir == "" {
		t.Skip("no scoped navigation capture supplied")
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "derived-review-prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(dir, "budget-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Effective int             `json:"effective"`
		Auto      int             `json:"auto"`
		Fixed     int             `json:"fixed"`
		Schema    json.RawMessage `json:"schema"`
		Calls     []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Context   string `json:"context"`
			Text      string `json:"text"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(encoded, &capture); err != nil || capture.Effective != 258400 || capture.Auto != 244800 || capture.Fixed != 32891 {
		t.Fatal("original model limits/observed host footprint changed", err)
	}
	if !strings.Contains(string(prompt), "evidence-navigation-v1") || !strings.Contains(string(prompt), "review-scope-v4") || len([]rune(string(prompt))) < 213006 {
		t.Fatal("missing complete derived prompt")
	}
	calls := []tool.Call{}
	for _, c := range capture.Calls {
		calls = append(calls, tool.Call{ID: c.ID, ProviderCallID: c.ID, ToolName: c.Name, Arguments: json.RawMessage(c.Arguments), ModelContext: c.Context, ModelContextVersion: tool.ModelContextSnapshotVersion, Result: &tool.Result{Status: tool.ResultSuccess, Text: c.Text}})
	}
	limits := ContextLimits{EffectiveTokens: capture.Effective, AutoCompactTokens: capture.Auto, CurrentUserHostToolReferences: true}
	fixed := strings.Repeat("s", capture.Fixed-len([]rune(fixedSystemRules))-2)
	request, info, err := NewContextBuilder(0).buildWithResearchState(context.Background(), []conversation.Message{checkpointText("u", "run", string(prompt))}, "", "u", nil, calls, skill.RunContext{}, "", fixed, "", limits, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal("new navigation reintroduced current-input overflow", err)
	}
	if info.EstimatedTokens > capture.Effective || request.Messages[1].Content != string(prompt) || len(request.Messages) != 2+2*len(calls) || info.CompactedThroughMessageID != "" {
		t.Fatal("new navigation lost complete prompt or protocol", info)
	}
	final, err := fitFinalRequest(request, calls, nil, capture.Effective, func(r model.ChatRequest) model.ChatRequest {
		return stageSubmissionRequest(r, capture.Schema)
	})
	if err != nil || final.Messages[1].Content != string(prompt) {
		t.Fatal("new prompt cannot submit complete review schema", err, apperr.Public(err), info.EstimatedTokens)
	}
	t.Logf("new prompt=%d; observed fixed footprint=%d (synthetic); estimate=%d; original soft/hard budgets=%d/%d; completed calls=%d; full-schema submission fits", len([]rune(string(prompt))), capture.Fixed, info.EstimatedTokens, capture.Auto, capture.Effective, len(calls))
}
