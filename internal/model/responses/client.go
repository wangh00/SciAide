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
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

const maxStreamLineBytes = 1024 * 1024

type Client struct {
	profile  modelprofile.Profile
	secret   []byte
	http     *http.Client
	recorder modelcap.ReasoningRecorder
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
	value := New(profile, secret)
	value.http = client
	return value
}
func (c *Client) Capabilities(context.Context) (model.Capabilities, error) {
	return model.Capabilities{Streaming: true, ToolCalling: true, Reasoning: true, MaxContextTokens: c.profile.ContextBudget(c.profile.ModelID).WindowTokens}, nil
}

type inputItem struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   any             `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    string          `json:"output,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

func (i inputItem) MarshalJSON() ([]byte, error) {
	if len(i.Raw) > 0 {
		return i.Raw, nil
	}
	type wireInputItem inputItem
	return json.Marshal(wireInputItem(i))
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
	Store           bool        `json:"store"`
	Include         []string    `json:"include,omitempty"`
	Temperature     *float64    `json:"temperature,omitempty"`
	MaxOutputTokens *int        `json:"max_output_tokens,omitempty"`
	Reasoning       *struct {
		Effort string `json:"effort"`
	} `json:"reasoning,omitempty"`
}

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
	controlUnsupported := false
	for index := 0; index <= len(attempts); index++ {
		level := modelcap.ReasoningLevel("")
		if index < len(attempts) {
			level = attempts[index]
		}
		request.ResolvedReasoningLevel = level
		stream, err := c.streamOnce(ctx, request)
		if err == nil {
			wireMode := "responses_effort"
			if !level.Valid() {
				wireMode = "provider_default"
			}
			result := modelcap.ReasoningResult{Requested: requested, Resolved: level, Rejected: rejected, ControlUnsupported: controlUnsupported, WireMode: wireMode}
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
		case modelutil.ReasoningRejectionControl:
			rejected = nil
			controlUnsupported = true
			index = len(attempts) - 1
		default:
			return nil, err
		}
	}
	return nil, fmt.Errorf("reasoning negotiation exhausted")
}

func (c *Client) streamOnce(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
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
	value := payload{Model: c.profile.ModelID, Input: []inputItem{}, Tools: []toolDef{}, Stream: true, Store: false, Include: []string{"reasoning.encrypted_content"}, Temperature: c.profile.Temperature, MaxOutputTokens: c.profile.MaxOutputTokens}
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
	if err := appendProviderTurns(&value, request.ProviderTurns); err != nil {
		return nil, err
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
		if request.ResolvedReasoningLevel.Valid() {
			if kind := modelutil.ClassifyReasoningRejection(response.StatusCode, body); kind != modelutil.ReasoningRejectionNone {
				return nil, &reasoningRejectedError{kind: kind, err: modelutil.ClassifyStatus(response.StatusCode, body)}
			}
		}
		return nil, modelutil.ClassifyStatus(response.StatusCode, body)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes)
	return &stream{body: response.Body, scanner: scanner, providerNames: providerNames, calls: map[string]*callAccumulator{}, itemOrdinals: map[string]int{}, completedOrdinals: map[int]struct{}{}}, nil
}

type responseItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	Role             string          `json:"role,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	EncryptedContent *string         `json:"encrypted_content,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
}

func appendProviderTurns(value *payload, turns []model.ProviderTurn) error {
	for _, turn := range turns {
		if turn.Protocol != modelcap.ProtocolOpenAIResponses {
			return fmt.Errorf("provider turn protocol %q cannot be replayed by Responses", turn.Protocol)
		}
		if len(turn.Items) == 0 {
			return fmt.Errorf("Responses provider turn has no output items")
		}
		previousOrdinal := -1
		for _, persisted := range turn.Items {
			if persisted.Ordinal <= previousOrdinal || len(persisted.Payload) == 0 {
				return fmt.Errorf("Responses provider items are not strictly ordered")
			}
			previousOrdinal = persisted.Ordinal
			var item responseItem
			if err := json.Unmarshal(persisted.Payload, &item); err != nil {
				return fmt.Errorf("decode Responses provider item: %w", err)
			}
			if item.Type != persisted.Type {
				return fmt.Errorf("Responses provider item type mismatch")
			}
			switch item.Type {
			case "reasoning":
				if item.ID == "" || (len(item.Summary) == 0 && len(item.Content) == 0 && item.EncryptedContent == nil) {
					return fmt.Errorf("invalid persisted Responses reasoning item")
				}
			case "message":
				if item.Role != "assistant" || len(item.Content) == 0 {
					return fmt.Errorf("invalid persisted Responses message item")
				}
			case "function_call":
				if item.ID == "" || item.CallID == "" || item.Name == "" || persisted.CallID != item.CallID || !json.Valid([]byte(item.Arguments)) {
					return fmt.Errorf("invalid persisted Responses function_call item")
				}
			default:
				return fmt.Errorf("unsupported persisted Responses output item %q", item.Type)
			}
			value.Input = append(value.Input, inputItem{Raw: append(json.RawMessage(nil), persisted.Payload...)})
		}
		for _, result := range turn.ToolResults {
			if result.Role != model.RoleTool || strings.TrimSpace(result.ToolCallID) == "" {
				return fmt.Errorf("invalid Responses provider turn tool result")
			}
			value.Input = append(value.Input, inputItem{Type: "function_call_output", CallID: result.ToolCallID, Output: modelutil.WrapUntrusted("tool_result", result.Content)})
		}
	}
	return nil
}

