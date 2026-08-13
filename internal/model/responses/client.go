package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelutil"
)

const maxStreamLineBytes = 1024 * 1024

type Client struct {
	profile modelprofile.Profile
	secret  []byte
	http    *http.Client
}

func New(profile modelprofile.Profile, secret []byte) *Client {
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{profile: profile, secret: append([]byte(nil), secret...), http: &http.Client{Timeout: timeout}}
}
func NewWithHTTPClient(profile modelprofile.Profile, secret []byte, client *http.Client) *Client {
	value := New(profile, secret)
	value.http = client
	return value
}
func (c *Client) Capabilities(context.Context) (model.Capabilities, error) {
	return model.Capabilities{Streaming: true, ToolCalling: true, Reasoning: true, MaxContextTokens: 200_000}, nil
}

type inputItem struct {
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}
type inputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type toolDef struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}
type payload struct {
	Model           string      `json:"model"`
	Instructions    string      `json:"instructions,omitempty"`
	Input           []inputItem `json:"input"`
	Tools           []toolDef   `json:"tools,omitempty"`
	Stream          bool        `json:"stream"`
	Temperature     *float64    `json:"temperature,omitempty"`
	MaxOutputTokens *int        `json:"max_output_tokens,omitempty"`
	Reasoning       *struct {
		Effort string `json:"effort"`
	} `json:"reasoning,omitempty"`
}

