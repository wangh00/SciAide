package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

func stageBudgetContract(input json.RawMessage, prompt string) chat.WorkflowAIExecution {
	return chat.WorkflowAIExecution{WorkflowRunID: "workflow", Input: input, InputSHA256: fmt.Sprintf("%x", sha256.Sum256(input)), PromptText: prompt, PromptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(prompt))), AllowedTools: json.RawMessage(`["builtin.research.task.read"]`), OutputSchema: submissionRepairSchema}
}

func TestStageBudgetSoftLimitCannotDisableEffectiveWindowProjection(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"originalRequest": "unchanged human goal", "context": map[string]any{"markdown": strings.Repeat("full original report. ", 4000)}})
	// Fixed rules exceed the soft threshold by themselves, but leave room
	// for a usable paged input in the actual 80K context window.
	prompt := strings.Repeat("fixed rule. ", 2000) + "\n<stage_input>\n" + string(input) + "\n</stage_input>"
	contract := stageBudgetContract(input, prompt)
	run := chat.Run{ID: "run", UserMessageID: "u"}
	messages := []conversation.Message{checkpointText("u", "run", prompt)}
	defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "read authorized frozen input", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	projected := budgetedStageMessages(messages, run, contract, defs, nil, "", "", "", 80000, 16000)
	text := conversationText(projected[0])
	if text == prompt || !strings.Contains(text, `"$read"`) || !strings.Contains(text, "unchanged human goal") || conversationText(messages[0]) != prompt {
		t.Fatal("soft threshold prevented safe projection or changed original")
	}
	request, _, err := NewContextBuilder(0).buildWithResearchState(context.Background(), projected, "", "u", defs, nil, skill.RunContext{}, "", "", "", ContextLimits{EffectiveTokens: 80000, AutoCompactTokens: 16000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fitFinalRequest(request, nil, nil, 80000, func(r model.ChatRequest) model.ChatRequest { return stageSubmissionRequest(r, contract.OutputSchema) }); err != nil {
		t.Fatal(err)
	}
}

func TestStageBudgetUsesRunMessageBindingWhenPersistedTextDiffers(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"originalRequest": "keep the frozen requirement", "context": map[string]any{"markdown": "short"}})
	prompt := "host\n<stage_input>\n" + string(input) + "\n</stage_input>"
	contract := stageBudgetContract(input, prompt)
	run := chat.Run{ID: "run", UserMessageID: "u"}
	// A retry/recovery path can load an older persisted view while the immutable
	// execution contract remains authoritative. Binding by message identity must
	// replace that view; matching the full text would silently skip the transport
	// projection.
	messages := []conversation.Message{checkpointText("u", "run", "stale persisted stage view")}
	defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "read authorized frozen input", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	got := budgetedStageMessages(messages, run, contract, defs, nil, "", "", "", 80_000, 16_000)
	if len(got) != 1 || conversationText(got[0]) != prompt {
		t.Fatalf("run-bound prompt was not restored: %q", conversationText(got[0]))
	}
}

