package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestWorkflowProtocolRolloverContinuesToolsOverHTTP(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				bodies = append(bodies, string(body))
				n := len(bodies)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
				if n <= 2 {
					callID := fmt.Sprintf("call-%d", n)
					args := fmt.Sprintf(`{"actionId":"res_%d"}`, n)
					if protocol == modelcap.ProtocolOpenAIResponses {
						emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "reasoning", "id": "rs", "summary": []any{}, "encrypted_content": strings.Repeat("opaque", 16000)}})
						emit(map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"type": "function_call", "id": callID, "call_id": callID, "name": "resource_open", "arguments": args}})
						emit(map[string]any{"type": "response.completed", "response": map[string]any{}})
					} else {
						emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": "private"}})
						emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": strings.Repeat("opaque", 16000)}})
						emit(map[string]any{"type": "content_block_stop", "index": 0})
						emit(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": callID, "name": "resource_open", "input": json.RawMessage(args)}})
						emit(map[string]any{"type": "content_block_stop", "index": 1})
						emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}})
						emit(map[string]any{"type": "message_stop"})
					}
					return
				}
				text := `{"items":["both resources verified"],"state":"ready"}`
				if protocol == modelcap.ProtocolOpenAIResponses {
					emit(map[string]any{"type": "response.output_text.delta", "delta": text})
					emit(map[string]any{"type": "response.completed", "response": map[string]any{}})
				} else {
					emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": text}})
					emit(map[string]any{"type": "content_block_stop", "index": 0})
					emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}})
					emit(map[string]any{"type": "message_stop"})
				}
			}))
			defer server.Close()
			loop, state, _ := newLoopFixture(t, nil)
			input := strings.Repeat("frozen-code-line\n", 7000)
			state.messages[0].Parts[0].Text = input
			state.workflowAI = true
			definition := tool.Definition{QualifiedName: resource.OpenTool, Description: "read", InputSchema: json.RawMessage(`{"type":"object","required":["actionId"],"properties":{"actionId":{"type":"string"}}}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
			executions := 0
			err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(i tool.Invocation) tool.Result {
				executions++
				return tool.Result{Status: tool.ResultSuccess, Text: "verified evidence " + string(i.Arguments)}
			}})
			if err != nil {
				t.Fatal(err)
			}
			loop.resources = &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{definition}, Context: "issued resources", OpenIDs: map[string]bool{"res_1": true, "res_2": true}}}
			loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", AllowedToolNames: []string{resource.OpenTool}, SystemContext: "discussion-only-state", ExecutionSystemContext: "execution-policy", ExecutionDynamicState: "current-stage", StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
			p := modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}
			var provider model.ChatModel = responses.New(p, nil)
			if protocol == modelcap.ProtocolAnthropic {
				provider = anthropic.New(p, nil)
			}
			loop.models = protocolResolver{model: provider, protocol: protocol}
			if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeCompleted {
				t.Fatalf("outcome=%s error=%s", outcome, state.run.ErrorMessage)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 3 || executions != 2 || len(state.calls) != 2 || state.run.ModelTurns != 3 || !state.run.ContextCompacted {
				t.Fatalf("requests=%d executions=%d calls=%d turns=%d compacted=%v", len(bodies), executions, len(state.calls), state.run.ModelTurns, state.run.ContextCompacted)
			}
			for _, body := range bodies[1:] {
				if strings.Contains(body, "opaque") || strings.Contains(body, "discussion-only-state") || !strings.Contains(body, "verified evidence") || !strings.Contains(body, "execution-policy") || !strings.Contains(body, "fresh research tool exchange") || !strings.Contains(body, "resource_open") || strings.Contains(body, "submit_stage_result") {
					t.Fatal("rollover lost evidence/tools or replayed native state")
				}
			}
			if len(state.providerTurns) < 2 || !strings.Contains(string(state.providerTurns[0].Items[0].Payload), "opaque") {
				t.Fatal("original protocol audit not retained")
			}
		})
	}
}
