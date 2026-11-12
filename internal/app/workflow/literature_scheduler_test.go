package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

// Current retrieval no longer schedules additional pages. Historical frozen
// checkpoints must still disclose incomplete coverage after restart.
func TestLegacyLiteraturePageCoverageSurvivesRestart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      literatureCheckpoint
		incomplete bool
	}{
		{"pending_ledger", literatureCheckpoint{PageLedger: map[string]int{"pubmed\ntrial": 20}}, true},
		{"pending_page", literatureCheckpoint{Pages: []literaturePage{{Query: "trial", Source: "pubmed", Offset: 20}}}, true},
		{"completed", literatureCheckpoint{PageLedger: map[string]int{"pubmed\ntrial": -1}}, false},
		{"current_first_page", literatureCheckpoint{SourceFailures: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.state)
			if err != nil {
				t.Fatal(err)
			}
			var restored literatureCheckpoint
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			if got := literaturePagesIncomplete(restored); got != tc.incomplete {
				t.Fatal(got, tc.incomplete)
			}
			if message := literatureBudgetMessage(restored); !strings.Contains(message, "未穷尽数据库") {
				t.Fatal(message)
			}
		})
	}
}
