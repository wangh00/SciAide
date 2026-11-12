package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestCapturedIdenticalQuoteRepairsRejectedWithSourceMenu(t *testing.T) {
	dir := os.Getenv("SCIAIDE_QUOTE_SELECTION_REPLAY_DIR")
	if dir == "" {
		t.Skip("no frozen repeated quotation capture supplied")
	}
	read := func(name string, dst any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || json.Unmarshal(data, dst) != nil {
			t.Fatal("invalid quotation capture", name, err)
		}
	}
	var turns []struct {
		Text string `json:"draft_text"`
	}
	read("model-turns-original.json", &turns)
	var executions []struct {
		Attempt int    `json:"attempt"`
		Schema  string `json:"output_schema_json"`
	}
	read("executions-original.json", &executions)
	var schema json.RawMessage
	for _, execution := range executions {
		if execution.Attempt == 11 {
			schema = json.RawMessage(execution.Schema)
		}
	}
	var chat struct {
		Details string `json:"error_details"`
	}
	read("latest-chat-original.json", &chat)
	at := strings.Index(chat.Details, "{")
	var issue workflow.CitationQuoteError
	if at < 0 || json.NewDecoder(strings.NewReader(chat.Details[at:])).Decode(&issue) != nil {
		t.Fatal("missing real quote diagnostic")
	}
	r := newCitationRepair(turns[0].Text, schema, &issue)
	if r == nil || len(r.sources) != 1 || r.quoteLimit != 700 {
		t.Fatal("real failed candidate cannot use bounded source selection")
	}
	baseBefore := string(r.base)
	for i, turn := range turns[1:] {
		var patch map[string]any
		if json.Unmarshal([]byte(turn.Text), &patch) != nil {
			t.Fatal("invalid real patch")
		}
		// The prior implementation regenerated hashes after a no-op; replay the
		// identical payload against the current fixed base without changing it.
		patch["baseSHA256"] = r.hash
		candidate, _ := json.Marshal(patch)
		if _, err := r.merge(string(candidate)); err == nil || !strings.Contains(err.Error(), "unchanged rejectedSupportingQuote") {
			t.Fatal("real no-op quotation repair accepted", i+2, err)
		}
	}
	segments := r.sources[0].Segments
	last := segments[len(segments)-1]
	merged, err := r.merge(sourceSegmentPatch(r, issue.Reference, last.ID))
	if err != nil || !strings.Contains(merged, "6-month assessment") || strings.Contains(merged, "sourceSegmentIds") || string(r.base) != baseBefore {
		t.Fatal("exact model-selected segment changed base or failed merge", err)
	}
	if !strings.Contains(r.instruction(), "700-character") || !strings.Contains(r.instruction(), "reason/assessment") || !strings.Contains(r.instruction(), last.ID) {
		t.Fatal("real correction hides scientific/full-result fallback")
	}
	t.Log("two original no-op patch payloads rejected against current base; exact segment merge is protocol-only, not scientific acceptance")
}
