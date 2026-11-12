package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Replay an opt-in, read-only capture in memory. No live task is retried and no
// provider is called; valid fixture dispositions test the scheduling contract,
// not the correctness of scientific claims in the captured model responses.
func TestCapturedSelectedEvidenceBatchProgressConverges(t *testing.T) {
	dir := os.Getenv("SCIAIDE_SELECTED_EVIDENCE_REPLAY_DIR")
	if dir == "" {
		t.Skip("no frozen literature batch supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "workflow-detail-original.json"))
	if err != nil {
		t.Fatal(err)
	}
	var detail RunDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatal(err)
	}
	step := findNodeStep(detail.Steps, "evidence_screening")
	if step == nil || step.Status != StepFailed {
		t.Fatal("capture does not contain failed evidence screening")
	}
	var oldInput struct {
		Info      selectedEvidenceInput `json:"_selectedEvidence"`
		Documents []string              `json:"documentIds"`
	}
	_ = json.Unmarshal(step.Input, &oldInput)
	var latest AIExecution
	for _, e := range detail.AIExecutions {
		if e.WorkflowStepID == step.ID && e.Attempt == step.Attempt {
			latest = e
		}
	}
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	if len(oldInput.Documents) <= len(oldInput.Info.Documents) || latest.Status != "failed" {
		t.Fatal("capture does not reproduce conflicting inventories")
	}
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(latest.OutputText, selectedEvidencePhaseNode(node, step.Input), step.Input)
	if err != nil {
		t.Fatal("capture failed before the document scope check", err)
	}
	seeds, seedErr := workflowCandidateCitationSeeds(detail.Run.Compilation, detail.Steps, *step, indexedEvidenceSnapshots(detail))
	if seedErr != nil {
		t.Fatal("capture source resolution failed", seedErr)
	}
	value = restoreWorkflowCitationMarkers(value, seeds, latest.ChatRunID)
	if err := validateSelectedEvidence(value, step.Input); err == nil || !strings.Contains(err.Error(), "outside the current analysis batch") {
		t.Fatal("captured rejected response was accepted or failed elsewhere", err)
	}
	oldCount := len(oldInput.Documents)
	seen, batchCount := map[string]bool{}, 0
	for cycle := 0; cycle < 10; cycle++ {
		step = findNodeStep(detail.Steps, "evidence_screening")
		step.Status, step.Input, step.InputSHA256 = StepQueued, raw(`{}`), ""
		base, err := bindNodeInput(detail, node)
		if err != nil {
			t.Fatal(err)
		}
		input, err := prepareSelectedEvidence(detail, bindIndexedEvidence(detail, node, base))
		if err != nil {
			t.Fatal(err)
		}
		var in struct {
			Key       string                `json:"sourceKey"`
			Info      selectedEvidenceInput `json:"_selectedEvidence"`
			Documents []string              `json:"documentIds"`
		}
		_ = json.Unmarshal(input, &in)
		if in.Info.Phase == "coverage" && !in.Info.Direct {
			if len(in.Info.Analyses) != oldCount || len(seen) != oldCount || len(in.Documents) != oldCount {
				t.Fatal("coverage lost selected documents", len(in.Info.Analyses), len(seen), oldCount)
			}
			t.Logf("capture reproduced %d selected / %d batch conflict; new schedule converged in %d non-overlapping batches, with all %d dispositions restored after reload", oldCount, len(oldInput.Info.Documents), batchCount, oldCount)
			return
		}
		if in.Info.Phase != "batch" || !rawJSONEqual(mustJSON(in.Info.Documents), mustJSON(in.Documents)) {
			t.Fatal("batch inventory still conflicts")
		}
		analyses := []map[string]any{}
		for _, id := range in.Info.Documents {
			if seen[id] {
				t.Fatal("accepted document scheduled twice", id)
			}
			seen[id] = true
			analyses = append(analyses, map[string]any{"documentId": id, "finding": "Replay fixture: no result claim", "limitations": "Fixture only, not a scientific evaluation", "references": []string{}})
		}
		output := mustJSON(map[string]any{"documentAnalyses": analyses})
		projected := selectedEvidencePhaseNode(node, input)
		if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(output), projected, input); err != nil {
			t.Fatal(err)
		}
		if err := validateWorkflowAIStageOutput(projected, output, input); err != nil {
			t.Fatal(err)
		}
		id := "replay-batch-" + string(rune('A'+cycle))
		detail.AIExecutions = append(detail.AIExecutions, AIExecution{ID: id, WorkflowStepID: step.ID, Status: "completed", Output: output, OutputSHA256: hashJSON(output)})
		var refs []tool.CitationRef
		_ = json.Unmarshal(mustJSON(decodeObject(input)["candidates"]), &refs)
		// Simulate a reread of a source already in the bounded window. It changes
		// window ordering, not the frozen materials or accepted scientific text.
		var evidence any
		if len(refs) > 0 {
			evidence = indexedEvidencePage{Key: localEvidenceMaterialKey(detail), Citations: refs[:1]}
		}
		detail.Events = append(detail.Events, RuntimeEvent{Type: "workflow.selected_evidence_batch", Payload: mustJSON(map[string]any{"executionId": id, "sourceKey": in.Key, "indexedEvidence": evidence})})
		batchCount++
		if err := json.Unmarshal(mustJSON(detail), &detail); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("batch progress failed to converge")
}