func (c *Client) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	providerNames := map[string]string{}
	aliases := map[string]string{}
	for _, def := range request.Tools {
		if err := modelutil.ValidateDefinition(def); err != nil {
			return nil, err
		}
		alias := modelutil.ProviderToolName(def.Name)
		if existing := providerNames[alias]; existing != "" && existing != def.Name {
			return nil, fmt.Errorf("model tool name alias collision")
		}
		providerNames[alias] = def.Name
		aliases[def.Name] = alias
	}
	value := payload{Model: c.profile.ModelID, Input: []inputItem{}, Tools: []toolDef{}, Stream: true, Temperature: c.profile.Temperature, MaxOutputTokens: c.profile.MaxOutputTokens}
	if request.ResolvedReasoningLevel.Valid() {
		value.Reasoning = &struct {
			Effort string `json:"effort"`
		}{Effort: string(request.ResolvedReasoningLevel)}
	}
	for _, def := range request.Tools {
		value.Tools = append(value.Tools, toolDef{Type: "function", Name: aliases[def.Name], Description: def.Description, Parameters: append(json.RawMessage(nil), def.InputSchema...)})
	}
	for _, message := range request.Messages {
		switch message.Role {
		case model.RoleSystem:
			if value.Instructions != "" {
				value.Instructions += "\n\n"
			}
			value.Instructions += message.Content
		case model.RoleUser:
			value.Input = append(value.Input, inputItem{Type: "message", Role: "user", Content: []inputContent{{Type: "input_text", Text: modelutil.WrapUntrusted("conversation_content", message.Content)}}})
		case model.RoleAssistant:
			if message.Content != "" {
				value.Input = append(value.Input, inputItem{Type: "message", Role: "assistant", Content: []inputContent{{Type: "output_text", Text: message.Content}}})
			}
			for _, call := range message.ToolCalls {
				if err := modelutil.ValidateToolCall(call); err != nil {
					return nil, err
				}
				name := aliases[call.Name]
				if name == "" {
					name = modelutil.ProviderToolName(call.Name)
				}
				value.Input = append(value.Input, inputItem{Type: "function_call", CallID: call.ID, Name: name, Arguments: string(call.Arguments)})
			}
		case model.RoleTool:
			value.Input = append(value.Input, inputItem{Type: "function_call_output", CallID: message.ToolCallID, Output: modelutil.WrapUntrusted("tool_result", message.Content)})
		default:
			return nil, fmt.Errorf("unsupported model message role %q", message.Role)
		}
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, modelutil.Endpoint(c.profile.BaseURL, "responses"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	modelutil.ApplyBearerAndCustomHeaders(req, c.secret, c.profile.CustomHeaders)
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, modelutil.ClassifyNetwork(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body := modelutil.ReadErrorBody(response.Body)
		response.Body.Close()
		if request.ResolvedReasoningLevel.Valid() && modelutil.ReasoningControlRejected(response.StatusCode, body) {
			request.ResolvedReasoningLevel = ""
			return c.Stream(ctx, request)
		}
		return nil, modelutil.ClassifyStatus(response.StatusCode, body)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes)
	return &stream{body: response.Body, scanner: scanner, providerNames: providerNames, calls: map[string]*callAccumulator{}}, nil
}

type callAccumulator struct {
	ID, CallID, Name string
	Arguments        strings.Builder
}
type stream struct {
	body          io.ReadCloser
	scanner       *bufio.Scanner
	providerNames map[string]string
	calls         map[string]*callAccumulator
	queue         []model.Event
	hadToolCalls  bool
	done          bool
}
type eventPayload struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Delta   string `json:"delta"`
	Item    struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	ItemID      string `json:"item_id"`
	OutputIndex int    `json:"output_index"`
	Response    struct {
		Status            string        `json:"status"`
		Usage             responseUsage `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}
type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (u responseUsage) normalized() model.Usage {
	cached := 0
	if u.InputTokensDetails != nil {
		cached = u.InputTokensDetails.CachedTokens
	}
	return model.Usage{InputTokens: u.InputTokens, FreshInputTokens: max(u.InputTokens-cached, 0), OutputTokens: u.OutputTokens, CachedInputTokens: cached, CacheDetailsReported: u.InputTokensDetails != nil}
}

func (s *stream) Recv() (model.Event, error) {
	if len(s.queue) > 0 {
		return s.pop(), nil
	}
	if s.done {
		return model.Event{}, io.EOF
	}
	for s.scanner.Scan() {
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			if err := s.finish(); err != nil {
				return model.Event{}, err
			}
			return s.pop(), nil
		}
		var event eventPayload
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "模型返回了无法解析的 Responses 流数据。", false, err)
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: event.Delta})
			}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				key := event.Item.ID
				if key == "" {
					key = event.Item.CallID
				}
				if _, exists := s.calls[key]; !exists && len(s.calls) >= modelutil.MaxToolCalls {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回了过多的工具调用。", false, nil)
				}
				if len(event.Item.Arguments) > modelutil.MaxToolArgsBytes {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回的工具参数过大。", false, nil)
				}
				a := &callAccumulator{ID: event.Item.ID, CallID: event.Item.CallID, Name: event.Item.Name}
				a.Arguments.WriteString(event.Item.Arguments)
				s.calls[key] = a
			}
		case "response.function_call_arguments.delta":
			key := event.ItemID
			if a := s.calls[key]; a != nil {
				if a.Arguments.Len()+len(event.Delta) > modelutil.MaxToolArgsBytes {
					return model.Event{}, fmt.Errorf("tool arguments exceed limit")
				}
				a.Arguments.WriteString(event.Delta)
			}
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				key := event.Item.ID
				if key == "" {
					key = event.Item.CallID
				}
				a := s.calls[key]
				if a == nil {
					a = &callAccumulator{}
				}
				if event.Item.ID != "" {
					a.ID = event.Item.ID
				}
				if event.Item.CallID != "" {
					a.CallID = event.Item.CallID
				}
				if event.Item.Name != "" {
					a.Name = event.Item.Name
				}
				if event.Item.Arguments != "" && a.Arguments.Len() == 0 {
					a.Arguments.WriteString(event.Item.Arguments)
				}
				if err := s.emitCall(a); err != nil {
					return model.Event{}, err
				}
				s.hadToolCalls = true
				delete(s.calls, key)
			}
		case "response.completed", "response.incomplete":
			usage := event.Response.Usage.normalized()
			if usage.InputTokens > 0 || usage.OutputTokens > 0 {
				s.queue = append(s.queue, model.Event{Type: model.EventUsage, Usage: &usage})
			}
			reason := "stop"
			if s.hadToolCalls {
				reason = "tool_calls"
			} else if event.Response.IncompleteDetails != nil {
				switch event.Response.IncompleteDetails.Reason {
				case "max_output_tokens":
					reason = "length"
				case "content_filter":
					reason = "content_filter"
				default:
					reason = "incomplete"
				}
			}
			s.queue = append(s.queue, model.Event{Type: model.EventDone, FinishReason: reason})
			s.done = true
		case "response.failed", "error":
			message := "Responses 请求未完成。"
			if event.Message != "" {
				message = event.Message
			} else if event.Response.Error != nil && event.Response.Error.Message != "" {
				message = event.Response.Error.Message
			}
			return model.Event{}, modelutil.Error("MODEL_REQUEST_REJECTED", message, false, nil)
		}
		if len(s.queue) > 0 {
			return s.pop(), nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return model.Event{}, err
		}
		return model.Event{}, modelutil.Error("MODEL_UNAVAILABLE", "模型连接意外中断。", false, err)
	}
	if !s.done {
		return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Responses 流在完成事件前结束。", false, io.ErrUnexpectedEOF)
	}
	return model.Event{}, io.EOF
}
func (s *stream) emitCall(a *callAccumulator) error {
	id := a.CallID
	if id == "" {
		id = a.ID
	}
	name := a.Name
	if q := s.providerNames[name]; q != "" {
		name = q
	}
	call := model.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(a.Arguments.String())}
	if err := modelutil.ValidateToolCall(call); err != nil {
		return modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回了无效的工具调用。", false, err)
	}
	s.queue = append(s.queue, model.Event{Type: model.EventToolCall, ToolCall: &call})
	return nil
}
func (s *stream) finish() error {
	if s.done {
		return nil
	}
	for _, a := range s.calls {
		if err := s.emitCall(a); err != nil {
			return err
		}
		s.hadToolCalls = true
	}
	reason := "stop"
	if s.hadToolCalls {
		reason = "tool_calls"
	}
	s.queue = append(s.queue, model.Event{Type: model.EventDone, FinishReason: reason})
	s.done = true
	return nil
}
func (s *stream) pop() model.Event { e := s.queue[0]; s.queue = s.queue[1:]; return e }
func (s *stream) Close() error     { s.done = true; return s.body.Close() }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
