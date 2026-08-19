package modelutil

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

const (
	MaxToolCalls       = 32
	MaxToolCallIDBytes = 1024
	MaxToolNameBytes   = 160
	MaxToolArgsBytes   = 256 * 1024
	MaxProviderName    = 64
	MaxErrorBodyBytes  = 16 * 1024
)

func ProviderToolName(qualified string) string {
	qualified = strings.TrimSpace(qualified)
	safe := qualified != "" && len(qualified) <= MaxProviderName
	for _, c := range qualified {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		safe = false
	}
	if safe {
		return qualified
	}
	var prefix strings.Builder
	for _, c := range qualified {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			prefix.WriteRune(c)
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
	if len(value) > MaxProviderName-len(suffix) {
		value = value[:MaxProviderName-len(suffix)]
	}
	return value + suffix
}

func ValidateDefinition(def model.ToolDefinition) error {
	if strings.TrimSpace(def.Name) == "" || len(def.Name) > MaxToolNameBytes {
		return fmt.Errorf("invalid model tool name")
	}
	var object map[string]json.RawMessage
	if len(def.InputSchema) == 0 || json.Unmarshal(def.InputSchema, &object) != nil || object == nil {
		return fmt.Errorf("tool input schema must be a JSON object")
	}
	return nil
}

func ValidateToolCall(call model.ToolCall) error {
	if strings.TrimSpace(call.ID) == "" || len(call.ID) > MaxToolCallIDBytes {
		return fmt.Errorf("invalid tool call id")
	}
	if strings.TrimSpace(call.Name) == "" || len(call.Name) > MaxToolNameBytes {
		return fmt.Errorf("invalid tool call name")
	}
	if len(call.Arguments) == 0 || len(call.Arguments) > MaxToolArgsBytes || !json.Valid(call.Arguments) {
		return fmt.Errorf("invalid tool call arguments")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &object) != nil || object == nil {
		return fmt.Errorf("tool call arguments must be a JSON object")
	}
	return nil
}

func WrapUntrusted(label, value string) string {
	return "<untrusted_" + label + ">\n" + value + "\n</untrusted_" + label + ">"
}

func Endpoint(baseURL, suffix string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/"+suffix) {
		return base
	}
	for _, endpoint := range []string{"/chat/completions", "/responses", "/messages", "/models"} {
		base = strings.TrimSuffix(base, endpoint)
	}
	return base + "/" + suffix
}

func ApplyBearerAndCustomHeaders(req *http.Request, secret []byte, headers map[string]string) {
	if len(secret) > 0 {
		req.Header.Set("Authorization", "Bearer "+string(secret))
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
}

func ClassifyNetwork(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return Error("MODEL_TIMEOUT", "模型请求超时，请检查网络或增大超时时间。", false, err)
	}
	var u *url.Error
	if errors.As(err, &u) && u.Timeout() {
		return Error("MODEL_TIMEOUT", "模型请求超时，请检查网络或增大超时时间。", false, err)
	}
	return Error("MODEL_UNAVAILABLE", "暂时无法连接模型服务。", true, err)
}

func ClassifyStatus(status int, body []byte) error {
	details := ProviderErrorDetails("HTTP response", status, body)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrorWithDetails("MODEL_AUTH_FAILED", "模型服务拒绝了密钥，请重新设置 API Key。", details, false, nil)
	case http.StatusNotFound:
		return ErrorWithDetails("MODEL_NOT_FOUND", "模型或 API 地址不存在，请检查 Base URL、接口协议和 Model ID。", details, false, nil)
	case http.StatusTooManyRequests:
		return ErrorWithDetails("MODEL_RATE_LIMITED", "模型服务繁忙或已达到限额，请稍后重试。", details, true, nil)
	default:
		if status >= 500 {
			return ErrorWithDetails("MODEL_UNAVAILABLE", "模型服务暂时不可用。", details, true, fmt.Errorf("HTTP %d", status))
		}
		message := fmt.Sprintf("模型服务拒绝了请求（HTTP %d）。", status)
		if detail := ProviderErrorMessage(body); detail != "" {
			message = fmt.Sprintf("模型服务拒绝了请求（HTTP %d）：%s", status, detail)
		}
		return ErrorWithDetails("MODEL_REQUEST_REJECTED", message, details, false, fmt.Errorf("HTTP %d", status))
	}
}

func ProviderErrorMessage(body []byte) string {
	var payload map[string]any
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return ""
	}
	value := nestedString(payload, "error", "message")
	if value == "" {
		value = nestedString(payload, "response", "error", "message")
	}
	if value == "" {
		value, _ = payload["message"].(string)
	}
	value = providerBearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = providerSecretPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = strings.Join(strings.Fields(value), " ")
	if runes := []rune(value); len(runes) > 300 {
		value = string(runes[:300]) + "…"
	}
	return value
}

var (
	providerBearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s"',}]+`)
	providerSecretPattern = regexp.MustCompile(`(?i)((?:api[_ -]?key|token|secret|password|authorization|cookie)\s*(?:is|=|:)\s*)[^\s,;]+`)
)

