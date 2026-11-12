package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestSelectedEvidenceCoverageAdmitsWholeSnapshotBeforeModelBudget(t *testing.T) {
	rows := []any{}
	for i := 0; i < 33; i++ {
		rows = append(rows, map[string]any{"documentId": fmt.Sprint(i), "finding": strings.Repeat("完整中文分析😀", 80), "limitations": "only original excerpts", "references": []string{}, "workProduct": "short"})
	}
	args := map[string]any{
		"_evidenceWorkPaging": evidenceWorkPagingVersion,
		"originalRequest":     "unchanged user goal",
		"researchContract":    map[string]any{"successCriteria": []string{"unchanged criterion"}},
		"_selectedEvidence":   map[string]any{"phase": "coverage", "documents": []string{"kept"}, "analyses": rows},
		"candidates":          []any{map[string]any{"reference": "[K-original]", "quote": strings.Repeat("原始摘录😀\n\\\"", 6500)}},
		"_reviewRevision":     map[string]any{"priorOutput": strings.Repeat("prior report retained\n", 4000)},
	}
	original := mustJSON(args)
	view, _ := evidenceWorkModelProjection(original)
	if len(view) <= 220*1024 || !rawJSONEqual(view, original) {
		t.Fatal("fixture must exceed the old cap without long workProduct", len(view))
	}
	full, err := boundedSelectedEvidenceInput(args)
	if err != nil || !rawJSONEqual(full, original) {
		t.Fatal("readable snapshot blocked or changed before model budgeting", err)
	}
	assertCoverageSnapshotPagedAndReadable(t, full)
	// No existing reader capability: do not invent a new one to bypass admission.
	delete(args, "_evidenceWorkPaging")
	if _, err := boundedSelectedEvidenceInput(args); err == nil {
		t.Fatal("oversized input accepted without authorized reader")
	}
	args["_evidenceWorkPaging"] = evidenceWorkPagingVersion
	args["candidates"] = strings.Repeat("😀", tool.MaxWorkflowSnapshotRunes)
	if _, err := boundedSelectedEvidenceInput(args); err == nil || !strings.Contains(err.Error(), "snapshot capacity") {
		t.Fatal("persisted character limit was bypassed", err)
	}
}

func TestSelectedEvidenceCoverageByteCountIsNotModelTokenCount(t *testing.T) {
	full, err := boundedSelectedEvidenceInput(map[string]any{"_evidenceWorkPaging": evidenceWorkPagingVersion, "context": strings.Repeat("中", 80_000)})
	if err != nil || len(full) <= 220*1024 || utf8.RuneCount(full) >= 220*1024 {
		t.Fatal("Unicode bytes still mistaken for model admission", err)
	}
}

// Uses the production read-only task reader to recover ALL projected values;
// neither an unread navigation link nor a truncated quote counts as evidence.
func assertCoverageSnapshotPagedAndReadable(t *testing.T, full json.RawMessage) {
	t.Helper()
	prompt := "host\n<stage_input>\n" + string(full) + "\n</stage_input>"
	view, changed := ProjectResearchStagePrompt(prompt, full, 30_000)
	if !changed || utf8.RuneCountInString(view) > 30_000 || !strings.Contains(view, "<stage_input_pages") {
		t.Fatal("complete coverage cannot reach generic paging", utf8.RuneCountInString(view))
	}
	start := strings.LastIndex(view, "<stage_input>\n") + len("<stage_input>\n")
	end := strings.LastIndex(view, "\n</stage_input>")
	var projected any
	if err := json.Unmarshal([]byte(view[start:end]), &projected); err != nil {
		t.Fatal(err)
	}
	svc, repo := stageMemoryService()
	repo.detail.Steps[0].Input = full
	links := 0
	var read func(any)
	read = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if link, ok := v["$read"].(map[string]any); ok {
				links++
				offset, end := int(link["offset"].(float64)), int(link["endOffset"].(float64))
				first := offset
				var recovered strings.Builder
				for offset < end {
					page, err := svc.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_input", Offset: offset})
					if err != nil {
						t.Fatal(err)
					}
					var p struct {
						Content, SHA256 string
						NextOffset      int
					}
					if json.Unmarshal(page, &p) != nil || p.SHA256 != hashJSON(full) || p.SHA256 != link["sha256"] || p.NextOffset <= offset {
						t.Fatal("page lost snapshot identity or forward progress")
					}
					runes := []rune(p.Content)
					recovered.WriteString(string(runes[:min(len(runes), end-offset)]))
					offset = p.NextOffset
				}
				if recovered.String() != string([]rune(string(full))[first:end]) || !json.Valid([]byte(recovered.String())) {
					t.Fatal("paged original value changed or truncated")
				}
				return
			}
			for _, child := range v {
				read(child)
			}
		case []any:
			for _, child := range v {
				read(child)
			}
		}
	}
	read(projected)
	if links == 0 {
		t.Fatal("fixture produced no real paging links")
	}
}

