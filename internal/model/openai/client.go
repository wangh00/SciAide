package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/modelutil"
)

const (
	maxStreamLineBytes    = 1024 * 1024
	maxToolCallsPerTurn   = 32
	maxToolCallIDBytes    = 1024
	maxToolNameBytes      = 160
	maxToolArgumentsBytes = 256 * 1024
	maxProviderToolName   = 64
	maxErrorBodyBytes     = 16 * 1024
	maxResponseBodyBytes  = 4 * 1024 * 1024
)

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
	return &Client{profile: profile, secret: append([]byte(nil), secret...), http: modelutil.NewStreamingHTTPClient(timeout), recorder: recorder}
}

func NewWithHTTPClient(profile modelprofile.Profile, secret []byte, client *http.Client) *Client {
	value := New(profile, secret)
	value.http = client
	return value
}

func (c *Client) Capabilities(context.Context) (model.Capabilities, error) {
	return model.Capabilities{Streaming: true, ToolCalling: true, Reasoning: true, MaxContextTokens: c.profile.ContextBudget(c.profile.ModelID).WindowTokens}, nil
}

func (c *Client) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	stream, _, err := c.negotiateOpen(ctx, request)
	return stream, err
}

func (c *Client) Test(ctx context.Context, profile modelprofile.Profile, secret []byte) error {
	_, err := c.Discover(ctx, profile, secret)
	return err
}

