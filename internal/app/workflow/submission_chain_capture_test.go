package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestCapturedSubmissionChainValidatesOriginalSourceSelection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_SUBMISSION_CHAIN_REPLAY_DIR")
	if dir == "" {
		t.Skip("no task-scoped submission capture")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var rows []struct {
		Input string `json:"input_json"`
	}
	var input struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	var runs []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(read("workflow-step.json"), &rows) != nil || len(rows) != 1 || json.Unmarshal([]byte(rows[0].Input), &input) != nil || json.Unmarshal(read("run.json"), &runs) != nil || len(runs) != 1 {
		t.Fatal("invalid captured frozen input")
	}
	modelInput := rows[0].Input
	for _, c := range input.Candidates {
		if c.Reference != "" && c.IndexVersionID != "" && c.ChunkID != "" && c.QuoteSHA256 != "" {
			modelInput = strings.ReplaceAll(modelInput, c.Reference, citation.KnowledgeReference(runs[0].ID, c.IndexVersionID, c.ChunkID, c.QuoteSHA256))
		}
	}
	schema := json.RawMessage(read("schema.json"))
	for _, name := range []string{"turn-1.txt", "turn-2.txt"} {
		candidate, _, err := NormalizeAIStageSubmission(string(read(name)), schema)
		if err != nil {
			t.Fatal(err)
		}
		err = validateSelectedEvidence(canonicalSupportingQuotes(candidate, raw(modelInput)), raw(modelInput))
		var quote *CitationQuoteError
		if !errors.As(err, &quote) || quote.Reference != "[K-1E5F8868F874]" {
			t.Fatal("real failure did not reproduce", name, err)
		}
	}
	merged := read("merged-selection.json")
	if _, _, err := NormalizeAIStageSubmission(string(merged), schema); err != nil {
		t.Fatal("agent selection no longer matches the frozen schema", err)
	}
	if err := validateSelectedEvidence(canonicalSupportingQuotes(merged, raw(modelInput)), raw(modelInput)); err != nil {
		t.Fatal("agent source selection fails complete evidence business validation", err)
	}
	t.Log("Both real candidates remain rejected; exact original source selection passes the same schema and evidence checks. No live model or task was run.")
}
