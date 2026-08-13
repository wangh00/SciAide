package openai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

const (
	maxStreamLineBytes    = 1024 * 1024
	maxToolCallsPerTurn   = 32
	maxToolCallIDBytes    = 1024
	maxToolNameBytes      = 160
	maxToolArgumentsBytes = 256 * 1024
	maxProviderToolName   = 64
	maxErrorBodyBytes     = 16 * 1024
)

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

func (c *Client) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	return c.openWithRetry(ctx, request)
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
		models = append(models, modelprofile.AvailableModel{ID: identifier, OwnedBy: strings.TrimSpace(item.OwnedBy)})
	}
	slices.SortFunc(models, func(a, b modelprofile.AvailableModel) int {
		return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
	})
	return models, nil
}

func (c *Client) openWithRetry(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		stream, retryAfter, err := c.open(ctx, request)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		var public *apperr.Error
		if !errors.As(err, &public) || !public.Retryable || attempt == 2 {
			break
		}
		delay := time.Duration(250*(1<<attempt))*time.Millisecond + time.Duration(rand.IntN(150))*time.Millisecond
		if retryAfter > delay {
			delay = retryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (c *Client) open(ctx context.Context, request model.ChatRequest) (model.Stream, time.Duration, error) {
	payload := requestPayload{Model: c.profile.ModelID, Stream: true, StreamOptions: &streamOptions{IncludeUsage: true}, Temperature: c.profile.Temperature, MaxTokens: c.profile.MaxOutputTokens}
	if request.ResolvedReasoningLevel.Valid() {
		payload.ReasoningEffort = string(request.ResolvedReasoningLevel)
	}
	providerNames := make(map[string]string, len(request.Tools))
	qualifiedNames := make(map[string]string, len(request.Tools))
	for _, definition := range request.Tools {
		if err := validateModelToolDefinition(definition); err != nil {
			return nil, 0, err
		}
		providerName := providerToolName(definition.Name)
		if existing, duplicate := providerNames[providerName]; duplicate && existing != definition.Name {
			return nil, 0, fmt.Errorf("model tool name alias collision")
		}
		providerNames[providerName] = definition.Name
		qualifiedNames[definition.Name] = providerName
	}
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
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL(c.profile.BaseURL, "chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	c.applyHeaders(req)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, 0, classifyNetwork(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		response.Body.Close()
		return nil, parseRetryAfter(response.Header.Get("Retry-After")), classifyStatus(response.StatusCode, response.Header, responseBody)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes)
	return &stream{body: response.Body, scanner: scanner, toolCalls: make(map[int]*toolCallAccumulator), providerNames: providerNames}, 0, nil
}

func mapRequestMessage(message model.Message, qualifiedNames map[string]string) (requestMessage, error) {
	content := message.Content
	mapped := requestMessage{Role: string(message.Role), Content: &content}
	switch message.Role {
	case model.RoleSystem:
	case model.RoleUser:
		content = wrapUntrusted("conversation_content", message.Content)
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
	return mapped, nil
}

// providerToolName maps SciAide's qualified names (which intentionally use
// dots for namespaces) to the conservative OpenAI-compatible function-name
// alphabet. A hash suffix keeps aliases stable and prevents dot/underscore
// replacements from silently colliding.
func providerToolName(qualified string) string {
	qualified = strings.TrimSpace(qualified)
	providerSafe := qualified != "" && len(qualified) <= maxProviderToolName
	for _, character := range qualified {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		providerSafe = false
		break
	}
	if providerSafe {
		return qualified
	}
	var prefix strings.Builder
	for _, character := range qualified {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			prefix.WriteRune(character)
		} else {
			prefix.WriteByte('_')
		}
	}
	value := strings.Trim(prefix.String(), "_-")
	if value == "" {
		value = "tool"
	}
	digest := sha256.Sum256([]byte(qualified))
	suffix := fmt.Sprintf("_%x", digest[:6])
	if len(value) > maxProviderToolName-len(suffix) {
		value = value[:maxProviderToolName-len(suffix)]
	}
	return value + suffix
}

func validateModelToolDefinition(definition model.ToolDefinition) error {
	name := strings.TrimSpace(definition.Name)
	if name == "" || len(name) > maxToolNameBytes {
		return fmt.Errorf("invalid model tool name")
	}
	var object map[string]json.RawMessage
	if len(definition.InputSchema) == 0 || json.Unmarshal(definition.InputSchema, &object) != nil || object == nil {
		return fmt.Errorf("tool input schema must be a JSON object")
	}
	return nil
}

func (c *Client) applyHeaders(req *http.Request) {
	if len(c.secret) > 0 {
		req.Header.Set("Authorization", "Bearer "+string(c.secret))
	}
	for name, value := range c.profile.CustomHeaders {
		req.Header.Set(name, value)
	}
}

type requestPayload struct {
	Model           string           `json:"model"`
	Messages        []requestMessage `json:"messages"`
	Tools           []requestTool    `json:"tools,omitempty"`
	Stream          bool             `json:"stream"`
	StreamOptions   *streamOptions   `json:"stream_options,omitempty"`
	Temperature     *float64         `json:"temperature,omitempty"`
	MaxTokens       *int             `json:"max_tokens,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
}

type requestMessage struct {
	Role       string            `json:"role"`
	Content    *string           `json:"content"`
	ToolCalls  []requestToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
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
	toolCalls     map[int]*toolCallAccumulator
	providerNames map[string]string
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
			if err := s.finalizeToolCalls(); err != nil {
				return model.Event{}, err
			}
			s.done = true
			s.queue = append(s.queue, model.Event{Type: model.EventDone, FinishReason: s.finishReason})
			return s.pop(), nil
		}
		var chunk responseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return model.Event{}, modelError("MODEL_STREAM_INVALID", "模型返回了无法解析的流数据。", false, err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: choice.Delta.Content})
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
			usage := chunk.Usage.normalized()
			s.queue = append(s.queue, model.Event{Type: model.EventUsage, Usage: &usage})
		}
		if len(s.queue) > 0 {
			return s.pop(), nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return model.Event{}, modelError("MODEL_UNAVAILABLE", "模型连接意外中断。", false, err)
	}
	if s.finishReason != "" {
		if err := s.finalizeToolCalls(); err != nil {
			return model.Event{}, err
		}
		s.done = true
		s.queue = append(s.queue, model.Event{Type: model.EventDone, FinishReason: s.finishReason})
		return s.pop(), nil
	}
	return model.Event{}, modelError("MODEL_STREAM_INVALID", "模型流在完成标记前结束。", false, io.ErrUnexpectedEOF)
}

func (s *stream) pop() model.Event {
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event
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
		if qualified := s.providerNames[name]; qualified != "" {
			name = qualified
		}
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

type responseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string             `json:"content"`
			ToolCalls []responseToolCall `json:"tool_calls"`
		} `json:"delta"`
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
}

func (u responseUsage) normalized() model.Usage {
	result := model.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
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
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
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
	if errors.Is(err, context.DeadlineExceeded) {
		return modelError("MODEL_TIMEOUT", "模型请求超时，请检查网络或增大超时时间。", false, err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return modelError("MODEL_TIMEOUT", "模型请求超时，请检查网络或增大超时时间。", false, err)
	}
	return modelError("MODEL_UNAVAILABLE", "暂时无法连接模型服务。", true, err)
}

func classifyStatus(status int, _ http.Header, responseBody []byte) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return modelError("MODEL_AUTH_FAILED", "模型服务拒绝了密钥，请重新设置 API Key。", false, nil)
	case http.StatusNotFound:
		return modelError("MODEL_NOT_FOUND", "模型或 API 地址不存在，请检查 Base URL 和 Model ID。", false, nil)
	case http.StatusTooManyRequests:
		return modelError("MODEL_RATE_LIMITED", "模型服务繁忙或已达到限额，请稍后重试。", true, nil)
	default:
		if status >= 500 {
			return modelError("MODEL_UNAVAILABLE", "模型服务暂时不可用。", true, fmt.Errorf("HTTP %d", status))
		}
		message := "模型服务拒绝了请求，请检查模型配置。"
		if detail := providerErrorMessage(responseBody); detail != "" {
			message = fmt.Sprintf("模型服务拒绝了请求（HTTP %d）：%s", status, detail)
		}
		return modelError("MODEL_REQUEST_REJECTED", message, false, fmt.Errorf("HTTP %d", status))
	}
}

func providerErrorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return ""
	}
	value := strings.TrimSpace(payload.Error.Message)
	if value == "" {
		value = strings.TrimSpace(payload.Message)
	}
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > 300 {
		value = string([]rune(value)[:300]) + "…"
	}
	return value
}

func modelError(code, message string, retryable bool, cause error) error {
	return &apperr.Error{Code: code, UserMessage: message, Retryable: retryable, Cause: cause}
}

func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil && seconds > 0 && seconds <= 60 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}
