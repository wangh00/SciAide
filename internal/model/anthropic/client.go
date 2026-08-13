package anthropic

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
	"github.com/wangh00/SciAide/internal/modelcap"
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
	v := New(profile, secret)
	v.http = client
	return v
}
func (c *Client) Capabilities(context.Context) (model.Capabilities, error) {
	return model.Capabilities{Streaming: true, ToolCalling: true, Reasoning: true, MaxContextTokens: 200_000}, nil
}
func TestConnection(ctx context.Context, profile modelprofile.Profile, secret []byte) error {
	probe := profile
	probe.MaxOutputTokens = intPointer(1)
	stream, err := New(probe, secret).Stream(ctx, model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "Reply OK."}}})
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			return nil
		}
		if recvErr != nil {
			return recvErr
		}
		if event.Type == model.EventDone {
			return nil
		}
	}
}
func intPointer(value int) *int { return &value }

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}
type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}
type toolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type payload struct {
	Model       string    `json:"model"`
	System      string    `json:"system,omitempty"`
	Messages    []message `json:"messages"`
	Tools       []toolDef `json:"tools,omitempty"`
	Stream      bool      `json:"stream"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature *float64  `json:"temperature,omitempty"`
	Thinking    *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	} `json:"thinking,omitempty"`
}

func (c *Client) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	aliases := map[string]string{}
	providerNames := map[string]string{}
	for _, def := range request.Tools {
		if err := modelutil.ValidateDefinition(def); err != nil {
			return nil, err
		}
		alias := modelutil.ProviderToolName(def.Name)
		if old := providerNames[alias]; old != "" && old != def.Name {
			return nil, fmt.Errorf("model tool name alias collision")
		}
		aliases[def.Name] = alias
		providerNames[alias] = def.Name
	}
	maxTokens := 4096
	if c.profile.MaxOutputTokens != nil {
		maxTokens = *c.profile.MaxOutputTokens
	}
	value := payload{Model: c.profile.ModelID, Messages: []message{}, Tools: []toolDef{}, Stream: true, MaxTokens: maxTokens, Temperature: c.profile.Temperature}
	if request.ResolvedReasoningLevel.Valid() {
		if value.MaxTokens <= 1024 {
			value.MaxTokens = 2048
		}
		value.Thinking = &struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		}{Type: "enabled", BudgetTokens: thinkingBudget(request.ResolvedReasoningLevel, value.MaxTokens)}
		if value.Temperature != nil && *value.Temperature != 1 {
			// Extended thinking only accepts the default temperature.
			value.Temperature = nil
		}
	}
	for _, def := range request.Tools {
		value.Tools = append(value.Tools, toolDef{Name: aliases[def.Name], Description: def.Description, InputSchema: append(json.RawMessage(nil), def.InputSchema...)})
	}
	for _, item := range request.Messages {
		switch item.Role {
		case model.RoleSystem:
			if value.System != "" {
				value.System += "\n\n"
			}
			value.System += item.Content
		case model.RoleUser:
			value.Messages = appendMessage(value.Messages, "user", contentBlock{Type: "text", Text: modelutil.WrapUntrusted("conversation_content", item.Content)})
		case model.RoleAssistant:
			if item.Content != "" {
				value.Messages = appendMessage(value.Messages, "assistant", contentBlock{Type: "text", Text: item.Content})
			}
			for _, call := range item.ToolCalls {
				if err := modelutil.ValidateToolCall(call); err != nil {
					return nil, err
				}
				name := aliases[call.Name]
				if name == "" {
					name = modelutil.ProviderToolName(call.Name)
				}
				value.Messages = appendMessage(value.Messages, "assistant", contentBlock{Type: "tool_use", ID: call.ID, Name: name, Input: append(json.RawMessage(nil), call.Arguments...)})
			}
		case model.RoleTool:
			if strings.TrimSpace(item.ToolCallID) == "" {
				return nil, fmt.Errorf("tool result requires tool call id")
			}
			value.Messages = appendMessage(value.Messages, "user", contentBlock{Type: "tool_result", ToolUseID: item.ToolCallID, Content: modelutil.WrapUntrusted("tool_result", item.Content)})
		default:
			return nil, fmt.Errorf("unsupported model message role %q", item.Role)
		}
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, modelutil.Endpoint(c.profile.BaseURL, "messages"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	if len(c.secret) > 0 {
		req.Header.Set("x-api-key", string(c.secret))
	}
	for k, v := range c.profile.CustomHeaders {
		req.Header.Set(k, v)
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, modelutil.ClassifyNetwork(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		b := modelutil.ReadErrorBody(response.Body)
		response.Body.Close()
		return nil, modelutil.ClassifyStatus(response.StatusCode, b)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes)
	return &stream{body: response.Body, scanner: scanner, providerNames: providerNames, blocks: map[int]*toolAccumulator{}}, nil
}
func appendMessage(messages []message, role string, block contentBlock) []message {
	if len(messages) > 0 && messages[len(messages)-1].Role == role {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, block)
		return messages
	}
	return append(messages, message{Role: role, Content: []contentBlock{block}})
}

func thinkingBudget(level modelcap.ReasoningLevel, maxTokens int) int {
	ratio := map[modelcap.ReasoningLevel]float64{
		modelcap.ReasoningLow: 0.15, modelcap.ReasoningMedium: 0.30, modelcap.ReasoningHigh: 0.50,
		modelcap.ReasoningXHigh: 0.70, modelcap.ReasoningMax: 0.85,
	}[level]
	budget := int(float64(maxTokens) * ratio)
	if budget < 1024 {
		budget = 1024
	}
	if budget >= maxTokens {
		budget = maxTokens - 1
	}
	return budget
}

type toolAccumulator struct {
	ID, Name  string
	Arguments strings.Builder
}
type stream struct {
	body          io.ReadCloser
	scanner       *bufio.Scanner
	providerNames map[string]string
	blocks        map[int]*toolAccumulator
	queue         []model.Event
	done          bool
	stopReason    string
	usage         model.Usage
}
type event struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		StopReason string `json:"stop_reason"`
		Usage      usage  `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage usage `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}
type usage struct {
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
}

func (u usage) normalized() model.Usage {
	read, created := 0, 0
	if u.CacheReadInputTokens != nil {
		read = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		created = *u.CacheCreationInputTokens
	}
	return model.Usage{InputTokens: u.InputTokens + read + created, FreshInputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedInputTokens: read, CacheWriteTokens: created, CacheDetailsReported: u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil}
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
		var e event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "模型返回了无法解析的 Anthropic 流数据。", false, err)
		}
		switch e.Type {
		case "message_start":
			s.usage = e.Message.Usage.normalized()
		case "content_block_start":
			if e.ContentBlock.Type == "tool_use" {
				if _, exists := s.blocks[e.Index]; !exists && len(s.blocks) >= modelutil.MaxToolCalls {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回了过多的工具调用。", false, nil)
				}
				if len(e.ContentBlock.Input) > modelutil.MaxToolArgsBytes {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回的工具参数过大。", false, nil)
				}
				a := &toolAccumulator{ID: e.ContentBlock.ID, Name: e.ContentBlock.Name}
				if len(e.ContentBlock.Input) > 0 && string(e.ContentBlock.Input) != "{}" {
					a.Arguments.Write(e.ContentBlock.Input)
				}
				s.blocks[e.Index] = a
			}
		case "content_block_delta":
			if e.Delta.Type == "text_delta" && e.Delta.Text != "" {
				s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: e.Delta.Text})
			}
			if e.Delta.Type == "input_json_delta" {
				a := s.blocks[e.Index]
				if a != nil {
					if a.Arguments.Len()+len(e.Delta.PartialJSON) > modelutil.MaxToolArgsBytes {
						return model.Event{}, fmt.Errorf("tool arguments exceed limit")
					}
					a.Arguments.WriteString(e.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if a := s.blocks[e.Index]; a != nil {
				if a.Arguments.Len() == 0 {
					a.Arguments.WriteString("{}")
				}
				name := a.Name
				if q := s.providerNames[name]; q != "" {
					name = q
				}
				call := model.ToolCall{ID: a.ID, Name: name, Arguments: json.RawMessage(a.Arguments.String())}
				if err := modelutil.ValidateToolCall(call); err != nil {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回了无效的工具调用。", false, err)
				}
				s.queue = append(s.queue, model.Event{Type: model.EventToolCall, ToolCall: &call})
				delete(s.blocks, e.Index)
			}
		case "message_delta":
			if e.Delta.StopReason != "" {
				s.stopReason = e.Delta.StopReason
			}
			u := e.Usage.normalized()
			if u.OutputTokens > 0 {
				s.usage.OutputTokens = u.OutputTokens
			}
		case "message_stop":
			if s.usage.InputTokens > 0 || s.usage.OutputTokens > 0 {
				s.queue = append(s.queue, model.Event{Type: model.EventUsage, Usage: &s.usage})
			}
			reason := "stop"
			if s.stopReason == "tool_use" {
				reason = "tool_calls"
			}
			s.queue = append(s.queue, model.Event{Type: model.EventDone, FinishReason: reason})
			s.done = true
		case "error":
			return model.Event{}, modelutil.Error("MODEL_REQUEST_REJECTED", e.Error.Message, false, nil)
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
	return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 流在完成事件前结束。", false, io.ErrUnexpectedEOF)
}
func (s *stream) pop() model.Event { e := s.queue[0]; s.queue = s.queue[1:]; return e }
func (s *stream) Close() error     { s.done = true; return s.body.Close() }
