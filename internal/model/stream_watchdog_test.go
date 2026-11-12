package model_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
)

func watchdogClient(protocol, url string) model.ChatModel {
	p := modelprofile.Profile{BaseURL: url, ModelID: "watchdog-fixture", TimeoutSeconds: 5}
	switch protocol {
	case "chat":
		return openai.New(p, nil)
	case "responses":
		return responses.New(p, nil)
	default:
		return anthropic.New(p, nil)
	}
}

func TestRealProtocolStreamsIgnoreHeartbeatAndIncompleteFraming(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, mode := range []string{"silence", "ping", "metadata", "half_event", "cancel", "buffered"} {
			if mode == "buffered" && protocol != "chat" {
				continue
			}
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					ticker := time.NewTicker(20 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-r.Context().Done():
							return
						case <-ticker.C:
							text := ""
							switch mode {
							case "ping":
								text = ": keepalive\n\n"
							case "half_event":
								text = "data: {\"delta\":\""
							case "metadata":
								switch protocol {
								case "chat":
									text = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"
								case "responses":
									text = "data: {\"type\":\"response.in_progress\"}\n\n"
								default:
									text = "data: {\"type\":\"ping\"}\n\n"
								}
							}
							if text != "" {
								if _, err := io.WriteString(w, text); err != nil {
									return
								}
								w.(http.Flusher).Flush()
							}
						}
					}
				}))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				request := model.ChatRequest{StreamInactivityTimeout: 120 * time.Millisecond, DisableStreaming: mode == "buffered"}
				if mode == "cancel" {
					time.AfterFunc(30*time.Millisecond, cancel)
				}
				started := time.Now()
				stream, err := watchdogClient(protocol, server.URL).Stream(ctx, request)
				if err == nil {
					defer stream.Close()
					for {
						_, err = stream.Recv()
						if err != nil {
							break
						}
					}
				}
				if mode == "cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					return
				}
				if stream != nil && !model.LastStreamProgress(stream).IsZero() {
					t.Fatal("heartbeat/incomplete frames counted as effective progress", model.LastStreamProgress(stream))
				}
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Code != "MODEL_STREAM_STALLED" || !appErr.Retryable || time.Since(started) > 2*time.Second {
					t.Fatal(err, time.Since(started))
				}
			})
		}
	}
}

func TestRealProtocolReasoningAndToolArgumentsRenewProgressWithoutLeaking(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, kind := range []string{"text", "reasoning", "arguments"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					write := func(s string) { _, _ = fmt.Fprintf(w, "data: %s\n\n", s); w.(http.Flusher).Flush() }
					if protocol == "responses" && kind == "arguments" {
						write(`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"call","name":"fixture","arguments":""}}`)
					}
					if protocol == "anthropic" {
						write(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
						switch kind {
						case "text":
							write(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
						case "reasoning":
							write(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
						default:
							write(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"fixture","input":{}}}`)
						}
					}
					for i := 0; i < 6; i++ {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(50 * time.Millisecond):
						}
						switch protocol {
						case "chat":
							switch kind {
							case "text":
								write(`{"choices":[{"delta":{"content":"visible"}}]}`)
							case "reasoning":
								write(`{"choices":[{"delta":{"reasoning_content":"private-progress"}}]}`)
							default:
								write(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"","function":{"arguments":" "}}]}}]}`)
							}
						case "responses":
							switch kind {
							case "text":
								write(`{"type":"response.output_text.delta","delta":"visible"}`)
							case "reasoning":
								write(`{"type":"response.reasoning_summary_text.delta","delta":"private-progress"}`)
							default:
								write(`{"type":"response.function_call_arguments.delta","item_id":"fc","delta":" "}`)
							}
						default:
							switch kind {
							case "text":
								write(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"visible"}}`)
							case "reasoning":
								write(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private-progress"}}`)
							default:
								write(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" "}}`)
							}
						}
					}
					// Stop a validly progressing stream with an explicit permanent
					// provider error; progress is not permission to accept a draft.
					write(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request_error","message":"fixture stop"}}`)
				}))
				defer server.Close()
				started := time.Now()
				stream, err := watchdogClient(protocol, server.URL).Stream(context.Background(), model.ChatRequest{StreamInactivityTimeout: 160 * time.Millisecond})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				var visible strings.Builder
				for {
					event, recvErr := stream.Recv()
					if event.Type == model.EventTextDelta {
						visible.WriteString(event.Text)
					}
					if recvErr != nil {
						err = recvErr
						break
					}
				}
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Code == "MODEL_STREAM_STALLED" || time.Since(started) < 300*time.Millisecond {
					t.Fatal(err, time.Since(started))
				}
				if progress := model.LastStreamProgress(stream); !progress.After(started) {
					t.Fatal("protocol/wrapper lost last effective progress clock", protocol, kind, progress)
				}
				if kind != "text" && visible.Len() != 0 || strings.Contains(visible.String(), "private-progress") {
					t.Fatal("private progress leaked", visible.String())
				}
			})
		}
	}
}
