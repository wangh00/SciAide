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
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

const maxStreamLineBytes = 1024 * 1024

type Client struct {
	profile  modelprofile.Profile
	secret   []byte
	http     *http.Client
	recorder modelcap.ReasoningRecorder
	modeMu   sync.RWMutex
	mode     thinkingMode
}

func New(profile modelprofile.Profile, secret []byte, recorders ...modelcap.ReasoningRecorder) *Client {
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	var recorder modelcap.ReasoningRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &Client{profile: profile, secret: append([]byte(nil), secret...), http: &http.Client{Timeout: timeout}, recorder: recorder}
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
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
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
		BudgetTokens int    `json:"budget_tokens,omitempty"`
	} `json:"thinking,omitempty"`
	OutputConfig *struct {
		Effort string `json:"effort"`
	} `json:"output_config,omitempty"`
}

type thinkingMode string

const (
	thinkingProviderDefault thinkingMode = "provider_default"
	thinkingAdaptive        thinkingMode = "anthropic_adaptive"
	thinkingLegacy          thinkingMode = "anthropic_legacy"
)

type reasoningRejectedError struct {
	kind modelutil.ReasoningRejectionKind
	err  error
}

func (e *reasoningRejectedError) Error() string { return e.err.Error() }
func (e *reasoningRejectedError) Unwrap() error { return e.err }

func (c *Client) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	requested := request.RequestedReasoningLevel
	if !requested.Valid() {
		requested = request.ResolvedReasoningLevel
	}
	attempts := modelcap.ReasoningAttempts(request.ResolvedReasoningLevel)
	if len(attempts) == 0 {
		attempts = []modelcap.ReasoningLevel{""}
	}
	rejected := make([]modelcap.ReasoningLevel, 0, len(attempts))
	mode := c.preferredThinkingMode()
	controlUnsupported := false
	for effortIndex := 0; effortIndex <= len(attempts); {
		level := modelcap.ReasoningLevel("")
		if effortIndex < len(attempts) {
			level = attempts[effortIndex]
		} else {
			mode = thinkingProviderDefault
		}
		request.ResolvedReasoningLevel = level
		stream, err := c.streamOnce(ctx, request, mode)
		if err == nil {
			c.rememberThinkingMode(mode)
			result := modelcap.ReasoningResult{Requested: requested, Resolved: level, Rejected: rejected, ControlUnsupported: controlUnsupported, WireMode: string(mode)}
			if c.recorder != nil && requested.Valid() {
				_ = c.recorder.RecordReasoningResult(ctx, c.profile.ID, c.profile.ModelID, result)
			}
			return model.WithReasoningResolution(stream, requested, level), nil
		}
		var rejection *reasoningRejectedError
		if !errors.As(err, &rejection) || !level.Valid() {
			return nil, err
		}
		switch rejection.kind {
		case modelutil.ReasoningRejectionValue:
			rejected = append(rejected, level)
			effortIndex++
			mode = c.preferredThinkingMode()
		case modelutil.ReasoningRejectionControl:
			if mode == thinkingAdaptive {
				// Older Claude models reject adaptive thinking but accept the
				// budget_tokens form. Try it at the same user-selected tier.
				mode = thinkingLegacy
				continue
			}
			controlUnsupported = true
			rejected = nil
			effortIndex = len(attempts)
			mode = thinkingProviderDefault
		default:
			return nil, err
		}
	}
	return nil, fmt.Errorf("reasoning negotiation exhausted")
}

func (c *Client) preferredThinkingMode() thinkingMode {
	c.modeMu.RLock()
	remembered := c.mode
	c.modeMu.RUnlock()
	if remembered == thinkingAdaptive || remembered == thinkingLegacy {
		return remembered
	}
	for _, item := range c.profile.Models {
		if item.ID != c.profile.ModelID {
			continue
		}
		switch item.ReasoningWireMode {
		case string(thinkingAdaptive):
			return thinkingAdaptive
		case string(thinkingLegacy):
			return thinkingLegacy
		}
	}
	if legacyAnthropicModel(c.profile.ModelID) {
		return thinkingLegacy
	}
	// Unknown future and custom aliases optimistically try the modern API.
	return thinkingAdaptive
}

func (c *Client) rememberThinkingMode(mode thinkingMode) {
	if mode != thinkingAdaptive && mode != thinkingLegacy {
		return
	}
	c.modeMu.Lock()
	c.mode = mode
	c.modeMu.Unlock()
}

func legacyAnthropicModel(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	for _, prefix := range []string{"us.anthropic.", "eu.anthropic.", "anthropic."} {
		id = strings.TrimPrefix(id, prefix)
	}
	for _, marker := range []string{"claude-3-", "claude-opus-4-0", "claude-opus-4-1", "claude-opus-4-5", "claude-sonnet-4-0", "claude-sonnet-4-5", "claude-haiku-4-5"} {
		if strings.Contains(id, marker) {
			return true
		}
	}
	return false
}

