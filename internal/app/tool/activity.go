package tool

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// ActivityArgumentProjectionBytes bounds the JSON sent to activity UIs. The
// complete invocation remains in the local audit record and is never needed
// to render an expandable execution card.
const ActivityArgumentProjectionBytes = 16 * 1024

// ResourceActivityLabel is shared by live and historical activity projections.
// Structured metadata is display-only and must never become an instruction.
func ResourceActivityLabel(call Call) string {
	if call.Result == nil || (call.ToolName != "builtin.resource.open" && call.ToolName != "builtin.resource.search") {
		return ""
	}
	var result struct {
		Label string `json:"label"`
	}
	if json.Unmarshal(call.Result.Structured, &result) != nil {
		return ""
	}
	return SafeActivityText(strings.TrimSpace(result.Label), 180)
}

const (
	activityMaxDepth         = 8
	activityMaxObjectItems   = 64
	activityMaxArrayItems    = 32
	activityMaxStringRunes   = 1200
	activityMaxPermission    = 32
	activityMaxResourceRunes = 512
)

var (
	activityBearerPattern       = regexp.MustCompile(`(?i)(\bBearer\s+)[A-Za-z0-9._~+/=-]+`)
	activityHeaderSecretPattern = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization|x-api-key|api-key|token|secret|password)\s*[:=]\s*)[^\s,;]+`)
	activityQuerySecretPattern  = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password)=)[^&#\s]+`)
)

// RedactActivityText removes credentials that may have been embedded in a
// human-readable summary, error, command output, or permission resource. The
// complete value remains in the private audit record; callers should use this
// helper for every value crossing the activity/UI boundary.
func RedactActivityText(value string) string {
	return redactActivityString(value)
}

// SafeActivityText applies credential redaction and a rune bound without
// collapsing line breaks. It is intended for activity summaries and process
// output where preserving the original lines makes the live view useful.
func SafeActivityText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	value = redactActivityString(value)
	if limit <= 0 {
		limit = activityMaxStringRunes
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…（已截断）"
	}
	return value
}

// SafeActivityArguments returns a deterministic, bounded and redacted view of
// tool arguments suitable for a user-facing activity card. It deliberately
// does not attempt to preserve the exact invocation: model prompts, secrets
// and large data payloads belong to the private audit trail, while paths,
// queries and execution metadata remain visible for practical inspection.
func SafeActivityArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	projected := projectActivityValue(value, "", 0)
	encoded, err := json.Marshal(projected)
	if err != nil || len(encoded) > ActivityArgumentProjectionBytes {
		// A second pass with a deliberately small scalar leaves a valid JSON
		// object even when an unusual nested value exceeds the normal budget.
		projected = map[string]any{"details": "参数过大，已省略"}
		encoded, _ = json.Marshal(projected)
	}
	return encoded
}

// SafeActivityPermissions keeps the frozen permission contract visible while
// preventing an accidentally descriptive resource string from becoming an
// unbounded or secret-bearing UI payload.
func SafeActivityPermissions(values []PermissionRequirement) []PermissionRequirement {
	if len(values) == 0 {
		return []PermissionRequirement{}
	}
	limit := len(values)
	if limit > activityMaxPermission {
		limit = activityMaxPermission
	}
	result := make([]PermissionRequirement, 0, limit)
	for index, value := range values {
		if index >= limit {
			break
		}
		value.Resource = boundedActivityString(redactActivityString(value.Resource), activityMaxResourceRunes)
		result = append(result, value)
	}
	return result
}

func projectActivityValue(value any, key string, depth int) any {
	if depth >= activityMaxDepth {
		return "…（嵌套内容已省略）"
	}
	if isSensitiveActivityKey(key) {
		return "[已隐藏]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, minActivityInt(len(typed), activityMaxObjectItems))
		count := 0
		for childKey, child := range typed {
			if count >= activityMaxObjectItems {
				result["…（其余参数已省略）"] = true
				break
			}
			result[childKey] = projectActivityValue(child, childKey, depth+1)
			count++
		}
		return result
	case []any:
		result := make([]any, 0, minActivityInt(len(typed), activityMaxArrayItems))
		for index, child := range typed {
			if index >= activityMaxArrayItems {
				result = append(result, "…（其余项目已省略）")
				break
			}
			result = append(result, projectActivityValue(child, key, depth+1))
		}
		return result
	case string:
		return boundedActivityString(redactActivityString(typed), activityMaxStringRunes)
	default:
		return value
	}
}

func boundedActivityString(value string, maximum ...int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.TrimSpace(value)
	limit := activityMaxStringRunes
	if len(maximum) > 0 && maximum[0] > 0 {
		limit = maximum[0]
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…（已截断）"
	}
	return value
}

func redactActivityString(value string) string {
	value = activityBearerPattern.ReplaceAllString(value, `${1}[已隐藏]`)
	value = activityHeaderSecretPattern.ReplaceAllString(value, `${1}[已隐藏]`)
	return activityQuerySecretPattern.ReplaceAllString(value, `${1}[已隐藏]`)
}

func isSensitiveActivityKey(key string) bool {
	key = normalizeActivityKey(key)
	if key == "" {
		return false
	}
	for _, marker := range []string{
		"apikey", "secret", "token", "password", "passwd", "credential",
		"authorization", "cookie", "privatekey", "clientsecret", "accesskey",
	} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func normalizeActivityKey(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func minActivityInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
