package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
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
	return model.Capabilities{Streaming: true, ToolCalling: true}, nil
}

func (c *Client) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	return c.openWithRetry(ctx, request)
}

func (c *Client) Test(ctx context.Context, profile modelprofile.Profile, secret []byte) error {
	tester := NewWithHTTPClient(profile, secret, c.http)
	endpoint := endpointURL(profile.BaseURL, "models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	tester.applyHeaders(req)
	response, err := tester.http.Do(req)
	if err != nil {
		return classifyNetwork(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return classifyStatus(response.StatusCode, response.Header)
	}
	return nil
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
	payload.Messages = make([]requestMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		content := message.Content
		if message.Role != model.RoleSystem {
			content = "<untrusted_conversation_content>\n" + content + "\n</untrusted_conversation_content>"
		}
		payload.Messages = append(payload.Messages, requestMessage{Role: string(message.Role), Content: content})
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
		io.CopyN(io.Discard, response.Body, 4096)
		response.Body.Close()
		return nil, parseRetryAfter(response.Header.Get("Retry-After")), classifyStatus(response.StatusCode, response.Header)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	return &stream{body: response.Body, scanner: scanner}, 0, nil
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
	Model         string           `json:"model"`
	Messages      []requestMessage `json:"messages"`
	Stream        bool             `json:"stream"`
	StreamOptions *streamOptions   `json:"stream_options,omitempty"`
	Temperature   *float64         `json:"temperature,omitempty"`
	MaxTokens     *int             `json:"max_tokens,omitempty"`
}
type requestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type stream struct {
	body         io.ReadCloser
	scanner      *bufio.Scanner
	queue        []model.Event
	done         bool
	finishReason string
}

func (s *stream) Recv() (model.Event, error) {
	if len(s.queue) > 0 {
		event := s.queue[0]
		s.queue = s.queue[1:]
		return event, nil
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
			s.done = true
			return model.Event{Type: model.EventDone, FinishReason: s.finishReason}, nil
		}
		var chunk responseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return model.Event{}, modelError("MODEL_STREAM_INVALID", "模型返回了无法解析的流数据。", false, err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				s.queue = append(s.queue, model.Event{Type: model.EventTextDelta, Text: choice.Delta.Content})
			}
			if choice.FinishReason != nil {
				s.finishReason = *choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			s.queue = append(s.queue, model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}})
		}
		if len(s.queue) > 0 {
			event := s.queue[0]
			s.queue = s.queue[1:]
			return event, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return model.Event{}, modelError("MODEL_UNAVAILABLE", "模型连接意外中断。", false, err)
	}
	if s.finishReason != "" {
		s.done = true
		return model.Event{Type: model.EventDone, FinishReason: s.finishReason}, nil
	}
	return model.Event{}, modelError("MODEL_STREAM_INVALID", "模型流在完成标记前结束。", false, io.ErrUnexpectedEOF)
}

func (s *stream) Close() error { s.done = true; return s.body.Close() }

type responseChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func endpointURL(baseURL, suffix string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") && suffix == "chat/completions" {
		return base
	}
	if strings.HasSuffix(base, "/chat/completions") {
		base = strings.TrimSuffix(base, "/chat/completions")
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

func classifyStatus(status int, _ http.Header) error {
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
		return modelError("MODEL_REQUEST_REJECTED", "模型服务拒绝了请求，请检查模型配置。", false, fmt.Errorf("HTTP %d", status))
	}
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