func (c *Client) streamOnce(ctx context.Context, request model.ChatRequest, mode thinkingMode) (model.Stream, error) {
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
	if request.ResolvedReasoningLevel.Valid() && mode != thinkingProviderDefault {
		if value.MaxTokens <= 1024 {
			value.MaxTokens = 2048
		}
		if mode == thinkingAdaptive {
			value.Thinking = &struct {
				Type         string `json:"type"`
				BudgetTokens int    `json:"budget_tokens,omitempty"`
			}{Type: "adaptive"}
			value.OutputConfig = &struct {
				Effort string `json:"effort"`
			}{Effort: string(request.ResolvedReasoningLevel)}
		} else {
			value.Thinking = &struct {
				Type         string `json:"type"`
				BudgetTokens int    `json:"budget_tokens,omitempty"`
			}{Type: "enabled", BudgetTokens: thinkingBudget(request.ResolvedReasoningLevel, value.MaxTokens)}
		}
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
	if err := appendProviderTurns(&value, request.ProviderTurns); err != nil {
		return nil, err
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
		if request.ResolvedReasoningLevel.Valid() {
			if kind := modelutil.ClassifyReasoningRejection(response.StatusCode, b); kind != modelutil.ReasoningRejectionNone {
				return nil, &reasoningRejectedError{kind: kind, err: modelutil.ClassifyStatus(response.StatusCode, b)}
			}
		}
		return nil, modelutil.ClassifyStatus(response.StatusCode, b)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes)
	return &stream{body: response.Body, scanner: scanner, providerNames: providerNames, blocks: map[int]*blockAccumulator{}, completed: map[int]struct{}{}}, nil
}

func appendProviderTurns(value *payload, turns []model.ProviderTurn) error {
	for _, turn := range turns {
		if turn.Protocol != modelcap.ProtocolAnthropic {
			return fmt.Errorf("provider turn protocol %q cannot be replayed by Anthropic", turn.Protocol)
		}
		if len(turn.Items) == 0 {
			return fmt.Errorf("Anthropic provider turn has no content blocks")
		}
		blocks := make([]contentBlock, 0, len(turn.Items))
		previousOrdinal := -1
		for _, item := range turn.Items {
			if item.Ordinal <= previousOrdinal || len(item.Payload) == 0 {
				return fmt.Errorf("Anthropic provider items are not strictly ordered")
			}
			previousOrdinal = item.Ordinal
			var block contentBlock
			if err := json.Unmarshal(item.Payload, &block); err != nil {
				return fmt.Errorf("decode Anthropic provider item: %w", err)
			}
			if block.Type != item.Type {
				return fmt.Errorf("Anthropic provider item type mismatch")
			}
			switch block.Type {
			case "text":
			case "thinking":
				if block.Signature == "" {
					return fmt.Errorf("Anthropic thinking block is missing its signature")
				}
			case "redacted_thinking":
				if block.Data == "" {
					return fmt.Errorf("Anthropic redacted thinking block is missing data")
				}
			case "tool_use":
				if block.ID == "" || block.Name == "" || len(block.Input) == 0 || !json.Valid(block.Input) || item.CallID != block.ID {
					return fmt.Errorf("invalid persisted Anthropic tool_use block")
				}
			default:
				return fmt.Errorf("unsupported persisted Anthropic content block %q", block.Type)
			}
			blocks = append(blocks, block)
		}
		value.Messages = append(value.Messages, message{Role: "assistant", Content: blocks})
		for _, result := range turn.ToolResults {
			if result.Role != model.RoleTool || strings.TrimSpace(result.ToolCallID) == "" {
				return fmt.Errorf("invalid Anthropic provider turn tool result")
			}
			value.Messages = appendMessage(value.Messages, "user", contentBlock{Type: "tool_result", ToolUseID: result.ToolCallID, Content: modelutil.WrapUntrusted("tool_result", result.Content)})
		}
	}
	return nil
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

const (
	maxProviderBlockBytes    = 8 * 1024 * 1024
	maxProviderTurnBytes     = 16 * 1024 * 1024
	maxProviderBlocksPerTurn = 128
)

type blockAccumulator struct {
	Type, ID, Name, Data string
	Text                 strings.Builder
	Thinking             strings.Builder
	Signature            strings.Builder
	Arguments            strings.Builder
}
type stream struct {
	body          io.ReadCloser
	scanner       *bufio.Scanner
	providerNames map[string]string
	blocks        map[int]*blockAccumulator
	completed     map[int]struct{}
	toolBlocks    int
	providerBytes int
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
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
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

func (a *blockAccumulator) size() int {
	return len(a.Data) + a.Text.Len() + a.Thinking.Len() + a.Signature.Len() + a.Arguments.Len()
}

func (s *stream) completeBlock(index int, accumulator *blockAccumulator) (model.ProviderItem, *model.ToolCall, error) {
	block := contentBlock{Type: accumulator.Type}
	var call *model.ToolCall
	switch accumulator.Type {
	case "text":
		block.Text = accumulator.Text.String()
	case "thinking":
		block.Thinking = accumulator.Thinking.String()
		block.Signature = accumulator.Signature.String()
		if block.Signature == "" {
			return model.ProviderItem{}, nil, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 思考块缺少签名。", false, nil)
		}
	case "redacted_thinking":
		block.Data = accumulator.Data
		if block.Data == "" {
			return model.ProviderItem{}, nil, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 脱敏思考块缺少数据。", false, nil)
		}
	case "tool_use":
		arguments := accumulator.Arguments.String()
		if arguments == "" {
			arguments = "{}"
		}
		block.ID, block.Name, block.Input = accumulator.ID, accumulator.Name, json.RawMessage(arguments)
		name := accumulator.Name
		if qualified := s.providerNames[name]; qualified != "" {
			name = qualified
		}
		value := model.ToolCall{ID: accumulator.ID, Name: name, Arguments: json.RawMessage(arguments)}
		if err := modelutil.ValidateToolCall(value); err != nil {
			return model.ProviderItem{}, nil, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回了无效的工具调用。", false, err)
		}
		call = &value
	default:
		return model.ProviderItem{}, nil, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 返回了暂不支持的内容块。", false, nil)
	}
	payload, err := json.Marshal(block)
	if err != nil {
		return model.ProviderItem{}, nil, modelutil.Error("MODEL_STREAM_INVALID", "无法保存 Anthropic 内容块。", false, err)
	}
	item := model.ProviderItem{Ordinal: index, Type: accumulator.Type, Payload: payload}
	if call != nil {
		item.CallID = accumulator.ID
	}
	return item, call, nil
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
			_, completed := s.completed[e.Index]
			if e.Index < 0 || e.Index >= maxProviderBlocksPerTurn || s.blocks[e.Index] != nil || completed {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 返回了无效或重复的内容块索引。", false, nil)
			}
			a := &blockAccumulator{Type: e.ContentBlock.Type, ID: e.ContentBlock.ID, Name: e.ContentBlock.Name, Data: e.ContentBlock.Data}
			switch e.ContentBlock.Type {
			case "text":
				a.Text.WriteString(e.ContentBlock.Text)
				if e.ContentBlock.Text != "" {
					s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: e.ContentBlock.Text})
				}
			case "thinking":
				a.Thinking.WriteString(e.ContentBlock.Thinking)
				a.Signature.WriteString(e.ContentBlock.Signature)
			case "redacted_thinking":
			case "tool_use":
				s.toolBlocks++
				if s.toolBlocks > modelutil.MaxToolCalls {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回了过多的工具调用。", false, nil)
				}
				if len(e.ContentBlock.Input) > modelutil.MaxToolArgsBytes {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回的工具参数过大。", false, nil)
				}
				if len(e.ContentBlock.Input) > 0 && string(e.ContentBlock.Input) != "{}" {
					a.Arguments.Write(e.ContentBlock.Input)
				}
			default:
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 返回了暂不支持的内容块。", false, nil)
			}
			if a.size() > maxProviderBlockBytes {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 内容块超过大小限制。", false, nil)
			}
			s.blocks[e.Index] = a
		case "content_block_delta":
			a := s.blocks[e.Index]
			if a == nil {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 内容增量缺少起始块。", false, nil)
			}
			switch e.Delta.Type {
			case "text_delta":
				a.Text.WriteString(e.Delta.Text)
				if e.Delta.Text != "" {
					s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: e.Delta.Text})
				}
			case "thinking_delta":
				a.Thinking.WriteString(e.Delta.Thinking)
			case "signature_delta":
				a.Signature.WriteString(e.Delta.Signature)
			case "input_json_delta":
				if a.Arguments.Len()+len(e.Delta.PartialJSON) > modelutil.MaxToolArgsBytes {
					return model.Event{}, fmt.Errorf("tool arguments exceed limit")
				}
				a.Arguments.WriteString(e.Delta.PartialJSON)
			default:
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 返回了暂不支持的内容增量。", false, nil)
			}
			if a.size() > maxProviderBlockBytes {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 内容块超过大小限制。", false, nil)
			}
		case "content_block_stop":
			a := s.blocks[e.Index]
			if a == nil {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 内容结束事件缺少起始块。", false, nil)
			}
			item, call, err := s.completeBlock(e.Index, a)
			if err != nil {
				return model.Event{}, err
			}
			if s.providerBytes+len(item.Payload) > maxProviderTurnBytes {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 单轮协议状态超过大小限制。", false, nil)
			}
			s.providerBytes += len(item.Payload)
			if call != nil {
				s.queue = append(s.queue, model.Event{Type: model.EventToolCall, ToolCall: call})
			}
			s.queue = append(s.queue, model.Event{Type: model.EventProviderItem, ProviderItem: &item})
			delete(s.blocks, e.Index)
			s.completed[e.Index] = struct{}{}
		case "message_delta":
			if e.Delta.StopReason != "" {
				s.stopReason = e.Delta.StopReason
			}
			u := e.Usage.normalized()
			if u.OutputTokens > 0 {
				s.usage.OutputTokens = u.OutputTokens
			}
		case "message_stop":
			if len(s.blocks) > 0 {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Anthropic 流在内容块完成前结束。", false, nil)
			}
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