func TestStageBudgetFitsFirstCallSubmissionAndKeepsCitations(t *testing.T) {
	ref := tool.CitationRef{IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: strings.Repeat("a", 64)}
	old := citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	ref.Reference = citation.KnowledgeReference("run", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	input, _ := json.Marshal(map[string]any{"originalRequest": "original human requirement", "context": map[string]any{"markdown": strings.Repeat("current literal report "+old+"\n", 6000)}, "evidenceContext": []any{map[string]any{"reference": old, "quote": "actual evidence"}}})
	prompt := "host\n<stage_input>\n" + strings.ReplaceAll(string(input), old, ref.Reference) + "\n</stage_input>"
	contract := stageBudgetContract(input, prompt)
	run := chat.Run{ID: "run", UserMessageID: "u"}
	messages := []conversation.Message{checkpointText("u", "run", prompt)}
	defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "read current records", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	calls := []tool.Call{{ID: "seed", RunID: "run", ToolName: citation.WorkflowSeedToolName, ProviderCallID: "seed", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}}}
	projected := budgetedStageMessages(messages, run, contract, defs, calls, strings.Repeat("host", 2000), "", "", 80_000, 72_000)
	text := conversationText(projected[0])
	if text == prompt || !strings.Contains(text, "original human requirement") || !strings.Contains(text, `"`+old+`":"`+ref.Reference+`"`) {
		t.Fatal("projection/citation map missing")
	}
	if conversationText(messages[0]) != prompt {
		t.Fatal("original message changed")
	}
	request, info, err := NewContextBuilder(0).buildWithResearchState(context.Background(), projected, "", "u", defs, calls, skill.RunContext{}, "", strings.Repeat("host", 2000), "", ContextLimits{EffectiveTokens: 80_000, AutoCompactTokens: 72_000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fitFinalRequest(request, calls, nil, 80_000, func(r model.ChatRequest) model.ChatRequest { return stageSubmissionRequest(r, contract.OutputSchema) }); err != nil {
		t.Fatal(err)
	}
	// Correction reuses the same material view; final admission counts its growth.
	corrected := projected
	repaired, _, repairErr := NewContextBuilder(0).buildWithResearchState(context.Background(), corrected, "", "u", defs, calls, skill.RunContext{}, "", strings.Repeat("host", 2000), "", ContextLimits{EffectiveTokens: 80000, AutoCompactTokens: 72000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
	if repairErr != nil {
		t.Fatal(repairErr)
	}
	repaired.Messages = append(repaired.Messages, model.Message{Role: model.RoleUser, Content: strings.Repeat("diagnostic", 2500)})
	if _, repairErr = fitFinalRequest(repaired, calls, nil, 80000, func(r model.ChatRequest) model.ChatRequest { return stageSubmissionRequest(r, contract.OutputSchema) }); repairErr != nil {
		t.Fatal("repair lost input budget", repairErr)
	}
	if got := budgetedStageMessages(messages, run, contract, nil, calls, "", "", "", 80_000, 72_000); conversationText(got[0]) != prompt {
		t.Fatal("reader permission widened")
	}
	lowAuto := budgetedStageMessages(messages, run, contract, defs, calls, strings.Repeat("host", 2000), "", "", 80_000, 1000)
	if conversationText(lowAuto[0]) == prompt {
		t.Fatal("soft automatic threshold blocked effective-budget projection")
	}
	contract.InputSHA256 = "invalid"
	if got := budgetedStageMessages(messages, run, contract, defs, calls, "", "", "", 80_000, 72_000); conversationText(got[0]) != prompt {
		t.Fatal("unbound input projected")
	}
	t.Logf("original=%d projected=%d first=%d budget=80000", len([]rune(prompt)), len([]rune(text)), info.EstimatedTokens)
}
func TestTaskPagePreviewNeverSkipsHiddenCharacters(t *testing.T) {
	content := strings.Repeat("中文😀\"\\\n", 1000)
	page, _ := json.Marshal(map[string]any{"section": "stage_input", "offset": 123, "content": content, "nextOffset": 123 + len([]rune(content)), "sha256": strings.Repeat("a", 64), "totalCharacters": 20_000, "hasMore": true})
	original := tool.BuildModelContextSnapshot("read", "", tool.Result{Status: tool.ResultSuccess, Structured: page})
	for limit := 200; limit <= 800; limit++ {
		text := truncateToolContext(original, limit)
		if len([]rune(text)) > limit || !json.Valid([]byte(text)) {
			t.Fatal("invalid page preview", limit, text)
		}
		var value struct {
			ContentNotShown bool
			RetryOffset     int
			Structured      struct {
				Offset, NextOffset int
				Content            string
			}
		}
		_ = json.Unmarshal([]byte(text), &value)
		if value.ContentNotShown {
			if value.RetryOffset != 123 {
				t.Fatal("missing retry offset")
			}
			continue
		}
		p := value.Structured
		if p.Offset != 123 || p.NextOffset <= p.Offset || p.NextOffset != 123+len([]rune(p.Content)) || !strings.HasPrefix(content, p.Content) {
			t.Fatal("preview advances over omitted content")
		}
	}
}
func TestCapturedCurrentReviewInputBudgetReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_STAGE_BUDGET_CAPTURE")
	if dir == "" {
		t.Skip("optional task-scoped captured input")
	}
	read := func(name string) []byte {
		v, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	var row struct {
		Input string `json:"input_json"`
	}
	if json.Unmarshal(read("independent_review-latest.json"), &row) != nil {
		t.Fatal("bad input capture")
	}
	var exec struct {
		Prompt string `json:"prompt_text"`
		Schema string `json:"output_schema_json"`
	}
	if json.Unmarshal(read("review-failure-execution.json"), &exec) != nil {
		t.Fatal("bad execution capture")
	}
	contract := stageBudgetContract(json.RawMessage(row.Input), exec.Prompt)
	contract.OutputSchema = json.RawMessage(exec.Schema)
	messages := []conversation.Message{checkpointText("u", "run", exec.Prompt)}
	defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "read original stage", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	// No HTTP body was saved: the 40K fixed footprint is explicitly synthetic,
	// while the prompt, stage data and output schema are exact captured bytes.
	fixed := strings.Repeat("s", 40_000)
	projected := budgetedStageMessages(messages, chat.Run{ID: "run", UserMessageID: "u"}, contract, defs, nil, fixed, "", "", 190_000, 180_000)
	build := func(m []conversation.Message) (model.ChatRequest, ContextBuildInfo, error) {
		return NewContextBuilder(0).buildWithResearchState(context.Background(), m, "", "u", defs, nil, skill.RunContext{}, "", fixed, "", ContextLimits{EffectiveTokens: 190_000, AutoCompactTokens: 180_000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
	}
	if _, _, e := build(messages); e == nil {
		t.Fatal("original did not reproduce overflow")
	}
	request, info, e := build(projected)
	if e != nil {
		t.Fatal(e)
	}
	final, e := fitFinalRequest(request, nil, nil, 190_000, func(r model.ChatRequest) model.ChatRequest {
		return stageSubmissionRequest(r, workflow.ReviewSubmissionSchema(contract.OutputSchema))
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("captured prompt=%d projected=%d first=%d final=%d effective=190000 (200K default window); fixed=40000 synthetic; no model called", len([]rune(exec.Prompt)), len([]rune(conversationText(projected[0]))), info.EstimatedTokens, estimateRequestTokens(final))
}

func TestLatestTaskPageSurvivesManyOldPreviews(t *testing.T) {
	page, _ := json.Marshal(map[string]any{"section": "stage_input", "offset": 42, "nextOffset": 4042, "content": strings.Repeat("x", 4000), "sha256": strings.Repeat("a", 64)})
	current := tool.BuildModelContextSnapshot("latest", "", tool.Result{Status: tool.ResultSuccess, Structured: page})
	values := []string{}
	for i := 0; i < 30; i++ {
		values = append(values, strings.Repeat("older evidence ", 1000))
	}
	values = append(values, current)
	fitted := fitToolContexts(values, 14000)
	if fitted[len(fitted)-1] != current || toolContextTokens(fitted) > 14000 {
		t.Fatal("latest requested page hidden despite available budget")
	}
}

func TestStageInputPagingAllowanceIsBoundedAndCannotResetExploration(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"text": strings.Repeat("x", 2000)})
	c := stageBudgetContract(input, "host")
	c.NodeID = "review"
	calls := []tool.Call{}
	page := func(start, end int, sha, node string) tool.Call {
		args, _ := json.Marshal(map[string]any{"section": "stage_input", "offset": start, "nodeId": node})
		data, _ := json.Marshal(map[string]any{"section": "stage_input", "offset": start, "nextOffset": end, "content": string([]rune(string(input))[start:end]), "sha256": sha})
		return tool.Call{ToolName: workflow.ResearchTaskReadTool, Arguments: args, Result: &tool.Result{Status: tool.ResultSuccess, Structured: data}}
	}
	calls = append(calls, page(0, 100, c.InputSHA256, ""), page(0, 100, c.InputSHA256, ""), page(50, 150, c.InputSHA256, ""), page(100, 200, "wrong", ""), page(100, 200, c.InputSHA256, "foreign"), page(100, 200, c.InputSHA256, "review"))
	if stageInputTransportTurns(c, calls) != 2 {
		t.Fatal("repeated/foreign reads extended budget")
	}
	_, final := resourceStageProgress(14-stageInputTransportTurns(c, calls), true, calls)
	if !final {
		t.Fatal("actual exploration budget changed")
	}
	partial := []tool.Call{page(0, 1000, c.InputSHA256, ""), page(600, 1600, c.InputSHA256, "")}
	if stageInputTransportTurns(c, partial) != 2 {
		t.Fatal("legitimate overlap after shortened preview consumed exploration")
	}
	for i := 200; i < 600; i++ {
		calls = append(calls, page(i, i+1, c.InputSHA256, ""))
	}
	if stageInputTransportTurns(c, calls) != 256 {
		t.Fatal("transport allowance unbounded")
	}
}
