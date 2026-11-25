package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/modelutil"
	w "github.com/wangh00/SciAide/internal/transport/wails"
)

type ledgerFixtureCall struct{ Name, Args string }

// No usage is reported: this exercises the unchanged conservative character
// budget, not a favorable synthetic token ratio. All model HTTP is localhost.
func emitToolLedgerFixture(t *testing.T, wr http.ResponseWriter, protocol modelprofile.APIProtocol, turn int, calls []ledgerFixtureCall) {
	t.Helper()
	wr.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) { data, _ := json.Marshal(v); fmt.Fprintf(wr, "data: %s\n\n", data) }
	if protocol == modelprofile.ProtocolOpenAIChat {
		delta, finish := map[string]any{"content": "Saved history reread; no operation repeated or scientific conclusion approved."}, "stop"
		if len(calls) > 0 {
			items := []any{}
			for i, c := range calls {
				items = append(items, map[string]any{"index": i, "id": fmt.Sprintf("ledger-%d-%d", turn, i), "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Args}})
			}
			delta, finish = map[string]any{"tool_calls": items}, "tool_calls"
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
		_, _ = io.WriteString(wr, "data: [DONE]\n\n")
		return
	}
	if protocol == modelprofile.ProtocolOpenAIResponses {
		index := 0
		if turn == 1 {
			send(map[string]any{"type": "response.output_item.done", "output_index": index, "item": map[string]any{"type": "reasoning", "id": "ledger-reason", "encrypted_content": "PRIVATE_NATIVE_BLOCK" + strings.Repeat("x", 100000)}})
			index++
		}
		for i, c := range calls {
			send(map[string]any{"type": "response.output_item.done", "output_index": index, "item": map[string]any{"type": "function_call", "id": fmt.Sprintf("item-%d-%d", turn, i), "call_id": fmt.Sprintf("ledger-%d-%d", turn, i), "name": c.Name, "arguments": c.Args}})
			index++
		}
		if len(calls) == 0 {
			send(map[string]any{"type": "response.output_text.delta", "delta": "Saved history reread; no operation repeated or scientific conclusion approved."})
		}
		send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
		return
	}
	send(map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprint("ledger-", turn), "role": "assistant", "content": []any{}}})
	index := 0
	if turn == 1 {
		send(map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "redacted_thinking", "data": "PRIVATE_NATIVE_BLOCK" + strings.Repeat("x", 100000)}})
		send(map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	for i, c := range calls {
		var args any
		_ = json.Unmarshal([]byte(c.Args), &args)
		send(map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("ledger-%d-%d", turn, i), "name": c.Name, "input": args}})
		send(map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	reason := "tool_use"
	if len(calls) == 0 {
		reason = "end_turn"
		send(map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": "Saved history reread; no operation repeated or scientific conclusion approved."}})
		send(map[string]any{"type": "content_block_stop", "index": index})
	}
	send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}})
	send(map[string]any{"type": "message_stop"})
}

func ledgerWireStrings(v any) []string {
	result := []string{}
	switch value := v.(type) {
	case string:
		result = append(result, value)
	case []any:
		for _, item := range value {
			result = append(result, ledgerWireStrings(item)...)
		}
	case map[string]any:
		for _, item := range value {
			result = append(result, ledgerWireStrings(item)...)
		}
	}
	return result
}

func ledgerWirePage(values []string, view string, offset int) map[string]json.RawMessage {
	for _, text := range values {
		_, tail, found := strings.Cut(text, "\nResult:\n")
		if !found {
			continue
		}
		tail = strings.TrimSuffix(tail, "\n</untrusted_conversation_content>")
		var envelope struct{ Structured map[string]json.RawMessage }
		if json.Unmarshal([]byte(tail), &envelope) != nil {
			continue
		}
		var gotView string
		var gotOffset int
		_ = json.Unmarshal(envelope.Structured["view"], &gotView)
		_ = json.Unmarshal(envelope.Structured["offset"], &gotOffset)
		if gotView == view && gotOffset == offset {
			return envelope.Structured
		}
	}
	return nil
}

