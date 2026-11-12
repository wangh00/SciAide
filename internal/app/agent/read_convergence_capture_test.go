package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

// Replays saved public tool results through production preview/admission code.
// It is NOT a packet capture of old provider requests or a live-model replay.
func TestCapturedLongReadLoopConvergesUsingFittedPageEvidence(t *testing.T) {
	dir := os.Getenv("SCIAIDE_READ_LOOP_CAPTURE")
	if dir == "" {
		t.Skip("no captured long read loop supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "read-loop.capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Contract struct {
			ID      string `json:"id"`
			NodeID  string `json:"node_id"`
			Input   string `json:"input_json"`
			SHA     string `json:"input_sha256"`
			Prompt  string `json:"prompt_text"`
			Schema  string `json:"output_schema_json"`
			Version string `json:"prompt_version"`
		}
		Calls []struct {
			ID         string `json:"id"`
			RunID      string `json:"run_id"`
			ProviderID string `json:"provider_call_id"`
			Name       string `json:"tool_name"`
			Arguments  string `json:"arguments_json"`
		}
		ToolEvents []struct {
			Payload string `json:"payload_json"`
		}
		Turns []struct {
			Index int `json:"turn_index"`
		}
	}
	if json.Unmarshal(data, &capture) != nil {
		t.Fatal("invalid capture")
	}
	keyData, err := os.ReadFile(filepath.Join(dir, "idempotency-keys.capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	if json.Unmarshal(keyData, &keys) != nil {
		t.Fatal("invalid keys")
	}
	results := map[string]*tool.Result{}
	for _, e := range capture.ToolEvents {
		var event struct {
			ToolCall struct {
				ID     string
				Result *tool.Result
			}
		}
		if json.Unmarshal([]byte(e.Payload), &event) != nil {
			t.Fatal("invalid public tool event")
		}
		results[event.ToolCall.ID] = event.ToolCall.Result
	}
	contract := chat.WorkflowAIExecution{ID: capture.Contract.ID, WorkflowStepID: "captured-step", NodeID: capture.Contract.NodeID, PromptVersion: capture.Contract.Version, Input: json.RawMessage(capture.Contract.Input), InputSHA256: capture.Contract.SHA, OutputSchema: json.RawMessage(capture.Contract.Schema)}
	byTurn := map[int][]tool.Call{}
	chatID := ""
	for _, c := range capture.Calls {
		prefix := "agent-tool:" + c.RunID + ":"
		if !strings.HasPrefix(keys[c.ID], prefix) || results[c.ID] == nil {
			continue
		}
		indexText, _, _ := strings.Cut(strings.TrimPrefix(keys[c.ID], prefix), ":")
		index, err := strconv.Atoi(indexText)
		if err != nil {
			t.Fatal(err)
		}
		chatID = c.RunID
		byTurn[index] = append(byTurn[index], tool.Call{ID: c.ID, RunID: c.RunID, ProviderCallID: c.ProviderID, ToolName: c.Name, Arguments: json.RawMessage(c.Arguments), Result: results[c.ID]})
	}
	g := newStageReadConvergence(contract, chatID)
	if g == nil {
		t.Fatal("frozen capture identity invalid")
	}
	var calls []tool.Call
	var pending []model.ToolCall
	for turn := 1; turn <= len(capture.Turns); turn++ {
		messages, _, _, _, err := newestToolMessages(calls, 380000, maxToolContextTokens)
		if err != nil {
			t.Fatal(err)
		}
		req := model.ChatRequest{Messages: append([]model.Message{{Role: model.RoleSystem, Content: "Frozen captured stage replay."}, {Role: model.RoleUser, Content: capture.Contract.Prompt, HostToolReferences: true}}, messages...), Tools: []model.ToolDefinition{{Name: workflow.ResearchTaskReadTool, InputSchema: json.RawMessage(`{"type":"object","properties":{"section":{"type":"string","enum":["stage_input","planning_discussion"]}}}`)}}}
		req, err = fitFinalRequest(req, calls, nil, 380000, func(r model.ChatRequest) model.ChatRequest { return r })
		if err != nil {
			t.Fatal(err)
		}
		if g.inspect(req, pending, calls) {
			submit, _, err := g.admitSubmission(req, calls, nil, 380000, contract.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			if submit.ForcedTool != stageSubmissionTool || turn >= len(capture.Turns)-4 || len(submit.ProviderTurns) != 0 {
				t.Fatal("captured loop was not safely shortened", turn)
			}
			t.Logf("captured %d turns; fitted public-tool replay enters submission on turn %d after %d no-new-visible-material read turns; original requests/native reasoning not replayed", len(capture.Turns), turn, g.streak)
			return
		}
		g.commit(req, calls)
		pending = nil
		for _, call := range byTurn[turn] {
			calls = append(calls, call)
			pending = append(pending, model.ToolCall{ID: call.ProviderCallID, Name: call.ToolName, Arguments: call.Arguments})
		}
	}
	t.Fatal("captured repeated-read sequence did not converge")
}