// Opt-in immutable capture only. Does not open the formal database, retry a
// user's run, invoke a provider, or claim a scientific verdict from a fixture.
func TestCapturedEvidenceCoverageBudgetPreservesAcceptedBatches(t *testing.T) {
	dir := os.Getenv("SCIAIDE_COVERAGE_BUDGET_REPLAY_DIR")
	if dir == "" {
		t.Skip("no isolated coverage snapshot supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "workflow-detail-original.json"))
	if err != nil {
		t.Fatal(err)
	}
	var detail RunDetail
	if err = json.Unmarshal(data, &detail); err != nil {
		t.Fatal(err)
	}
	step := findNodeStep(detail.Steps, "evidence_screening")
	if step == nil || step.Status != StepFailed || step.ErrorCode != "WORKFLOW_LITERATURE_PREPARATION_FAILED" {
		t.Fatal("capture is not a local preparation failure")
	}
	// Reproduce the queued preparation branch, including its pending revision
	// checklist. Preparing a failed step directly would omit that checklist.
	step.Status = StepQueued
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	base, err := bindNodeInput(detail, node)
	if err != nil {
		t.Fatal(err)
	}
	// Audit availability is not part of batch identity; use captured facts
	// rather than querying the mutable source database during offline replay.
	lastBatch, err := os.ReadFile(filepath.Join(dir, "last-batch-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeObject(base)
	fields["researchRecordFacts"] = decodeObject(lastBatch)["researchRecordFacts"]
	base = bindPreviousCitationSelection(detail, node, bindIndexedEvidence(detail, node, mustJSON(fields)))
	before, _ := json.Marshal(detail)
	input, err := prepareSelectedEvidence(detail, base)
	if err != nil {
		t.Fatal("accepted batches blocked before actual model admission", err)
	}
	var in struct {
		Key  string                `json:"sourceKey"`
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(input, &in)
	if in.Info.Phase != "coverage" || in.Info.Direct || len(in.Info.Analyses) != 33 || len(in.Info.Documents) != 33 {
		t.Fatal("accepted work not reused", in.Info.Phase, len(in.Info.Analyses), len(in.Info.Documents))
	}
	view, _ := evidenceWorkModelProjection(input)
	if len(view) <= 220*1024 {
		t.Fatal("capture no longer reproduces old local byte cap", len(view))
	}
	assertCoverageSnapshotPagedAndReadable(t, input)
	after, _ := json.Marshal(detail)
	if string(before) != string(after) {
		t.Fatal("offline preparation modified saved stages or executions")
	}
	if err := os.WriteFile(filepath.Join(dir, "coverage-input.json"), input, 0600); err != nil {
		t.Fatal(err)
	}
	stepCopy := *step
	stepCopy.Input, stepCopy.InputSHA256 = input, hashJSON(input)
	if err := os.WriteFile(filepath.Join(dir, "coverage-prompt.txt"), []byte(buildAIStagePrompt(detail, stepCopy, selectedEvidencePhaseNode(node, input))), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("accepted 33/33 original document analyses reused; complete input=%d bytes, legacy workProduct-only view=%d bytes; all generic pages recovered; no model or formal task modified", len(input), len(view))
}