func ledgerStoredNative(t *testing.T, app *Application, runID string, protocol modelprofile.APIProtocol) string {
	t.Helper()
	if protocol == modelprofile.ProtocolOpenAIChat {
		var count int
		if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM provider_turn_items WHERE run_id=?`, runID).Scan(&count); err != nil || count != 0 {
			t.Fatal("normalized protocol invented native audit", count, err)
		}
		return ""
	}
	var payload string
	if err := app.store.DB().QueryRow(`SELECT payload_json FROM provider_turn_items WHERE run_id=? AND turn_index=1 AND instr(payload_json,'PRIVATE_NATIVE_BLOCK')>0`, runID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	_ = json.Unmarshal([]byte(payload), &item)
	key := "encrypted_content"
	if protocol == modelprofile.ProtocolAnthropic {
		key = "data"
	}
	if item[key] != "PRIVATE_NATIVE_BLOCK"+strings.Repeat("x", 100000) {
		t.Fatal("original native audit was changed or discarded")
	}
	return payload
}

func TestToolHistoryOriginalApplicationWindowAndRereadRestart(t *testing.T) {
	baseline := os.Getenv("SCIAIDE_TOOL_LEDGER_BASELINE") == "1"
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, action := range []string{"complete", "cancel", "shutdown"} {
			if baseline && action != "complete" {
				continue
			}
			t.Run(string(protocol)+"/"+action, func(t *testing.T) {
				batches := 1
				if protocol == modelprofile.ProtocolOpenAIChat {
					batches = 2
				}
				maxRequests, sourceCalls := batches+4, 32*batches
				entered, disconnected := make(chan struct{}, 1), make(chan struct{}, 1)
				release := make(chan struct{})
				var mu sync.Mutex
				var wire [][]byte
				var snapshots []modelutil.DebugRequestSnapshot
				var originalRef contextmemory.ToolLedgerRef
				firstID, readText := "", ""
				admissions := 0
				server := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					defer mu.Unlock()
					wire = append(wire, body)
					n := len(wire)
					step := n - batches + 1
					var root any
					_ = json.Unmarshal(body, &root)
					values := ledgerWireStrings(root)
					calls := []ledgerFixtureCall{}
					if action != "complete" && step == 3 {
						if ledgerWirePage(values, "index", 0) == nil {
							t.Error("active ledger index lost before cancellation")
						}
						entered <- struct{}{}
						select {
						case <-r.Context().Done():
							disconnected <- struct{}{}
						case <-release:
						}
						return
					}
					if n <= batches {
						if n == 1 && strings.Contains(string(body), "context_tool_history") {
							t.Error("ledger definition enlarged the original first request")
						}
						for i := 0; i < 32; i++ {
							query := fmt.Sprintf("原始查询%02d builtin.workspace.read_text 🧪 ", 32*(n-1)+i)
							query += strings.Repeat("q", 300-len([]rune(query)))
							args, _ := json.Marshal(map[string]any{"query": query, "server": strings.Repeat("s", 128), "limit": 1})
							calls = append(calls, ledgerFixtureCall{"tools_search", string(args)})
						}
					} else if baseline || n > maxRequests {
						t.Error("baseline retried or candidate issued an unplanned request", n)
					} else {
						if !strings.Contains(string(body), "context_tool_history") || strings.Contains(string(body), "PRIVATE_NATIVE_BLOCK") {
							t.Error("reader not exposed only at the independent exchange, or private native state replayed")
						}
						if step == 2 {
							for _, text := range values {
								if _, tail, ok := strings.Cut(text, "<tool_history_ledger>"); ok {
									ref, _, _ := strings.Cut(tail, "</tool_history_ledger>")
									_ = json.Unmarshal([]byte(ref), &originalRef)
								}
							}
							if originalRef.Count != sourceCalls-8 {
								t.Error("archive did not retain exactly 8 ordinary calls", originalRef.Count)
							}
						}
						args := map[string]any{"throughCallId": originalRef.ThroughCallID, "ledgerSha256": originalRef.SHA256}
						switch step {
						case 2:
							args["view"] = "index"
						case 3:
							page := ledgerWirePage(values, "index", 0)
							var entries []contextmemory.ToolLedgerEntry
							_ = json.Unmarshal(page["entries"], &entries)
							if len(entries) != 4 {
								t.Error("original index page not fully visible", len(entries))
								for _, text := range values {
									if strings.Contains(text, "Result:") && strings.Contains(text, "tool_history") {
										t.Log("synthetic history evidence:", tool.SafeActivityText(text, 700))
									}
								}
							} else {
								firstID = entries[0].CallID
							}
							args["view"], args["callId"], args["maxChars"] = "arguments", firstID, 200
						case 4:
							page := ledgerWirePage(values, "arguments", 0)
							var next int
							_ = json.Unmarshal(page["text"], &readText)
							_ = json.Unmarshal(page["nextOffset"], &next)
							if next != 200 || len([]rune(readText)) != 200 {
								t.Error("first exact argument page/continuation lost", next)
							}
							args["view"], args["callId"], args["offset"], args["maxChars"] = "arguments", firstID, next, 1000
						case 5:
							page := ledgerWirePage(values, "arguments", 200)
							var tail string
							var next int
							_ = json.Unmarshal(page["text"], &tail)
							_ = json.Unmarshal(page["nextOffset"], &next)
							if next != -1 || tail == "" {
								t.Error("second argument page was not complete", next)
							}
							readText += tail
						}
						if step < 5 {
							encoded, _ := json.Marshal(args)
							calls = append(calls, ledgerFixtureCall{"context_tool_history", string(encoded)})
						}
					}
					emitToolLedgerFixture(t, wr, protocol, n, calls)
				}))
				defer server.Close()
				defer close(release)
				observer, _ := modelutil.NewRequestSnapshotObserver(func(v modelutil.DebugRequestSnapshot) { mu.Lock(); snapshots = append(snapshots, v); mu.Unlock() })
				root := t.TempDir()
				app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true, DebugRequestSnapshotObserver: observer, ModelRequestGate: func(context.Context) error {
					mu.Lock()
					defer mu.Unlock()
					admissions++
					if admissions > maxRequests+1 {
						return fmt.Errorf("fixture request cap")
					}
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				app.Startup(context.Background())
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "ledger fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: "fixture-model", Models: []modelprofile.ProfileModel{{ID: "fixture-model", Enabled: true, IsDefault: true, ContextWindowTokens: 48000, AutoCompactTokenLimit: 43200, ContextWindowSource: "manual"}}, Enabled: true, IsDefault: true, TimeoutSeconds: 10})
				if err != nil {
					t.Fatal(err)
				}
				project, err := app.ProjectFacade.CreateProject(w.CreateProjectRequest{Name: "isolated history fixture"})
				if err != nil {
					t.Fatal(err)
				}
				conv, err := app.ConversationFacade.CreateConversation(w.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: "fixture-model"})
				if err != nil {
					t.Fatal(err)
				}
				run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conv.ID, ModelProfileID: profile.ID, ModelID: "fixture-model", Text: "Synthetic history test only: inspect configured MCP metadata, then reread a saved argument without repeating tools. Do not fetch sources, execute commands or approve research."})
				if err != nil {
					t.Fatal(err)
				}
				if action != "complete" {
					select {
					case <-entered:
					case <-time.After(15 * time.Second):
						t.Fatal("original loop never reached the active ledger cancellation point")
					}
					if action == "cancel" {
						if err = app.ChatFacade.CancelRun(run.ID); err != nil {
							t.Fatal(err)
						}
					} else {
						app.BeginShutdown()
					}
					select {
					case <-disconnected:
					case <-time.After(5 * time.Second):
						t.Fatal("host control did not disconnect the active ledger request")
					}
				}
				deadline := time.Now().Add(45 * time.Second)
				var result w.RunSnapshot
				for {
					result, err = app.ChatFacade.GetRunSnapshot(run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if result.Run.Status == chat.RunCompleted || result.Run.Status == chat.RunFailed || result.Run.Status == chat.RunCancelled || result.Run.Status == chat.RunInterrupted {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("original Application did not stop", result.Run.Status)
					}
					time.Sleep(10 * time.Millisecond)
				}
				wantRequests, wantCalls := maxRequests, sourceCalls+3
				wantStatus := chat.RunCompleted
				if baseline {
					wantRequests, wantCalls = batches, sourceCalls
					wantStatus = chat.RunFailed
				}
				if action != "complete" {
					wantRequests, wantCalls = batches+2, sourceCalls+1
					wantStatus = chat.RunCancelled
					if action == "shutdown" {
						wantStatus = chat.RunInterrupted
					}
				}
				if len(result.ToolCalls) != wantCalls || result.Run.Status != wantStatus || baseline && result.Run.ErrorCode != "CONTEXT_BUILD_FAILED" {
					t.Fatal("original flow did not follow bounded history contract", result.Run.Status, result.Run.ErrorCode, result.Run.ErrorMessage, len(result.ToolCalls))
				}
				originalModelViews := map[string]string{}
				matchedRead := false
				for _, c := range result.ToolCalls {
					if c.Result == nil || c.Status != tool.CallCompleted || c.Result.Status != tool.ResultSuccess {
						t.Fatal("original tool failed or invented completion", c.ToolName, c.Status, c.ErrorCode, c.ErrorMessage)
					}
					if c.ID == firstID {
						matchedRead = true
						if string(c.Arguments) != readText {
							t.Fatal("wire reread changed saved argument bytes")
						}
					}
					var snapshot string
					if err := app.store.DB().QueryRow(`SELECT model_context_text FROM tool_results WHERE tool_call_id=?`, c.ID).Scan(&snapshot); err != nil {
						t.Fatal(err)
					}
					originalModelViews[c.ID] = snapshot
				}
				if action == "complete" && !baseline && (!matchedRead || firstID == "" || readText == "") {
					t.Fatal("no actual old argument was reread")
				}
				nativeOriginal := ledgerStoredNative(t, app, run.ID, protocol)
				if err = app.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true})
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				reopened.Startup(context.Background())
				after, err := reopened.ChatFacade.GetRunSnapshot(run.ID)
				if err != nil || after.Run.Status != result.Run.Status || len(after.ToolCalls) != wantCalls {
					t.Fatal("restart changed durable history", err)
				}
				for id, saved := range originalModelViews {
					var got string
					if err := reopened.store.DB().QueryRow(`SELECT model_context_text FROM tool_results WHERE tool_call_id=?`, id).Scan(&got); err != nil || got != saved {
						t.Fatal("original model-view snapshot changed at restart", err)
					}
				}
				if ledgerStoredNative(t, reopened, run.ID, protocol) != nativeOriginal {
					t.Fatal("restart changed original native bytes")
				}
				var usageRows, inputTokens, compactions, retries int
				if err := reopened.store.DB().QueryRow(`SELECT COUNT(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(CASE WHEN request_kind='compaction' THEN 1 ELSE 0 END),0) FROM model_request_usage WHERE run_id=?`, run.ID).Scan(&usageRows, &inputTokens, &compactions); err != nil || usageRows != wantRequests || inputTokens != 0 || compactions != 0 {
					t.Fatal("invented usage/compaction or uncounted HTTP", usageRows, inputTokens, compactions, err)
				}
				if err := reopened.store.DB().QueryRow(`SELECT COUNT(*) FROM run_events WHERE aggregate_id=? AND event_type='run.retrying'`, run.ID).Scan(&retries); err != nil || retries != 0 {
					t.Fatal("window introduced a model retry", err, retries)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(wire) != wantRequests || len(snapshots) != wantRequests || admissions != wantRequests {
					t.Fatal("extra HTTP/retry or missing observation", len(wire), len(snapshots), admissions)
				}
				if action != "complete" && snapshots[len(snapshots)-1].Outcome != "context_canceled" {
					t.Fatal("active-window cancellation was misclassified")
				}
				for i, v := range snapshots {
					if v.Protocol != protocol || v.RunID != run.ID || v.ConfiguredBudget == nil || v.ConfiguredBudget.EffectiveTokens != 45600 || v.BodyBytes != len(wire[i]) || v.Inspection != "complete" {
						t.Fatal("window changed configured budget or snapshot provenance")
					}
				}
				t.Logf("baseline=%t %s/%s: unchanged 48k/no usage; HTTP=%d completed tools=%d; restart retained all original records; 0 real model", baseline, protocol, action, len(wire), len(result.ToolCalls))
			})
		}
	}
}
