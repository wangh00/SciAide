package modelutil

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func providerJSONLocation(err error) (string, int64) {
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	switch {
	case err == nil:
		return "valid_json", 0
	case errors.As(err, &syntax):
		if syntax.Error() == "unexpected end of JSON input" {
			return "unexpected_end", syntax.Offset
		}
		return "invalid_syntax", syntax.Offset
	case errors.As(err, &mismatch):
		return "type_mismatch", mismatch.Offset
	default:
		return "invalid_json", 0
	}
}

// ProviderJSONErrorCause deliberately does not wrap the original error: a
// UnmarshalTypeError can include provider values or private field names in its
// Error string, which may subsequently be logged outside PublicError.Details.
func ProviderJSONErrorCause(err error) error {
	kind, offset := providerJSONLocation(err)
	return fmt.Errorf("provider JSON %s at UTF-8 byte offset %d (1-based)", kind, offset)
}

// ProviderProtocolErrorCause must not expose arbitrary native item types or
// source values embedded in adapter validation errors via Error().
func ProviderProtocolErrorCause(err error) error {
	if err == nil {
		return nil
	}
	kind, _ := providerJSONLocation(err)
	if kind != "invalid_json" {
		return ProviderJSONErrorCause(err)
	}
	return errors.New("invalid provider stream event structure")
}

func providerPayloadMetadata(body []byte, parseErr error) map[string]any {
	kind, offset := providerJSONLocation(parseErr)
	return map[string]any{"payloadOmitted": true, "payloadBytes": len(body), "payloadSha256": fmt.Sprintf("%x", sha256.Sum256(body)), "jsonError": kind, "jsonByteOffset": offset}
}

func encodedProviderMetadata(value map[string]any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

// StreamEventErrorDetails never copies native text, reasoning, signatures,
// tool arguments, item IDs or upstream messages into a public error. Even a
// syntactically valid event may contain private state before a protocol error.
func StreamEventErrorDetails(source string, body []byte) string {
	return ProviderErrorDetails(source, 0, encodedProviderMetadata(streamEventMetadata(body)))
}

// Keep the original malformed envelope's fingerprint, plus safe identities
// from the separately validated terminal error. No prefix content is retained.
func CoalescedStreamErrorDetails(source string, envelope, terminal []byte) string {
	metadata := streamEventMetadata(envelope)
	failure := streamEventMetadata(terminal)
	for _, key := range []string{"type", "providerType", "providerCode"} {
		metadata[key] = failure[key]
	}
	metadata["eventBoundary"] = "complete"
	metadata["framingViolation"] = "missing_separator_before_terminal_error"
	return ProviderErrorDetails(source, 0, encodedProviderMetadata(metadata))
}

// ProtocolDecodeErrorDetails also handles valid JSON with incompatible field
// types; generic HTTP-body redaction cannot safely retain such native content.
func ProtocolDecodeErrorDetails(source string, body []byte, decodeErr error) string {
	metadata := streamEventMetadata(body)
	metadata["jsonError"], metadata["jsonByteOffset"] = providerJSONLocation(decodeErr)
	return ProviderErrorDetails(source, 0, encodedProviderMetadata(metadata))
}

func streamEventMetadata(body []byte) map[string]any {
	var value map[string]any
	err := json.Unmarshal(body, &value)
	metadata := providerPayloadMetadata(body, err)
	if err == nil {
		metadata["type"] = safeDiagnosticIdentity(stringField(value, "type"))
		if index, ok := value["output_index"].(float64); ok && index >= 0 && index <= 1<<31 && index == float64(int64(index)) {
			metadata["output_index"] = int64(index)
		}
		if item, ok := value["item"].(map[string]any); ok {
			metadata["item_type"] = safeDiagnosticIdentity(stringField(item, "type"))
		}
		failure, _ := value["error"].(map[string]any)
		if response, ok := value["response"].(map[string]any); ok && failure == nil {
			failure, _ = response["error"].(map[string]any)
		}
		if failure == nil && (stringField(value, "type") == "error" || stringField(value, "code") != "") {
			failure = value // Responses also permits top-level type:error/code.
		}
		if failure != nil {
			metadata["providerCode"] = safeDiagnosticIdentity(stringField(failure, "code"))
			metadata["providerType"] = safeDiagnosticIdentity(stringField(failure, "type"))
		}
	}
	return metadata
}

func stringField(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

// Only known diagnostic enums survive; arbitrary provider-controlled strings
// cannot disguise credentials or private text as an event/error identifier.
func safeDiagnosticIdentity(value string) string {
	switch value {
	case "", "error", "upstream_error", "api_error", "unknown_error", "invalid_request", "invalid_request_error", "invalid_api_key", "authentication_error", "permission_error", "permission_denied", "unauthorized", "forbidden", "invalid_parameter", "invalid_parameters", "context_length_exceeded", "model_not_found", "insufficient_quota", "quota_exceeded", "billing_hard_limit_reached", "usage_limit_reached", "no_userid", "content_policy_violation", "invalid_prompt", "stream_read_error", "upstream_stream_read_error", "upstream_http2_stream_error", "gateway_concurrency_limit", "server_error", "internal_error", "service_unavailable", "temporarily_unavailable", "timeout_error", "rate_limit_error", "rate_limit_exceeded", "overloaded_error":
		return value
	case "response.created", "response.in_progress", "response.completed", "response.failed", "response.error", "response.incomplete", "response.output_item.added", "response.output_item.done", "response.output_text.delta", "response.output_text.done", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.refusal.delta", "response.refusal.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop", "ping", "message", "reasoning", "function_call", "tool_use", "text", "thinking", "redacted_thinking":
		return value
	}
	return "unrecognized"
}

// SanitizeProviderDiagnostic handles legacy or already formatted diagnostics
// without trusting their prefix. All native payloads are projected as metadata,
// including legacy diagnostics mislabeled as an HTTP response.
func SanitizeProviderDiagnostic(detail string) string {
	const marker = "\nProvider payload:\n"
	payload := []byte(detail)
	if strings.HasPrefix(detail, "Source: ") {
		if index := strings.Index(detail, marker); index >= 0 {
			payload = []byte(detail[index+len(marker):])
		}
	}
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil || value["payloadOmitted"] != true {
		return StreamEventErrorDetails("original error", payload)
	}
	// Preserve only verified metadata fields, not arbitrary extra keys
	// smuggled into a diagnostic-looking object. Never copy the source label.
	clean := map[string]any{"payloadOmitted": true}
	for _, key := range []string{"payloadBytes", "jsonByteOffset", "output_index"} {
		if n, ok := value[key].(float64); ok && n >= 0 && n <= 1<<53 && n == float64(int64(n)) {
			clean[key] = n
		}
	}
	if hash, ok := value["payloadSha256"].(string); ok && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == "" {
		clean["payloadSha256"] = hash
	}
	for _, key := range []string{"type", "item_type", "providerCode", "providerType"} {
		if s, ok := value[key].(string); ok {
			clean[key] = safeDiagnosticIdentity(s)
		}
	}
	switch value["jsonError"] {
	case "valid_json", "unexpected_end", "invalid_syntax", "type_mismatch", "invalid_json":
		clean["jsonError"] = value["jsonError"]
	}
	switch value["eventBoundary"] {
	case "complete", "transport_eof":
		clean["eventBoundary"] = value["eventBoundary"]
	}
	if value["framingViolation"] == "missing_separator_before_terminal_error" {
		clean["framingViolation"] = value["framingViolation"]
	}
	return ProviderErrorDetails("original error", 0, encodedProviderMetadata(clean))
}
