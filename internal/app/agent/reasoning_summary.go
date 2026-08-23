package agent

import (
	"encoding/json"
	"strings"

	"github.com/wangh00/SciAide/internal/model"
)

const maxVisibleReasoningSummaryRunes = 1_200

// visibleReasoningSummary only accepts provider-designated summary_text items.
// Raw Anthropic thinking and encrypted reasoning state remain model-private.
func visibleReasoningSummary(item model.ProviderItem) string {
	if item.Type != "reasoning" || len(item.Payload) == 0 {
		return ""
	}
	var payload struct {
		Summary []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return ""
	}
	parts := make([]string, 0, min(3, len(payload.Summary)))
	for _, value := range payload.Summary {
		text := strings.TrimSpace(value.Text)
		if value.Type != "summary_text" || text == "" {
			continue
		}
		parts = append(parts, text)
		if len(parts) == 3 {
			break
		}
	}
	result := strings.Join(parts, "\n")
	runes := []rune(result)
	if len(runes) > maxVisibleReasoningSummaryRunes {
		result = strings.TrimSpace(string(runes[:maxVisibleReasoningSummaryRunes])) + "..."
	}
	return result
}
