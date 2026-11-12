package tool

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const contextPreviewMarker = "\n...[tool result truncated for model context]...\n"
const MinimumContextPreviewRunes = len(`{"contentNotShown":true}`)

// FitModelContextPreview projects a TOOL envelope, not its durable result.
// Clipping encoded JSON inserts literal newlines into string tokens and can
// silently destroy pagination metadata. A preview instead remains JSON and
// explicitly withholds structured evidence/continuation offsets. It never
// invents an offset or action; an offered current action may be reread.
func FitModelContextPreview(value string, limit int) (string, bool) {
	var envelope map[string]json.RawMessage
	err := json.Unmarshal([]byte(value), &envelope)
	legacyBroken := err != nil && strings.HasPrefix(strings.TrimSpace(value), "{") && strings.Contains(value, contextPreviewMarker)
	ownPreview := err == nil && (string(envelope["contextPreviewTruncated"]) == "true" || string(envelope["contentNotShown"]) == "true")
	if !legacyBroken && !ownPreview && (err != nil || envelope["status"] == nil || (envelope["text"] == nil && envelope["structured"] == nil)) {
		return "", false
	}
	if !legacyBroken && utf8.RuneCountInString(value) <= limit {
		return value, true
	}
	const hiddenMarker = `{"contentNotShown":true}`
	if limit < MinimumContextPreviewRunes {
		return "", true
	}
	metadata := map[string]any{"contextPreviewTruncated": true, "contentNotShown": true, "truncated": true}
	if !legacyBroken {
		for _, key := range []string{"status", "errorCode", "toolCallId"} {
			if v := envelope[key]; v != nil {
				metadata[key] = v
			}
		}
		if ownPreview && envelope["rereadActionId"] != nil {
			metadata["rereadActionId"] = envelope["rereadActionId"]
		}
		if envelope["structured"] != nil {
			metadata["structuredNotShown"] = true
		}
		var fields map[string]json.RawMessage
		// Retain a small COMPLETE structured result when possible, but never a
		// paging envelope whose continuation would skip omitted visible text.
		paging := false
		_ = json.Unmarshal(envelope["structured"], &fields)
		for _, key := range []string{"actions", "nextOffset", "actionId", "content", "bytesRead", "originalBytes", "offset", "truncated", "hasMore", "characters", "totalUnits", "total"} {
			paging = paging || fields[key] != nil
		}
		if len(fields) > 0 && !paging && len(envelope["structured"]) <= min(1024, limit/4) {
			metadata["structured"] = envelope["structured"]
			delete(metadata, "structuredNotShown")
		}
		var structured struct {
			Kind     string `json:"kind"`
			ActionID string `json:"actionId"`
		}
		if json.Unmarshal(envelope["structured"], &structured) == nil && structured.Kind == "workspace_read" && structured.ActionID != "" {
			metadata["rereadActionId"] = structured.ActionID
		}
	}
	runes := []rune(value)
	encode := func(n int) string {
		head := (n + 1) / 2
		tail := n - head
		metadata["text"] = string(runes[:head]) + contextPreviewMarker + string(runes[len(runes)-tail:])
		out, _ := json.Marshal(metadata)
		return string(out)
	}
	if utf8.RuneCountInString(encode(0)) > limit {
		minimal := `{"contextPreviewTruncated":true,"contentNotShown":true}`
		if len(minimal) <= limit {
			return minimal, true
		}
		return hiddenMarker, true
	}
	low, high := 0, min(len(runes), limit)+1
	for high-low > 1 {
		mid := (low + high) / 2
		if utf8.RuneCountInString(encode(mid)) <= limit {
			low = mid
		} else {
			high = mid
		}
	}
	return encode(low), true
}

// A current exact workspace page has an executable continuation registered by
// the resource service. Prefer it over archived previews when the budget fits.
// This identifies a representation, not permission or scientific correctness.
func IsWorkspaceReadContext(value string) bool {
	var envelope struct {
		Structured struct {
			Kind     string `json:"kind"`
			ActionID string `json:"actionId"`
			Text     string `json:"text"`
		} `json:"structured"`
	}
	return json.Unmarshal([]byte(value), &envelope) == nil && envelope.Structured.Kind == "workspace_read" && envelope.Structured.ActionID != ""
}
