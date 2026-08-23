package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/model"
)

func TestVisibleReasoningSummaryAcceptsOnlyResponsesSummaryText(t *testing.T) {
	item := model.ProviderItem{Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","summary":[{"type":"summary_text","text":"  Checked evidence.  "},{"type":"other","text":"hidden"},{"type":"summary_text","text":"Compared alternatives."}],"encrypted_content":"opaque"}`)}
	if got := visibleReasoningSummary(item); got != "Checked evidence.\nCompared alternatives." {
		t.Fatalf("visibleReasoningSummary() = %q", got)
	}
	thinking := model.ProviderItem{Type: "thinking", Payload: json.RawMessage(`{"type":"thinking","thinking":"private chain","signature":"signed"}`)}
	if got := visibleReasoningSummary(thinking); got != "" {
		t.Fatalf("Anthropic thinking was exposed: %q", got)
	}
}

func TestVisibleReasoningSummaryIsBounded(t *testing.T) {
	value := strings.Repeat("研", maxVisibleReasoningSummaryRunes+50)
	payload, err := json.Marshal(map[string]any{"summary": []map[string]string{{"type": "summary_text", "text": value}}})
	if err != nil {
		t.Fatal(err)
	}
	got := visibleReasoningSummary(model.ProviderItem{Type: "reasoning", Payload: payload})
	if len([]rune(got)) > maxVisibleReasoningSummaryRunes+3 || !strings.HasSuffix(got, "...") {
		t.Fatalf("bounded summary length = %d", len([]rune(got)))
	}
}
