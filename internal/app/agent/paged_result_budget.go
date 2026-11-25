package agent

import (
	"encoding/json"
	"unicode/utf8"
)

// Keep a task page valid even when a later request has less preview space.
// nextOffset describes only the visible prefix, never an omitted middle.
// Both frozen stage-input pages and saved discovery field pages are the same
// paged-content contract: the latest complete page must stay readable.
func fitTaskPagePreview(text string, limit int) (string, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &envelope) != nil {
		return "", false
	}
	raw := envelope["structured"]
	var page struct {
		Section string `json:"section"`
		Offset  int    `json:"offset"`
		Next    int    `json:"nextOffset"`
		Content string `json:"content"`
		SHA     string `json:"sha256"`
		Mode    string `json:"addressMode"`
		Status  string `json:"status"`
		Field   string `json:"field"`
		Total   int    `json:"totalRunes"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return "", false
	}
	runes := []rune(page.Content)
	stageInput := page.Section == "stage_input" && len(page.SHA) == 64 && page.Next > page.Offset && len(runes) == page.Next-page.Offset
	// A discovery field page ends with nextOffset -1; a cut preview must
	// resume inside the original total, never past it.
	discoveryField := page.Status == "unverified_discovery_field" && page.Field == "abstract" && len(page.SHA) == 64 && page.Offset >= 0 && page.Total >= page.Offset+len(runes) && len(runes) >= 1 && (page.Next == -1 && page.Offset+len(runes) == page.Total || page.Next > page.Offset && len(runes) == page.Next-page.Offset)
	if !stageInput && !discoveryField {
		return "", false
	}
	var original map[string]json.RawMessage
	_ = json.Unmarshal(raw, &original)
	encode := func(n int) string {
		next := make(map[string]json.RawMessage, len(original))
		for k, v := range original {
			next[k] = v
		}
		next["content"], _ = json.Marshal(string(runes[:n]))
		next["nextOffset"], _ = json.Marshal(page.Offset + n)
		next["hasMore"] = json.RawMessage(`true`)
		next["contextPreviewTruncated"] = json.RawMessage(`true`)
		wrapper := make(map[string]json.RawMessage, len(envelope))
		for k, v := range envelope {
			wrapper[k] = v
		}
		wrapper["text"], _ = json.Marshal("Partial page preview; continue at visible nextOffset, not the saved page end.")
		wrapper["structured"], _ = json.Marshal(next)
		wrapper["truncated"] = json.RawMessage(`true`)
		value, _ := json.Marshal(wrapper)
		return string(value)
	}
	minimal := encode(1)
	if utf8.RuneCountInString(minimal) > limit {
		// There is not enough space for content. Do not expose a misleading
		// nextOffset from the original full result.
		hidden := map[string]json.RawMessage{}
		for key, value := range map[string]any{"contentNotShown": true, "section": page.Section, "retryOffset": page.Offset, "sha256": page.SHA} {
			hidden[key], _ = json.Marshal(value)
		}
		if discoveryField {
			for _, key := range []string{"status", "field", "queryId", "candidateId"} {
				if value, ok := original[key]; ok {
					hidden[key] = value
				}
			}
			delete(hidden, "section")
		}
		if page.Mode == "json_pointer" {
			for _, key := range []string{"addressMode", "pointer", "encoding", "valueSha256"} {
				if value, ok := original[key]; ok {
					hidden[key] = value
				}
			}
		}
		value, _ := json.Marshal(hidden)
		if utf8.RuneCount(value) <= limit {
			return string(value), true
		}
		tinyValue := map[string]any{"contentNotShown": true, "retryOffset": page.Offset}
		if page.Mode == "json_pointer" {
			delete(tinyValue, "retryOffset")
		}
		tiny, _ := json.Marshal(tinyValue)
		if len(tiny) <= limit {
			return string(tiny), true
		}
		return "", true
	}
	low, high := 1, len(runes)
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
