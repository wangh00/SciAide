package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

func TestCapturedReviewContinuityFitsSubmissionAndCorrection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_DEDUP_DERIVED")
	if dir == "" {
		t.Skip("optional scoped derived capture")
	}
	read := func(n string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, n))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	input, prompt, schema := json.RawMessage(read("input.json")), string(read("prompt.txt")), json.RawMessage(read("schema.json"))
	contract := stageBudgetContract(input, prompt)
	contract.OutputSchema = schema
	defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "read original stage", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	// This fixed host footprint is synthetic, not a captured commercial HTTP
	// body. The current report, prior verdict and actual prompt builder are real.
	fixed := strings.Repeat("s", 40_000)
	messages := []conversation.Message{checkpointText("u", "run", prompt)}
	projected := budgetedStageMessages(messages, chat.Run{ID: "run", UserMessageID: "u"}, contract, defs, nil, fixed, "", "", 190_000, 180_000)
	r, info, e := NewContextBuilder(0).buildWithResearchState(context.Background(), projected, "", "u", defs, nil, skill.RunContext{}, "", fixed, "", ContextLimits{EffectiveTokens: 190_000, AutoCompactTokens: 180_000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
	if e != nil {
		t.Fatal(e)
	}
	final, e := fitFinalRequest(r, nil, nil, 190_000, func(r model.ChatRequest) model.ChatRequest {
		return stageSubmissionRequest(r, workflow.ReviewSubmissionSchema(schema))
	})
	if e != nil {
		t.Fatal(e)
	}
	r.Messages = append(r.Messages, model.Message{Role: model.RoleAssistant, Content: string(read("candidate.json"))}, model.Message{Role: model.RoleUser, Content: strings.Repeat("Correction diagnostic; preserve original material. ", 100)})
	corrected, e := fitFinalRequest(r, nil, nil, 190_000, func(r model.ChatRequest) model.ChatRequest {
		return stageSubmissionRequest(r, workflow.ReviewSubmissionSchema(schema))
	})
	if e != nil {
		t.Fatal("correction overflow", e)
	}
	if conversationText(messages[0]) != prompt {
		t.Fatal("original changed")
	}
	t.Logf("real revised prompt chars=%d first=%d final=%d correction=%d; effective=190000, fixed=40000 synthetic; no model", len([]rune(prompt)), info.EstimatedTokens, estimateRequestTokens(final), estimateRequestTokens(corrected))
}