func (c *Client) Discover(ctx context.Context, profile modelprofile.Profile, secret []byte) ([]modelprofile.AvailableModel, error) {
	tester := NewWithHTTPClient(profile, secret, c.http)
	endpoint := endpointURL(profile.BaseURL, "models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	tester.applyHeaders(req)
	response, err := tester.http.Do(req)
	if err != nil {
		return nil, classifyNetwork(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return nil, classifyStatus(response.StatusCode, response.Header, body)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	var payload modelsResponse
	if err := decoder.Decode(&payload); err != nil {
		return nil, modelError("MODEL_LIST_INVALID", "服务返回的模型列表无法解析，仍可手动填写 Model ID。", false, err)
	}
	models := make([]modelprofile.AvailableModel, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		identifier := strings.TrimSpace(item.ID)
		if identifier == "" {
			continue
		}
		if _, exists := seen[identifier]; exists {
			continue
		}
		seen[identifier] = struct{}{}
		reasoningLevels := item.SupportedReasoningEfforts.levels()
		if len(reasoningLevels) == 0 {
			reasoningLevels = item.SupportedReasoningLevels.levels()
		}
		source := ""
		if len(reasoningLevels) > 0 {
			source = "provider"
		}
		contextWindow := firstPositiveInt(int(item.ContextWindow), int(item.MaxContextWindow), int(item.ContextLength), int(item.MaxContextTokens))
		contextSource := ""
		if contextWindow > 0 {
			contextSource = modelcap.ContextWindowSourceProvider
		}
		contextBudget := modelcap.ResolveContextBudget(contextWindow, int(item.AutoCompactTokenLimit), contextSource)
		models = append(models, modelprofile.AvailableModel{
			ID:                        identifier,
			OwnedBy:                   strings.TrimSpace(item.OwnedBy),
			ContextWindowTokens:       contextBudget.WindowTokens,
			AutoCompactTokenLimit:     contextBudget.AutoCompactTokens,
			ContextWindowSource:       contextBudget.Source,
			ReasoningLevels:           reasoningLevels,
			ReasoningCapabilitySource: source,
		})
	}
	slices.SortFunc(models, func(a, b modelprofile.AvailableModel) int {
		return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
	})
	return models, nil
}

type reasoningRejectedError struct {
	kind modelutil.ReasoningRejectionKind
	err  error
}

func (e *reasoningRejectedError) Error() string { return e.err.Error() }
func (e *reasoningRejectedError) Unwrap() error { return e.err }

func (c *Client) negotiateOpen(ctx context.Context, request model.ChatRequest) (model.Stream, time.Duration, error) {
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
		stream, retryAfter, err := c.open(ctx, request)
		if err == nil {
			wireMode := "openai_effort"
			if !level.Valid() {
				wireMode = "provider_default"
			}
			c.recordReasoning(ctx, modelcap.ReasoningResult{Requested: requested, Resolved: level, Rejected: rejected, ControlUnsupported: controlUnsupported, WireMode: wireMode})
			return model.WithReasoningResolution(stream, requested, level), 0, nil
		}
		var rejection *reasoningRejectedError
		if !errors.As(err, &rejection) || !level.Valid() {
			return nil, retryAfter, err
		}
		if rejection.kind == modelutil.ReasoningRejectionValue {
			rejected = append(rejected, level)
			continue
		}
		if rejection.kind == modelutil.ReasoningRejectionControl {
			// Skip the remaining values and retry exactly once without the
			// optional field, preserving the provider's native behavior.
			rejected = nil
			controlUnsupported = true
			index = len(attempts) - 1
			continue
		}
		return nil, retryAfter, err
	}
	return nil, 0, fmt.Errorf("reasoning negotiation exhausted")
}

func (c *Client) recordReasoning(ctx context.Context, result modelcap.ReasoningResult) {
	if c.recorder != nil && result.Requested.Valid() {
		_ = c.recorder.RecordReasoningResult(ctx, c.profile.ID, c.profile.ModelID, result)
	}
}

func (c *Client) open(ctx context.Context, request model.ChatRequest) (model.Stream, time.Duration, error) {
	streaming := !request.DisableStreaming
	payload := requestPayload{Model: c.profile.ModelID, Stream: streaming, Temperature: c.profile.Temperature, MaxTokens: c.profile.MaxOutputTokens}
	if streaming {
		payload.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if request.ResolvedReasoningLevel.Valid() {
		payload.ReasoningEffort = string(request.ResolvedReasoningLevel)
	}
	qualifiedNames, providerNames, err := modelutil.BuildToolAliases(request.Tools)
	if err != nil {
		return nil, 0, err
	}
	request = modelutil.ProjectToolReferences(request, modelutil.ToolReferenceAliases(request, qualifiedNames, providerToolName))
	payload.Messages = make([]requestMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		mapped, err := mapRequestMessage(message, qualifiedNames)
		if err != nil {
			return nil, 0, err
		}
		payload.Messages = append(payload.Messages, mapped)
	}
	payload.Tools = make([]requestTool, 0, len(request.Tools))
	for _, definition := range request.Tools {
		payload.Tools = append(payload.Tools, requestTool{Type: "function", Function: requestFunction{Name: qualifiedNames[definition.Name], Description: definition.Description, Parameters: append(json.RawMessage(nil), definition.InputSchema...)}})
	}
	if request.DisableTools {
		payload.ToolChoice = "none"
	}
	if request.ForcedTool != "" {
		name := qualifiedNames[request.ForcedTool]
		if name == "" || request.DisableTools {
			return nil, 0, fmt.Errorf("invalid forced tool contract")
		}
		payload.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": name}}
		disabled := false
		payload.ParallelToolCalls = &disabled
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL(c.profile.BaseURL, "chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	c.applyHeaders(req)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, 0, classifyNetwork(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		response.Body.Close()
		if request.ResolvedReasoningLevel.Valid() {
			if kind := modelutil.ClassifyReasoningRejection(response.StatusCode, responseBody); kind != modelutil.ReasoningRejectionNone {
				classified := classifyStatus(response.StatusCode, response.Header, responseBody)
				return nil, 0, &reasoningRejectedError{kind: kind, err: classified}
			}
		}
		return nil, parseRetryAfter(response.Header.Get("Retry-After")), classifyStatus(response.StatusCode, response.Header, responseBody)
	}
	if !streaming {
		stream, err := openBufferedResponse(response, providerNames)
		return stream, 0, err
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes)
	return &stream{body: response.Body, scanner: scanner, toolCalls: make(map[int]*toolCallAccumulator), providerNames: providerNames}, 0, nil
}

func openBufferedResponse(response *http.Response, providerNames map[string]string) (model.Stream, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, modelutil.Error("MODEL_UNAVAILABLE", "模型响应读取失败，SciAide 将自动重试。", true, err)
	}
	if len(body) > maxResponseBodyBytes {
		return nil, modelError("MODEL_RESPONSE_INVALID", "模型返回内容过大，无法安全处理。", false, nil)
	}
	var payload bufferedResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, modelutil.ErrorWithDetails("MODEL_RESPONSE_INVALID", "模型返回了无法解析的数据。", modelutil.ProviderErrorDetails("Chat Completions response", response.StatusCode, body), false, err)
	}
	if payload.Error != nil {
		message := strings.TrimSpace(payload.Error.Message)
		if message == "" {
			message = "Chat Completions 请求未完成。"
		}
		retryable := modelutil.StreamErrorRetryable(payload.Error.Type, payload.Error.Code)
		code := "MODEL_REQUEST_REJECTED"
		if retryable {
			code = "MODEL_UNAVAILABLE"
		}
		return nil, modelutil.ErrorWithDetails(code, message, modelutil.ProviderErrorDetails("Chat Completions response", response.StatusCode, body), retryable, nil)
	}

	events := make([]model.Event, 0, len(payload.Choices)+2)
	finishReason := ""
	textObserved := false
	toolObserved := false
	for _, choice := range payload.Choices {
		if choice.Message.Content != "" {
			events = append(events, model.Event{Type: model.EventTextDelta, Text: choice.Message.Content})
			textObserved = textObserved || strings.TrimSpace(choice.Message.Content) != ""
		}
		for _, call := range choice.Message.ToolCalls {
			name := call.Function.Name
			name = modelutil.ResolveProviderToolName(name, providerNames)
			mapped := model.ToolCall{ID: call.ID, Name: name, Arguments: json.RawMessage(call.Function.Arguments)}
			if err := validateCompleteToolCall(mapped); err != nil {
				return nil, modelError("MODEL_TOOL_CALL_INVALID", "模型返回了不完整或无效的工具调用。", false, err)
			}
			events = append(events, model.Event{Type: model.EventToolCall, ToolCall: &mapped})
			toolObserved = true
		}
		if choice.FinishReason != nil {
			finishReason = *choice.FinishReason
		}
	}
	if finishReason == "" {
		switch {
		case toolObserved:
			finishReason = "tool_calls"
		case textObserved:
			finishReason = "stop"
		default:
			return nil, modelError("MODEL_RESPONSE_EMPTY", "模型服务返回了空响应，SciAide 将自动重试。", true, nil)
		}
	}
	if payload.Usage != nil {
		usage := payload.Usage.normalized()
		events = append(events, model.Event{Type: model.EventUsage, Usage: &usage})
	}
	events = append(events, model.Event{Type: model.EventDone, FinishReason: finishReason})
	return &bufferedStream{events: events}, nil
}

func mapRequestMessage(message model.Message, qualifiedNames map[string]string) (requestMessage, error) {
	content := message.Content
	mapped := requestMessage{Role: string(message.Role), Content: &content}
	switch message.Role {
	case model.RoleSystem:
	case model.RoleUser:
		content = wrapUntrusted("conversation_content", message.Content)
		images, err := openAIImageParts(message.Parts)
		if err != nil {
			return requestMessage{}, err
		}
		if len(images) > 0 {
			parts := make([]requestContentPart, 0, len(images)+1)
			if content != "" {
				parts = append(parts, requestContentPart{Type: "text", Text: content})
			}
			parts = append(parts, images...)
			mapped.Content = nil
			mapped.ContentParts = parts
		} else {
			mapped.Content = &content
		}
	case model.RoleAssistant:
		if len(message.ToolCalls) > maxToolCallsPerTurn {
			return requestMessage{}, fmt.Errorf("too many assistant tool calls")
		}
		mapped.ToolCalls = make([]requestToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if err := validateCompleteToolCall(call); err != nil {
				return requestMessage{}, err
			}
			name := qualifiedNames[call.Name]
			if name == "" {
				name = providerToolName(call.Name)
			}
			mapped.ToolCalls = append(mapped.ToolCalls, requestToolCall{ID: call.ID, Type: "function", Function: requestToolCallFunction{Name: name, Arguments: string(call.Arguments)}})
		}
		if len(mapped.ToolCalls) > 0 && message.Content == "" {
			mapped.Content = nil
		}
	case model.RoleTool:
		if strings.TrimSpace(message.ToolCallID) == "" || len(message.ToolCallID) > maxToolCallIDBytes {
			return requestMessage{}, fmt.Errorf("tool message requires a bounded tool call id")
		}
		mapped.ToolCallID = message.ToolCallID
		content = wrapUntrusted("tool_result", message.Content)
	default:
		return requestMessage{}, fmt.Errorf("unsupported model message role %q", message.Role)
	}
	if message.Role != model.RoleUser && !(message.Role == model.RoleAssistant && len(mapped.ToolCalls) > 0 && content == "") {
		mapped.Content = &content
	}
	return mapped, nil
}

func openAIImageParts(parts []model.ContentPart) ([]requestContentPart, error) {
	result := make([]requestContentPart, 0)
	for _, part := range parts {
		if part.Type != "input_image" {
			continue
		}
		dataURL, err := model.ImageDataURL(part)
		if err != nil {
			return nil, err
		}
		result = append(result, requestContentPart{Type: "image_url", ImageURL: &requestImageURL{URL: dataURL, Detail: "auto"}})
	}
	return result, nil
}

// All protocol adapters share the same deterministic naming policy.
func providerToolName(qualified string) string {
	return modelutil.ProviderToolName(qualified)
}

func (c *Client) applyHeaders(req *http.Request) {
	modelutil.ApplyBearerAndCustomHeaders(req, c.secret, c.profile.CustomHeaders)
}

type requestPayload struct {
	Model             string           `json:"model"`
	Messages          []requestMessage `json:"messages"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	ToolChoice        any              `json:"tool_choice,omitempty"`
	Tools             []requestTool    `json:"tools,omitempty"`
	Stream            bool             `json:"stream"`
	StreamOptions     *streamOptions   `json:"stream_options,omitempty"`
	Temperature       *float64         `json:"temperature,omitempty"`
	MaxTokens         *int             `json:"max_tokens,omitempty"`
	ReasoningEffort   string           `json:"reasoning_effort,omitempty"`
}

type requestMessage struct {
	Role         string               `json:"role"`
	Content      *string              `json:"content"`
	ContentParts []requestContentPart `json:"-"`
	ToolCalls    []requestToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string               `json:"tool_call_id,omitempty"`
}

func (m requestMessage) MarshalJSON() ([]byte, error) {
	content := any(m.Content)
	if len(m.ContentParts) > 0 {
		content = m.ContentParts
	}
	type wireMessage struct {
		Role       string            `json:"role"`
		Content    any               `json:"content"`
		ToolCalls  []requestToolCall `json:"tool_calls,omitempty"`
		ToolCallID string            `json:"tool_call_id,omitempty"`
	}
	return json.Marshal(wireMessage{Role: m.Role, Content: content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID})
}

type requestContentPart struct {
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	ImageURL *requestImageURL `json:"image_url,omitempty"`
}

type requestImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type requestTool struct {
	Type     string          `json:"type"`
	Function requestFunction `json:"function"`
}

type requestFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type requestToolCall struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function requestToolCallFunction `json:"function"`
}

type requestToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type toolCallAccumulator struct {
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}

type stream struct {
	body          io.ReadCloser
	scanner       *bufio.Scanner
	queue         []model.Event
	done          bool
	finishReason  string
	textObserved  bool
	usage         *responseUsage
	toolCalls     map[int]*toolCallAccumulator
	providerNames map[string]string
}

type bufferedStream struct {
	events []model.Event
	closed bool
}

func (s *bufferedStream) Recv() (model.Event, error) {
	if s.closed || len(s.events) == 0 {
		return model.Event{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *bufferedStream) Close() error {
	s.closed = true
	s.events = nil
	return nil
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
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			if s.finishReason == "" {
				switch {
				case len(s.toolCalls) > 0:
					s.finishReason = "tool_calls"
				case s.textObserved:
					s.finishReason = "stop"
				default:
					return model.Event{}, modelutil.ErrorWithDetails(
						"MODEL_RESPONSE_EMPTY",
						"模型服务返回了空响应，SciAide 将自动重试。",
						"Chat Completions stream received [DONE] without content, tool calls, or finish_reason.",
						true,
						nil,
					)
				}
			}
			if err := s.finalizeToolCalls(); err != nil {
				return model.Event{}, err
			}
			s.emitUsage()
			s.done = true
			s.queue = append(s.queue, model.Event{Type: model.EventDone, FinishReason: s.finishReason})
			return s.pop(), nil
		}
		var chunk responseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return model.Event{}, modelutil.ErrorWithDetails("MODEL_STREAM_INVALID", "模型返回了无法解析的流数据。", modelutil.ProviderErrorDetails("Chat Completions stream event", 0, []byte(data)), false, err)
		}
		if chunk.Error != nil {
			message := strings.TrimSpace(chunk.Error.Message)
			if message == "" {
				message = "Chat Completions 请求未完成。"
			}
			retryable := modelutil.StreamErrorRetryable(chunk.Error.Type, chunk.Error.Code)
			code := "MODEL_REQUEST_REJECTED"
			if retryable {
				code = "MODEL_UNAVAILABLE"
			}
			return model.Event{}, modelutil.ErrorWithDetails(code, message, modelutil.ProviderErrorDetails("Chat Completions stream event", 0, []byte(data)), retryable, nil)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: choice.Delta.Content})
				if strings.TrimSpace(choice.Delta.Content) != "" {
					s.textObserved = true
				}
			}
			for _, fragment := range choice.Delta.ToolCalls {
				if err := s.appendToolCall(fragment); err != nil {
					return model.Event{}, err
				}
			}
			if choice.FinishReason != nil {
				s.finishReason = *choice.FinishReason
				if s.finishReason == "tool_calls" {
					if err := s.finalizeToolCalls(); err != nil {
						return model.Event{}, err
					}
				}
			}
		}
		if chunk.Usage != nil {
			s.usage = mergeResponseUsage(s.usage, *chunk.Usage)
		}
		if len(s.queue) > 0 {
			return s.pop(), nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return model.Event{}, modelutil.Error("MODEL_UNAVAILABLE", "模型连接意外中断，SciAide 将自动重连。", true, err)
	}
	if s.finishReason != "" {
		if err := s.finalizeToolCalls(); err != nil {
			return model.Event{}, err
		}
		s.emitUsage()
		s.done = true
		s.queue = append(s.queue, model.Event{Type: model.EventDone, FinishReason: s.finishReason})
		return s.pop(), nil
	}
	return model.Event{}, modelutil.Error("MODEL_STREAM_INTERRUPTED", "模型流在完成标记前中断，SciAide 将自动重连。", true, io.ErrUnexpectedEOF)
}

func (s *stream) pop() model.Event {
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event
}

func mergeResponseUsage(current *responseUsage, next responseUsage) *responseUsage {
	if current == nil {
		result := next
		return &result
	}
	result := *current
	if next.PromptTokens > 0 || result.PromptTokens == 0 {
		result.PromptTokens = next.PromptTokens
	}
	if next.CompletionTokens > 0 || result.CompletionTokens == 0 {
		result.CompletionTokens = next.CompletionTokens
	}
	if next.PromptCacheHitTokens != nil {
		result.PromptCacheHitTokens = next.PromptCacheHitTokens
	}
	if next.PromptCacheMissTokens != nil {
		result.PromptCacheMissTokens = next.PromptCacheMissTokens
	}
	if next.CacheReadInputTokens != nil {
		result.CacheReadInputTokens = next.CacheReadInputTokens
	}
	if next.CacheCreationInputTokens != nil {
		result.CacheCreationInputTokens = next.CacheCreationInputTokens
	}
	if next.PromptTokensDetails != nil {
		result.PromptTokensDetails = next.PromptTokensDetails
	}
	if next.CompletionTokensDetails != nil {
		result.CompletionTokensDetails = next.CompletionTokensDetails
	}
	return &result
}

func (s *stream) emitUsage() {
	if s.usage == nil {
		return
	}
	usage := s.usage.normalized()
	s.usage = nil
	s.queue = append(s.queue, model.Event{Type: model.EventUsage, Usage: &usage})
}

func (s *stream) appendToolCall(fragment responseToolCall) error {
	if fragment.Index < 0 || fragment.Index >= maxToolCallsPerTurn {
		return modelError("MODEL_TOOL_CALL_INVALID", "模型返回了过多或无效的工具调用。", false, nil)
	}
	value := s.toolCalls[fragment.Index]
	if value == nil {
		value = &toolCallAccumulator{}
		s.toolCalls[fragment.Index] = value
	}
	if err := appendBounded(&value.id, fragment.ID, maxToolCallIDBytes); err != nil {
		return modelError("MODEL_TOOL_CALL_INVALID", "模型返回的工具调用 ID 过长。", false, err)
	}
	if err := appendBounded(&value.name, fragment.Function.Name, maxToolNameBytes); err != nil {
		return modelError("MODEL_TOOL_CALL_INVALID", "模型返回的工具名称过长。", false, err)
	}
	if err := appendBounded(&value.arguments, fragment.Function.Arguments, maxToolArgumentsBytes); err != nil {
		return modelError("MODEL_TOOL_CALL_INVALID", "模型返回的工具参数过大。", false, err)
	}
	return nil
}

func appendBounded(builder *strings.Builder, fragment string, limit int) error {
	if len(fragment) > limit-builder.Len() {
		return fmt.Errorf("stream fragment exceeds limit")
	}
	builder.WriteString(fragment)
	return nil
}

func (s *stream) finalizeToolCalls() error {
	if len(s.toolCalls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(s.toolCalls))
	for index := range s.toolCalls {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	for _, index := range indexes {
		value := s.toolCalls[index]
		name := value.name.String()
		name = modelutil.ResolveProviderToolName(name, s.providerNames)
		call := model.ToolCall{ID: value.id.String(), Name: name, Arguments: json.RawMessage(value.arguments.String())}
		if err := validateCompleteToolCall(call); err != nil {
			return modelError("MODEL_TOOL_CALL_INVALID", "模型返回了不完整或无效的工具调用。", false, err)
		}
		s.queue = append(s.queue, model.Event{Type: model.EventToolCall, ToolCall: &call})
	}
	s.toolCalls = make(map[int]*toolCallAccumulator)
	return nil
}

func validateCompleteToolCall(call model.ToolCall) error {
	if strings.TrimSpace(call.ID) == "" || len(call.ID) > maxToolCallIDBytes {
		return fmt.Errorf("invalid tool call id")
	}
	if strings.TrimSpace(call.Name) == "" || len(call.Name) > maxToolNameBytes {
		return fmt.Errorf("invalid tool call name")
	}
	if len(call.Arguments) == 0 || len(call.Arguments) > maxToolArgumentsBytes || !json.Valid(call.Arguments) {
		return fmt.Errorf("invalid tool call arguments")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(call.Arguments, &object); err != nil || object == nil {
		return fmt.Errorf("tool call arguments must be a JSON object")
	}
	return nil
}

func wrapUntrusted(label, value string) string {
	return "<untrusted_" + label + ">\n" + value + "\n</untrusted_" + label + ">"
}

func (s *stream) Close() error { s.done = true; return s.body.Close() }

type responseError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

type responseChunk struct {
	Error   *responseError `json:"error"`
	Choices []struct {
		Delta struct {
			Content   string             `json:"content"`
			ToolCalls []responseToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *responseUsage `json:"usage"`
}

type bufferedResponse struct {
	Error   *responseError `json:"error"`
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *responseUsage `json:"usage"`
}

type responseUsage struct {
	PromptTokens             int  `json:"prompt_tokens"`
	CompletionTokens         int  `json:"completion_tokens"`
	PromptCacheHitTokens     *int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens    *int `json:"prompt_cache_miss_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	PromptTokensDetails      *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u responseUsage) normalized() model.Usage {
	result := model.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	if u.CompletionTokensDetails != nil {
		result.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	if u.PromptTokensDetails != nil {
		result.CacheDetailsReported = true
		result.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.PromptCacheHitTokens != nil {
		result.CacheDetailsReported = true
		result.CachedInputTokens = max(result.CachedInputTokens, *u.PromptCacheHitTokens)
	}
	if u.PromptCacheMissTokens != nil {
		result.CacheDetailsReported = true
	}
	if u.CacheReadInputTokens != nil {
		result.CacheDetailsReported = true
		result.CachedInputTokens = max(result.CachedInputTokens, *u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != nil {
		result.CacheDetailsReported = true
		result.CacheWriteTokens = *u.CacheCreationInputTokens
	}
	// OpenAI-compatible APIs report prompt_tokens as a cache-inclusive total.
	// Keep the raw value for diagnostics, but normalize the durable statistics
	// into mutually exclusive fresh/read/create token buckets.
	result.FreshInputTokens = max(result.InputTokens-result.CachedInputTokens-result.CacheWriteTokens, 0)
	return result
}

type responseToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type modelsResponse struct {
	Data []struct {
		ID                        string              `json:"id"`
		OwnedBy                   string              `json:"owned_by"`
		ContextWindow             optionalPositiveInt `json:"context_window"`
		MaxContextWindow          optionalPositiveInt `json:"max_context_window"`
		ContextLength             optionalPositiveInt `json:"context_length"`
		MaxContextTokens          optionalPositiveInt `json:"max_context_tokens"`
		AutoCompactTokenLimit     optionalPositiveInt `json:"auto_compact_token_limit"`
		SupportedReasoningEfforts reasoningEfforts    `json:"supported_reasoning_efforts"`
		SupportedReasoningLevels  reasoningEfforts    `json:"supported_reasoning_levels"`
	} `json:"data"`
}

type optionalPositiveInt int

func (value *optionalPositiveInt) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*value = 0
		return nil
	}
	trimmed = strings.Trim(trimmed, `"`)
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed <= 0 {
		*value = 0
		return nil
	}
	*value = optionalPositiveInt(parsed)
	return nil
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

type reasoningEfforts []json.RawMessage

func (values *reasoningEfforts) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*values = nil
		return nil
	}
	if trimmed[0] == '[' {
		var result []json.RawMessage
		if err := json.Unmarshal(trimmed, &result); err != nil {
			// Capability metadata is optional and must never make the model list
			// unusable when a compatible provider emits a nonstandard shape.
			*values = nil
			return nil
		}
		*values = result
		return nil
	}
	if trimmed[0] == '"' {
		*values = []json.RawMessage{append(json.RawMessage(nil), trimmed...)}
		return nil
	}
	*values = nil
	return nil
}

func (values reasoningEfforts) levels() []modelcap.ReasoningLevel {
	levels := make([]modelcap.ReasoningLevel, 0, len(values))
	for _, raw := range values {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			var object struct {
				Effort          string `json:"effort"`
				ReasoningEffort string `json:"reasoning_effort"`
			}
			if json.Unmarshal(raw, &object) != nil {
				continue
			}
			name = object.Effort
			if name == "" {
				name = object.ReasoningEffort
			}
		}
		level := modelcap.ReasoningLevel(strings.ToLower(strings.TrimSpace(name)))
		if level.Valid() {
			levels = append(levels, level)
		}
	}
	return modelcap.NormalizeReasoningLevels(levels)
}

func endpointURL(baseURL, suffix string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/"+suffix) {
		return base
	}
	for _, endpoint := range []string{"/chat/completions", "/models"} {
		base = strings.TrimSuffix(base, endpoint)
	}
	return base + "/" + suffix
}

func classifyNetwork(err error) error {
	return modelutil.ClassifyNetwork(err)
}

func classifyStatus(status int, header http.Header, responseBody []byte) error {
	return modelutil.ClassifyStatusWithHeaders(status, header, responseBody)
}

func modelError(code, message string, retryable bool, cause error) error {
	return &apperr.Error{Code: code, UserMessage: message, Retryable: retryable, Cause: cause}
}

func parseRetryAfter(value string) time.Duration { return modelutil.ParseRetryAfter(value) }
