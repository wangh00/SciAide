package modelutil

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/browserhttp"
	"github.com/wangh00/SciAide/internal/httpua"
	"io"
	"net"
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

// NewStreamingHTTPClient limits how long a model endpoint may take to begin
// its response without imposing a deadline on the full SSE body. A total
// http.Client.Timeout would abort healthy long-running reasoning and tool
// turns even while the provider is still streaming data.
func NewStreamingHTTPClient(responseTimeout time.Duration) *http.Client {
	if responseTimeout <= 0 {
		responseTimeout = 60 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: responseTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = responseTimeout
	transport.ResponseHeaderTimeout = responseTimeout
	return &http.Client{Transport: browserhttp.New(transport)}
}

func ProviderToolName(qualified string) string {
	qualified = strings.TrimSpace(qualified)
	if strings.HasPrefix(qualified, "builtin.") {
		name := strings.ReplaceAll(strings.TrimPrefix(qualified, "builtin."), ".", "_")
		if name != "" && len(name) <= MaxProviderName {
			valid := true
			for _, c := range name {
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
					valid = false
					break
				}
			}
			if valid {
				return name
			}
		}
	}
	return externalProviderToolName(qualified)
}

// External names keep their stable, collision-resistant form.
// Adapters reject collisions in each request before any network IO.
func externalProviderToolName(qualified string) string {
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

// Exact names win; one extra trailing separator may refer to a declared name.
func ResolveProviderToolName(providerName string, aliases map[string]string) string {
	if qualified := aliases[providerName]; qualified != "" {
		return qualified
	}
	if strings.HasSuffix(providerName, "_") && !strings.HasSuffix(providerName, "__") {
		if qualified := aliases[strings.TrimSuffix(providerName, "_")]; qualified != "" {
			return qualified
		}
	}
	return providerName
}

// ProviderToolAliasForQualified returns the exact provider alias declared for
// a qualified tool. It is used when persisting provider protocol state so a
// repaired call can be replayed with the declared spelling on the next turn.
func ProviderToolAliasForQualified(qualified string, aliases map[string]string) string {
	for alias, value := range aliases {
		if value == qualified {
			return alias
		}
	}
	return qualified
}

// BuildToolAliases validates definitions and builds both directions before IO.
func BuildToolAliases(definitions []model.ToolDefinition) (map[string]string, map[string]string, error) {
	forward, reverse := map[string]string{}, map[string]string{}
	for _, def := range definitions {
		if err := ValidateDefinition(def); err != nil {
			return nil, nil, err
		}
		alias := ProviderToolName(def.Name)
		if _, exists := reverse[alias]; exists {
			return nil, nil, fmt.Errorf("duplicate or colliding model tool name: %q", alias)
		}
		forward[def.Name], reverse[alias] = alias, def.Name
	}
	return forward, reverse, nil
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
	httpua.Apply(req)
}

func ClassifyNetwork(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if permanentTLSError(err) {
		return ErrorWithDetails("MODEL_TLS_INVALID", "无法验证模型服务的 TLS 证书，请检查 Base URL、系统时间或证书链。", "TLS certificate verification failed.\n"+err.Error(), false, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorWithDetails("MODEL_TIMEOUT", "模型请求暂时超时，SciAide 将自动重试。", "超时阶段：等待模型连接、TLS 握手或响应头。\n服务端未返回 HTTP 错误载荷。", true, err)
	}
	var u *url.Error
	if errors.As(err, &u) && u.Timeout() {
		return ErrorWithDetails("MODEL_TIMEOUT", "模型请求暂时超时，SciAide 将自动重试。", "超时阶段：等待模型连接、TLS 握手或响应头。\n服务端未返回 HTTP 错误载荷。", true, err)
	}
	return Error("MODEL_UNAVAILABLE", "暂时无法连接模型服务。", true, err)
}

func permanentTLSError(err error) bool {
	var verification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	return errors.As(err, &verification) || errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &recordHeader)
}

func ClassifyStatus(status int, body []byte) error {
	details := ProviderErrorDetails("HTTP response", status, body)
	var err error
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		err = ErrorWithDetails("MODEL_AUTH_FAILED", "模型服务拒绝了密钥，请重新设置 API Key。", details, false, nil)
	case http.StatusNotFound:
		err = ErrorWithDetails("MODEL_NOT_FOUND", "模型或 API 地址不存在，请检查 Base URL、接口协议和 Model ID。", details, false, nil)
	case http.StatusRequestTimeout:
		err = ErrorWithDetails("MODEL_TIMEOUT", "模型服务请求暂时超时，SciAide 将自动重试。", details, true, nil)
	case http.StatusTooManyRequests:
		err = ErrorWithDetails("MODEL_RATE_LIMITED", "模型服务繁忙或已达到限额，请稍后重试。", details, true, nil)
	default:
		if status >= 500 {
			err = ErrorWithDetails("MODEL_UNAVAILABLE", "模型服务暂时不可用。", details, true, fmt.Errorf("HTTP %d", status))
			break
		}
		message := fmt.Sprintf("模型服务拒绝了请求（HTTP %d）。", status)
		if detail := ProviderErrorMessage(body); detail != "" {
			message = fmt.Sprintf("模型服务拒绝了请求（HTTP %d）：%s", status, detail)
		}
		err = ErrorWithDetails("MODEL_REQUEST_REJECTED", message, details, false, fmt.Errorf("HTTP %d", status))
	}
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		appErr.HTTPStatus = status
	}
	return err
}

func ClassifyStatusWithHeaders(status int, headers http.Header, body []byte) error {
	err := ClassifyStatus(status, body)
	var appErr *apperr.Error
	if errors.As(err, &appErr) && appErr.Retryable {
		appErr.RetryAfter = ParseRetryAfter(headers.Get("Retry-After"))
	}
	return err
}

func IsRetryable(err error) bool {
	var appErr *apperr.Error
	return errors.As(err, &appErr) && appErr.Retryable
}

func RetryAfter(err error) time.Duration {
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		return appErr.RetryAfter
	}
	return 0
}

func StreamErrorRetryable(errorType, code string) bool {
	errorType = strings.ToLower(strings.TrimSpace(errorType))
	code = strings.ToLower(strings.TrimSpace(code))
	// Match the explicit transport failure, not the generic upstream wrapper.
	if code == "stream_read_error" || (code == "" && errorType == "stream_read_error") {
		return true
	}
	value := errorType + " " + code
	for _, marker := range []string{"overloaded", "rate_limit", "server_error", "internal_error", "service_unavailable", "temporarily_unavailable", "timeout_error"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
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
	value = strings.TrimSpace(value)
	seconds, err := strconv.Atoi(value)
	if err == nil && seconds > 0 && seconds <= 60 {
		return time.Duration(seconds) * time.Second
	}
	if at, parseErr := http.ParseTime(value); parseErr == nil {
		delay := time.Until(at)
		if delay > 0 {
			return min(delay, 60*time.Second)
		}
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
