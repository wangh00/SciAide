package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/modelutil"
)

// A captured relay fault appended a complete error data field before dispatching
// a preceding output field. This is NOT a general JSON repair or a success path.
// The decoder must first prove an explicit channel transition to error within a
// complete dispatch. Only two independently valid, single-line objects, a known
// nonterminal prefix matching its actual channel and a flat error are recognized.
// Neither field is emitted as a model event, provider item or tool call.
func coalescedTerminalFault(prefix, priorType, suffix string) (eventPayload, []byte, bool) {
	var failure eventPayload
	if strings.ContainsAny(prefix, "\n\r") || strings.ContainsAny(suffix, "\n\r") {
		return failure, nil, false
	}
	first, ok := uniqueObjectFields([]byte(prefix))
	if !ok {
		return failure, nil, false
	}
	var previous eventPayload
	if json.Unmarshal([]byte(prefix), &previous) != nil || previous.Type != priorType {
		return failure, nil, false
	}
	itemPrefix := false
	switch previous.Type {
	case "response.output_text.delta", "response.function_call_arguments.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning.delta":
		if previous.Delta == "" {
			return failure, nil, false
		}
	case "response.output_item.done":
		// The other observed prefix was a completed reasoning item, not a
		// complete response. Never salvage message/tool items or infer missing
		// identity from mutable accumulators in this failure-only path.
		if !faultReasoningItem(previous.Item) {
			return failure, nil, false
		}
		itemPrefix = true
	default:
		return failure, nil, false
	}
	for key, value := range first {
		if itemPrefix && key != "type" && key != "sequence_number" && key != "output_index" && key != "item" {
			return failure, nil, false
		}
		switch key {
		case "type", "delta", "item_id":
			if !faultString(value) {
				return failure, nil, false
			}
		case "sequence_number", "output_index", "content_index", "summary_index":
			if !faultIndex(value) {
				return failure, nil, false
			}
		case "item":
			if !itemPrefix {
				return failure, nil, false
			}
		default:
			return failure, nil, false
		}
	}
	last, ok := uniqueObjectFields([]byte(suffix))
	if !ok || json.Unmarshal([]byte(suffix), &failure) != nil || failure.Type != "error" || failure.Code == "" || failure.Message == "" {
		return eventPayload{}, nil, false
	}
	for key, value := range last {
		switch key {
		case "type", "code", "message":
			if !faultString(value) {
				return eventPayload{}, nil, false
			}
		case "sequence_number":
			if !faultIndex(value) {
				return eventPayload{}, nil, false
			}
		case "param":
			if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !faultString(value) {
				return eventPayload{}, nil, false
			}
		default:
			return eventPayload{}, nil, false
		}
	}
	return failure, []byte(suffix), true
}

func faultReasoningItem(raw json.RawMessage) bool {
	fields, ok := uniqueObjectFields(raw)
	if !ok {
		return false
	}
	item, err := decodeResponseItem(raw)
	if err != nil || item.Type != "reasoning" || item.ID == "" || (len(item.Summary) == 0 && len(item.Content) == 0 && item.EncryptedContent == nil) {
		return false
	}
	for key, value := range fields {
		switch key {
		case "id", "type":
			if !faultString(value) {
				return false
			}
		case "status":
			var status string
			if !faultString(value) || json.Unmarshal(value, &status) != nil || status != "completed" {
				return false
			}
		case "encrypted_content":
			if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !faultString(value) {
				return false
			}
		case "summary", "content":
			value = bytes.TrimSpace(value)
			if len(value) == 0 || value[0] != '[' {
				return false
			}
			var blocks []json.RawMessage
			if json.Unmarshal(value, &blocks) != nil {
				return false
			}
			for _, rawBlock := range blocks {
				block, ok := uniqueObjectFields(rawBlock)
				if !ok || len(block) != 2 || !faultString(block["type"]) || !faultString(block["text"]) {
					return false
				}
				var kind string
				if json.Unmarshal(block["type"], &kind) != nil || (key == "summary" && kind != "summary_text") || (key == "content" && kind != "reasoning_text") {
					return false
				}
			}
		default:
			return false // includes error/refusal and case aliases of known keys
		}
	}
	return true
}

func faultString(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) >= 2 && raw[0] == '"' // json.Valid was checked; null is not a string
}

func faultIndex(raw []byte) bool {
	var index int64
	return !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && json.Unmarshal(raw, &index) == nil && index >= 0
}

func classifyCoalescedTerminalFault(failure eventPayload, details string) error {
	err := modelutil.ClassifyStreamError(failure.Type, failure.Code, failure.Message, details)
	if modelutil.IsRetryable(err) {
		// This malformed-envelope branch is deliberately narrower than the
		// ordinary valid-error adapter: arbitrary code substrings/messages cannot
		// newly authorize a retry. Keep permanent/policy classifications above.
		switch failure.Code {
		case "stream_read_error", "upstream_stream_read_error", "upstream_http2_stream_error":
		default:
			return modelutil.ErrorWithDetails("MODEL_REQUEST_REJECTED", "模型服务返回了无法安全自动恢复的错误，已保留结果并停止；请查看诊断。", details, false, nil)
		}
	}
	return err
}

func uniqueObjectFields(raw []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, false
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, exists := fields[key]; exists {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	return fields, true
}