type callAccumulator struct {
	ID, CallID, Name string
	Arguments        strings.Builder
}
type stream struct {
	body              io.ReadCloser
	scanner           *bufio.Scanner
	providerNames     map[string]string
	calls             map[string]*callAccumulator
	itemOrdinals      map[string]int
	completedOrdinals map[int]struct{}
	nextOrdinal       int
	providerBytes     int
	queue             []model.Event
	hadToolCalls      bool
	done              bool
}
type eventPayload struct {
	Type        string          `json:"type"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Delta       string          `json:"delta"`
	Item        json.RawMessage `json:"item"`
	ItemID      string          `json:"item_id"`
	OutputIndex *int            `json:"output_index"`
	Error       *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Param   string `json:"param"`
	} `json:"error"`
	Response struct {
		Status            string        `json:"status"`
		Usage             responseUsage `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	} `json:"response"`
}
type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

const (
	maxResponseItemsPerTurn = 128
	maxResponseItemBytes    = 8 * 1024 * 1024
	maxResponseTurnBytes    = 16 * 1024 * 1024
)

func decodeResponseItem(raw json.RawMessage) (responseItem, error) {
	var item responseItem
	if len(raw) == 0 || len(raw) > maxResponseItemBytes || !json.Valid(raw) {
		return item, fmt.Errorf("invalid Responses output item payload")
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return item, err
	}
	if item.Type == "" {
		return item, fmt.Errorf("Responses output item has no type")
	}
	return item, nil
}

func responseItemKey(item responseItem) string {
	if item.ID != "" {
		return item.ID
	}
	return item.CallID
}

func (s *stream) itemOrdinal(item responseItem, explicit *int, completed bool) (int, error) {
	key := responseItemKey(item)
	ordinal := -1
	if explicit != nil {
		ordinal = *explicit
	} else if key != "" {
		if existing, ok := s.itemOrdinals[key]; ok {
			ordinal = existing
		}
	}
	if ordinal < 0 {
		ordinal = s.nextOrdinal
	}
	if ordinal < 0 || ordinal >= maxResponseItemsPerTurn {
		return 0, fmt.Errorf("Responses output item index is invalid")
	}
	if key != "" {
		if existing, ok := s.itemOrdinals[key]; ok && existing != ordinal {
			return 0, fmt.Errorf("Responses output item index changed")
		}
		s.itemOrdinals[key] = ordinal
	}
	if ordinal >= s.nextOrdinal {
		s.nextOrdinal = ordinal + 1
	}
	if completed {
		if _, exists := s.completedOrdinals[ordinal]; exists {
			return 0, fmt.Errorf("Responses output item completed twice")
		}
		s.completedOrdinals[ordinal] = struct{}{}
	}
	return ordinal, nil
}

func (s *stream) completeProviderItem(item responseItem, ordinal int) (model.ProviderItem, error) {
	switch item.Type {
	case "reasoning":
		if item.ID == "" || (len(item.Summary) == 0 && len(item.Content) == 0 && item.EncryptedContent == nil) {
			return model.ProviderItem{}, fmt.Errorf("invalid Responses reasoning item")
		}
	case "message":
		if item.Role != "assistant" || len(item.Content) == 0 {
			return model.ProviderItem{}, fmt.Errorf("invalid Responses assistant message item")
		}
	case "function_call":
		if item.ID == "" || item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
			return model.ProviderItem{}, fmt.Errorf("invalid Responses function call item")
		}
	default:
		return model.ProviderItem{}, fmt.Errorf("unsupported Responses output item %q", item.Type)
	}
	normalized, err := json.Marshal(item)
	if err != nil {
		return model.ProviderItem{}, err
	}
	if len(normalized) > maxResponseItemBytes || s.providerBytes+len(normalized) > maxResponseTurnBytes {
		return model.ProviderItem{}, fmt.Errorf("Responses provider state exceeds limit")
	}
	s.providerBytes += len(normalized)
	providerItem := model.ProviderItem{Ordinal: ordinal, Type: item.Type, Payload: normalized}
	if item.Type == "function_call" {
		providerItem.CallID = item.CallID
	}
	return providerItem, nil
}