func ProviderErrorDetails(source string, status int, body []byte) string {
	lines := []string{}
	if source = strings.TrimSpace(source); source != "" {
		lines = append(lines, "Source: "+source)
	}
	if status > 0 {
		lines = append(lines, fmt.Sprintf("HTTP status: %d", status))
	}
	if payload := sanitizedProviderPayload(body); payload != "" {
		lines = append(lines, "Provider payload:\n"+payload)
	} else {
		lines = append(lines, "Provider payload: <empty>")
	}
	return boundProviderDetails(strings.Join(lines, "\n"))
}

func sanitizedProviderPayload(body []byte) string {
	value := strings.TrimSpace(string(body))
	if value == "" {
		return ""
	}
	var decoded any
	if json.Unmarshal(body, &decoded) == nil {
		decoded = redactProviderValue(decoded, "")
		if encoded, err := json.MarshalIndent(decoded, "", "  "); err == nil {
			value = string(encoded)
		}
	}
	value = providerBearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = providerSecretPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return boundProviderDetails(value)
}

func redactProviderValue(value any, key string) any {
	if sensitiveProviderKey(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			typed[childKey] = redactProviderValue(child, childKey)
		}
	case []any:
		for index := range typed {
			typed[index] = redactProviderValue(typed[index], key)
		}
	case string:
		return providerSecretPattern.ReplaceAllString(providerBearerPattern.ReplaceAllString(typed, `${1}[REDACTED]`), `${1}[REDACTED]`)
	}
	return value
}

func sensitiveProviderKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
	for _, marker := range []string{"authorization", "api_key", "apikey", "token", "secret", "cookie", "password", "credential"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func nestedString(value map[string]any, path ...string) string {
	var current any = value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	result, _ := current.(string)
	return strings.TrimSpace(result)
}

func boundProviderDetails(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 8_000 {
		return string(runes[:8_000]) + "..."
	}
	return string(runes)
}

func Error(code, message string, retryable bool, cause error) error {
	return &apperr.Error{Code: code, UserMessage: message, Retryable: retryable, Cause: cause}
}

func ErrorWithDetails(code, message, details string, retryable bool, cause error) error {
	return &apperr.Error{Code: code, UserMessage: message, Details: boundProviderDetails(details), Retryable: retryable, Cause: cause}
}

func ParseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil && seconds > 0 && seconds <= 60 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func ReadErrorBody(body io.Reader) []byte {
	value, _ := io.ReadAll(io.LimitReader(body, MaxErrorBodyBytes))
	return value
}

type ReasoningRejectionKind int

const (
	ReasoningRejectionNone ReasoningRejectionKind = iota
	// ReasoningRejectionValue means the control exists but this effort value
	// is invalid. Adapters may retry the same request at the next lower tier.
	ReasoningRejectionValue
	// ReasoningRejectionControl means the field or thinking mode itself is
	// unsupported. Adapters may retry once using provider-native defaults.
	ReasoningRejectionControl
)

// ClassifyReasoningRejection only recognizes request-shape failures that are
// safe to retry before a stream has started. Authentication, tools, context,
// rate limits and transport errors are deliberately excluded.
func ClassifyReasoningRejection(status int, body []byte) ReasoningRejectionKind {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return ReasoningRejectionNone
	}
	detail := strings.ToLower(ProviderErrorMessage(body))
	if detail == "" {
		detail = strings.ToLower(string(body))
	}
	controls := []string{"reasoning_effort", "reasoning effort", "reasoning.effort", "output_config", "effort", "thinking", "budget_tokens", "budgettokens"}
	mentionsControl := false
	for _, control := range controls {
		if strings.Contains(detail, control) {
			mentionsControl = true
			break
		}
	}
	if !mentionsControl {
		return ReasoningRejectionNone
	}
	for _, marker := range []string{"invalid value", "unsupported value", "not a valid", "must be one of", "supported values", "expected one of", "allowed values"} {
		if strings.Contains(detail, marker) {
			return ReasoningRejectionValue
		}
	}
	if mentionsReasoningLevel(detail) && (strings.Contains(detail, "not supported") || strings.Contains(detail, "unsupported")) {
		return ReasoningRejectionValue
	}
	for _, marker := range []string{"unknown parameter", "unsupported parameter", "unrecognized parameter", "extra inputs", "not supported", "is unsupported", "does not support", "unknown field", "unexpected field"} {
		if strings.Contains(detail, marker) {
			return ReasoningRejectionControl
		}
	}
	return ReasoningRejectionNone
}

func mentionsReasoningLevel(detail string) bool {
	for _, field := range strings.FieldsFunc(detail, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		switch field {
		case "low", "medium", "high", "xhigh", "max":
			return true
		}
	}
	return false
}

func ReasoningControlRejected(status int, body []byte) bool {
	return ClassifyReasoningRejection(status, body) != ReasoningRejectionNone
}

type SliceStream struct {
	Events    []model.Event
	Index     int
	CloseFunc func() error
}

func (s *SliceStream) Recv() (model.Event, error) {
	if s.Index >= len(s.Events) {
		return model.Event{}, io.EOF
	}
	e := s.Events[s.Index]
	s.Index++
	return e, nil
}
func (s *SliceStream) Close() error {
	if s.CloseFunc != nil {
		return s.CloseFunc()
	}
	return nil
}