func (u responseUsage) normalized() model.Usage {
	cached := 0
	if u.InputTokensDetails != nil {
		cached = u.InputTokensDetails.CachedTokens
	}
	reasoning := 0
	if u.OutputTokensDetails != nil {
		reasoning = u.OutputTokensDetails.ReasoningTokens
	}
	return model.Usage{InputTokens: u.InputTokens, FreshInputTokens: max(u.InputTokens-cached, 0), OutputTokens: u.OutputTokens, ReasoningTokens: reasoning, CachedInputTokens: cached, CacheDetailsReported: u.InputTokensDetails != nil}
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
			return model.Event{}, modelutil.ErrorWithDetails("MODEL_STREAM_INVALID", "模型返回了无法解析的 Responses 流数据。", modelutil.ProviderErrorDetails("Responses stream event", 0, []byte(data)), false, err)
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: event.Delta})
			}
		case "response.output_item.added":
			item, err := decodeResponseItem(event.Item)
			if err != nil {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Responses 返回了无效的输出项。", false, err)
			}
			if _, err := s.itemOrdinal(item, event.OutputIndex, false); err != nil {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Responses 输出项顺序无效。", false, err)
			}
			if item.Type == "function_call" {
				key := responseItemKey(item)
				if _, exists := s.calls[key]; !exists && len(s.calls) >= modelutil.MaxToolCalls {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回了过多的工具调用。", false, nil)
				}
				if len(item.Arguments) > modelutil.MaxToolArgsBytes {
					return model.Event{}, modelutil.Error("MODEL_TOOL_CALL_INVALID", "模型返回的工具参数过大。", false, nil)
				}
				a := &callAccumulator{ID: item.ID, CallID: item.CallID, Name: item.Name}
				a.Arguments.WriteString(item.Arguments)
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
			item, err := decodeResponseItem(event.Item)
			if err != nil {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Responses 返回了无效的完成项。", false, err)
			}
			var completedCall *callAccumulator
			callKey := ""
			if item.Type == "function_call" {
				callKey = responseItemKey(item)
				completedCall = s.calls[callKey]
				if completedCall == nil {
					completedCall = &callAccumulator{}
				}
				if item.ID != "" {
					completedCall.ID = item.ID
				}
				if item.CallID != "" {
					completedCall.CallID = item.CallID
				}
				if item.Name != "" {
					completedCall.Name = item.Name
				}
				if item.Arguments != "" && completedCall.Arguments.Len() == 0 {
					completedCall.Arguments.WriteString(item.Arguments)
				}
				item.ID, item.CallID, item.Name, item.Arguments = completedCall.ID, completedCall.CallID, completedCall.Name, completedCall.Arguments.String()
			}
			ordinal, err := s.itemOrdinal(item, event.OutputIndex, true)
			if err != nil {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Responses 完成项顺序无效。", false, err)
			}
			providerItem, err := s.completeProviderItem(item, ordinal)
			if err != nil {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Responses 完成项无法安全保存。", false, err)
			}
			if completedCall != nil {
				if err := s.emitCall(completedCall); err != nil {
					return model.Event{}, err
				}
				s.hadToolCalls = true
				delete(s.calls, callKey)
			}
			s.queue = append(s.queue, model.Event{Type: model.EventProviderItem, ProviderItem: &providerItem})
		case "response.completed", "response.incomplete":
			if event.Type == "response.completed" && (len(s.calls) > 0 || len(s.completedOrdinals) < len(s.itemOrdinals)) {
				return model.Event{}, modelutil.Error("MODEL_STREAM_INVALID", "Responses 流在输出项完成前结束。", false, nil)
			}
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
		case "response.failed", "response.error", "error":
			message := "Responses 请求未完成。"
			if event.Message != "" {
				message = event.Message
			} else if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			} else if event.Response.Error != nil && event.Response.Error.Message != "" {
				message = event.Response.Error.Message
			} else if providerMessage := modelutil.ProviderErrorMessage([]byte(data)); providerMessage != "" {
				message = providerMessage
			}
			return model.Event{}, modelutil.ErrorWithDetails("MODEL_REQUEST_REJECTED", message, modelutil.ProviderErrorDetails("Responses stream event", 0, []byte(data)), false, nil)
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
