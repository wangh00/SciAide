package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func selectedEvidenceProjectionFixture() (RunDetail, json.RawMessage) {
	docs, materials, coverage := []string{}, []map[string]any{}, []map[string]any{}
	indexed, refs := []map[string]any{}, []tool.CitationRef{}
	for i := 0; i < 7; i++ {
		id, attachment := fmt.Sprintf("doc-%d", i), fmt.Sprintf("attachment-%d", i)
		docs = append(docs, id)
		indexed = append(indexed, map[string]any{"id": id, "attachmentId": attachment})
		materials = append(materials, map[string]any{"attachmentId": attachment, "candidateId": "candidate-" + id, "title": id})
		coverage = append(coverage, map[string]any{"documentId": id, "excerptCount": 1})
		if i == 6 { // The inventory must also retain a document without excerpts.
			continue
		}
		ref := tool.CitationRef{DocumentID: id, AttachmentID: attachment, IndexVersionID: "index", ChunkID: id, Quote: strings.Repeat("source", 4000)}
		ref.QuoteSHA256 = citation.QuoteSHA256(ref.Quote)
		ref.Reference = citation.KnowledgeReference("run", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
		refs = append(refs, ref)
	}
	base := mustJSON(map[string]any{"documentIds": docs, "candidates": refs, "documentCoverage": coverage, "importedMaterials": materials, "originalRequest": "Compare the selected studies"})
	detail := RunDetail{Run: Run{ID: "run"}, Steps: []Step{
		{ID: "sync", NodeID: "evidence_sync", Status: StepCompleted, Output: mustJSON(map[string]any{"structured": map[string]any{"documents": indexed, "documentIds": docs}})},
		{ID: "search", NodeID: "evidence_search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": refs})},
		{ID: "screen", NodeID: "evidence_screening", Status: StepQueued},
	}}
	return detail, base
}

func TestSelectedEvidenceBatchProjectsOneConsistentInventory(t *testing.T) {
	detail, base := selectedEvidenceProjectionFixture()
	input, err := prepareSelectedEvidence(detail, base)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Info      selectedEvidenceInput `json:"_selectedEvidence"`
		Documents []string              `json:"documentIds"`
		Coverage  []struct {
			DocumentID string `json:"documentId"`
		} `json:"documentCoverage"`
		Materials []struct {
			AttachmentID string `json:"attachmentId"`
		} `json:"importedMaterials"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.Info.Phase != "batch" || len(in.Info.Documents) >= 7 {
		t.Fatal("fixture did not enter a bounded batch", err)
	}
	if !rawJSONEqual(mustJSON(in.Documents), mustJSON(in.Info.Documents)) || len(in.Coverage) != len(in.Documents) || len(in.Materials) != len(in.Documents) {
		t.Fatalf("model sees conflicting document inventories: batch=%d documents=%d coverage=%d materials=%d", len(in.Info.Documents), len(in.Documents), len(in.Coverage), len(in.Materials))
	}
	for i, id := range in.Documents {
		if in.Coverage[i].DocumentID != id || in.Materials[i].AttachmentID != strings.Replace(id, "doc-", "attachment-", 1) {
			t.Fatal("off-batch material reached the model")
		}
	}
	node := selectedEvidencePhaseNode(CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}, input)
	if !strings.Contains(node.Prompt, "_selectedEvidence.documents") || strings.Contains(node.Prompt, "提供 supplementalQueries") {
		t.Fatal("batch prompt requests fields outside the current contract")
	}
}

func TestSelectedEvidenceBatchKeepsUnexcerptedMaterialAndFiltersAllInventories(t *testing.T) {
	detail, base := selectedEvidenceProjectionFixture()
	args := decodeObject(base)
	args["selectedCandidateIds"] = []string{"candidate-doc-0", "candidate-doc-6"}
	args["indexedReadRanges"] = []map[string]any{{"documentId": "doc-0", "locator": "page 1"}, {"documentId": "doc-6", "locator": "page 2"}}
	args["indexedEvidenceWorkingSet"] = map[string]any{"offeredChunkCount": 6, "omittedChunkCount": 1}
	projected := projectSelectedEvidenceBatch(detail, args, []string{"doc-6"}, nil)
	var actual struct {
		Documents  []string           `json:"documentIds"`
		Candidates []tool.CitationRef `json:"candidates"`
		Selected   []string           `json:"selectedCandidateIds"`
		Materials  []struct {
			AttachmentID string `json:"attachmentId"`
		} `json:"importedMaterials"`
		Coverage []struct {
			ID string `json:"documentId"`
		} `json:"documentCoverage"`
		Ranges []struct {
			ID      string `json:"documentId"`
			Locator string `json:"locator"`
		} `json:"indexedReadRanges"`
		Working struct {
			Count int `json:"offeredChunkCount"`
		} `json:"indexedEvidenceWorkingSet"`
	}
	if err := json.Unmarshal(mustJSON(projected), &actual); err != nil {
		t.Fatal(err)
	}
	if len(actual.Documents) != 1 || actual.Documents[0] != "doc-6" || len(actual.Candidates) != 0 || len(actual.Selected) != 1 || actual.Selected[0] != "candidate-doc-6" || len(actual.Materials) != 1 || actual.Materials[0].AttachmentID != "attachment-6" || len(actual.Coverage) != 1 || actual.Coverage[0].ID != "doc-6" || len(actual.Ranges) != 1 || actual.Ranges[0].ID != "doc-6" || actual.Ranges[0].Locator != "page 2" || actual.Working.Count != 0 {
		t.Fatal("unexcerpted document lost its material or received an off-batch inventory", string(mustJSON(projected)))
	}
	var original struct {
		Materials []json.RawMessage `json:"importedMaterials"`
		Ranges    []json.RawMessage `json:"indexedReadRanges"`
	}
	_ = json.Unmarshal(mustJSON(args), &original)
	if len(original.Materials) != 7 || len(original.Ranges) != 2 {
		t.Fatal("batch projection mutated the retained source inventory")
	}
}

func TestSelectedEvidenceCheckpointSurvivesReadWindowRotation(t *testing.T) {
	detail, base := selectedEvidenceProjectionFixture()
	first, err := prepareSelectedEvidence(detail, base)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Key  string                `json:"sourceKey"`
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(first, &in)
	notes := []map[string]any{}
	for _, id := range in.Info.Documents {
		notes = append(notes, map[string]any{"documentId": id, "finding": "Not used for a result claim", "limitations": "Bounded excerpts", "references": []string{}})
	}
	output := mustJSON(map[string]any{"documentAnalyses": notes})
	detail.AIExecutions = []AIExecution{{ID: "accepted", WorkflowStepID: "screen", Attempt: 1, Status: "completed", Output: output, OutputSHA256: hashJSON(output)}}
	read := tool.CitationRef{DocumentID: in.Info.Documents[0], AttachmentID: "attachment-0", Reference: "[K-NEW]", Quote: "Additional actual source text"}
	detail.Events = []RuntimeEvent{{Type: "workflow.selected_evidence_batch", Payload: mustJSON(map[string]any{"executionId": "accepted", "sourceKey": in.Key, "indexedEvidence": indexedEvidencePage{Key: localEvidenceMaterialKey(detail), Citations: []tool.CitationRef{read}}})}}
	// Reload persisted state before forming the next model working set.
	if err := json.Unmarshal(mustJSON(detail), &detail); err != nil {
		t.Fatal(err)
	}
	rotated := bindIndexedEvidence(detail, CompiledNode{ID: "evidence_screening"}, base)
	next, err := prepareSelectedEvidence(detail, rotated)
	if err != nil {
		t.Fatal(err)
	}
	var actual struct {
		Key  string                `json:"sourceKey"`
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(next, &actual)
	if actual.Key != in.Key {
		t.Fatal("read window rotation changed analysis identity")
	}
	accepted := stringSetOf(in.Info.Documents)
	for _, id := range actual.Info.Documents {
		if accepted[id] && actual.Info.Phase == "batch" {
			t.Fatal("an accepted document was scheduled again", id)
		}
	}
	for _, change := range []string{"originalRequest", "_reviewRevision", "researchSourceVersion"} {
		v := decodeObject(rotated)
		v[change] = "changed"
		changed, err := prepareSelectedEvidence(detail, mustJSON(v))
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(changed, &actual)
		if actual.Key == in.Key || len(actual.Info.Analyses) != 0 {
			t.Fatal("changed research context reused old analysis", change)
		}
	}
}

func TestSelectedEvidenceDirectBudgetIncludesPreparedEnvelope(t *testing.T) {
	input := mustJSON(map[string]any{"documentIds": []string{"a", "b"}, "candidates": []tool.CitationRef{{DocumentID: "a"}, {DocumentID: "b"}}})
	size := (120*1024 - len(input) - 30) / 2
	input = mustJSON(map[string]any{"documentIds": []string{"a", "b"}, "candidates": []tool.CitationRef{{DocumentID: "a", Quote: strings.Repeat("x", size)}, {DocumentID: "b", Quote: strings.Repeat("x", size)}}})
	if len(input) >= 120*1024 {
		t.Fatal("fixture already exceeds the raw budget")
	}
	prepared, err := prepareSelectedEvidence(RunDetail{}, input)
	if err != nil || len(prepared) > 120*1024 {
		t.Fatalf("prepared input exceeded its intended budget: %d, %v", len(prepared), err)
	}
}

func TestSelectedEvidenceCheckpointInvalidatesChangedSourceDependencies(t *testing.T) {
	detail, base := selectedEvidenceProjectionFixture()
	input, err := prepareSelectedEvidence(detail, base)
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		Key  string                `json:"sourceKey"`
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(input, &first)
	notes := []map[string]any{}
	for _, id := range first.Info.Documents {
		notes = append(notes, map[string]any{"documentId": id, "finding": "Fixture", "limitations": "Fixture", "references": []string{}})
	}
	detail.AIExecutions = []AIExecution{{ID: "accepted", WorkflowStepID: "screen", Status: "completed", Output: mustJSON(map[string]any{"documentAnalyses": notes})}}
	detail.Events = []RuntimeEvent{{Type: "workflow.selected_evidence_batch", Payload: mustJSON(map[string]any{"executionId": "accepted", "sourceKey": first.Key})}}
	for _, nodeID := range []string{"evidence_search", "evidence_sync"} {
		t.Run(nodeID, func(t *testing.T) {
			var changed RunDetail
			if err := json.Unmarshal(mustJSON(detail), &changed); err != nil {
				t.Fatal(err)
			}
			step := findNodeStep(changed.Steps, nodeID)
			output := decodeObject(step.Output)
			output["sourceVersion"] = "new source dependency"
			step.Output = mustJSON(output)
			next, err := prepareSelectedEvidence(changed, base)
			if err != nil {
				t.Fatal(err)
			}
			var actual struct {
				Key  string                `json:"sourceKey"`
				Info selectedEvidenceInput `json:"_selectedEvidence"`
			}
			_ = json.Unmarshal(next, &actual)
			if actual.Key == first.Key || len(actual.Info.Analyses) != 0 || !rawJSONEqual(mustJSON(actual.Info.Documents), mustJSON(first.Info.Documents)) {
				t.Fatal("changed frozen search/index source reused prior analyses")
			}
		})
	}
}

func TestSelectedEvidenceCoveragePrioritizesUsedSourcesWithinSeedCapacity(t *testing.T) {
	for _, usedCount := range []int{1, citation.MaxSeededKnowledgeCitations, citation.MaxSeededKnowledgeCitations + 1} {
		t.Run(fmt.Sprintf("used_%d", usedCount), func(t *testing.T) {
			refs, used := []tool.CitationRef{}, []string{}
			for i := 0; i < citation.MaxSeededKnowledgeCitations+2; i++ {
				ref := tool.CitationRef{DocumentID: "doc", Reference: fmt.Sprintf("[K-SOURCE-%d]", i), Quote: fmt.Sprintf("Exact fixture source %d", i)}
				refs = append(refs, ref)
				if i < usedCount {
					used = append(used, ref.Reference)
				}
			}
			base := mustJSON(map[string]any{"documentIds": []string{"doc"}, "candidates": refs})
			detail := RunDetail{Steps: []Step{
				{ID: "search", NodeID: "evidence_search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": refs})},
				{ID: "screen", NodeID: "evidence_screening", Status: StepQueued},
			}}
			key := selectedEvidenceSourceKey(detail, base)
			detail.AIExecutions = []AIExecution{{ID: "accepted", WorkflowStepID: "screen", Status: "completed", Output: mustJSON(map[string]any{"documentAnalyses": []map[string]any{{"documentId": "doc", "finding": "Fixture", "limitations": "Fixture", "references": used}}})}}
			detail.Events = []RuntimeEvent{{Type: "workflow.selected_evidence_batch", Payload: mustJSON(map[string]any{"executionId": "accepted", "sourceKey": key})}}
			// The newest model window contains only unused sources; accepted
			// analyses still require earlier exact excerpts to be restored.
			rotated := decodeObject(base)
			rotated["candidates"] = refs[usedCount:]
			prepared, err := prepareSelectedEvidence(detail, mustJSON(rotated))
			if usedCount > citation.MaxSeededKnowledgeCitations {
				if err == nil || !strings.Contains(err.Error(), "existing citation seed capacity") {
					t.Fatal("overflow silently discarded an accepted source", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var actual struct {
				Refs []tool.CitationRef `json:"candidates"`
			}
			_ = json.Unmarshal(prepared, &actual)
			if len(actual.Refs) != citation.MaxSeededKnowledgeCitations {
				t.Fatal("coverage did not retain the existing seed capacity", len(actual.Refs))
			}
			available := map[string]tool.CitationRef{}
			for _, ref := range actual.Refs {
				available[ref.Reference] = ref
			}
			for _, ref := range refs[:usedCount] {
				if got, exists := available[ref.Reference]; !exists || got.Quote != ref.Quote || got.DocumentID != ref.DocumentID {
					t.Fatal("capacity eviction discarded or altered a used source", ref.Reference)
				}
			}
		})
	}
}

func TestSelectedEvidenceCoverageRetainsAcceptedBatchSourcesOutsideWindow(t *testing.T) {
	detail, base := selectedEvidenceProjectionFixture()
	input, err := prepareSelectedEvidence(detail, base)
	if err != nil {
		t.Fatal(err)
	}
	key := decodeObject(input)["sourceKey"]
	var original struct {
		Documents []string           `json:"documentIds"`
		Refs      []tool.CitationRef `json:"candidates"`
	}
	_ = json.Unmarshal(base, &original)
	byDoc := map[string]tool.CitationRef{}
	for _, ref := range original.Refs {
		byDoc[ref.DocumentID] = ref
	}
	notes := []map[string]any{}
	for _, id := range original.Documents {
		refs := []string{}
		if ref, exists := byDoc[id]; exists {
			refs = append(refs, ref.Reference)
		}
		notes = append(notes, map[string]any{"documentId": id, "finding": "Bounded finding", "limitations": "Fixture", "references": refs})
	}
	detail.AIExecutions = []AIExecution{{ID: "done", WorkflowStepID: "screen", Status: "completed", Output: mustJSON(map[string]any{"documentAnalyses": notes})}}
	detail.Events = []RuntimeEvent{{Type: "workflow.selected_evidence_batch", Payload: mustJSON(map[string]any{"executionId": "done", "sourceKey": key})}}
	// Actual source dependency is unchanged, but the model window has evicted
	// five sources already used by accepted document analyses.
	rotated := decodeObject(base)
	rotated["candidates"] = original.Refs[len(original.Refs)-1:]
	prepared, err := prepareSelectedEvidence(detail, mustJSON(rotated))
	if err != nil {
		t.Fatal(err)
	}
	var actual struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
		Refs []tool.CitationRef    `json:"candidates"`
	}
	_ = json.Unmarshal(prepared, &actual)
	if actual.Info.Phase != "coverage" || len(actual.Info.Analyses) != len(original.Documents) {
		t.Fatal("accepted analyses were lost")
	}
	available := map[string]tool.CitationRef{}
	for _, ref := range actual.Refs {
		available[ref.Reference] = ref
	}
	for _, ref := range original.Refs {
		if got, exists := available[ref.Reference]; !exists || got.Quote != ref.Quote || got.DocumentID != ref.DocumentID {
			t.Fatal("coverage lost an accepted batch's original evidence", ref.Reference)
		}
	}
}

func TestSelectedEvidenceDocumentDiagnosticsLocateActionableErrors(t *testing.T) {
	input := raw(`{"_selectedEvidence":{"phase":"batch","documents":["a","b"]},"candidates":[]}`)
	for _, tc := range []struct{ name, output, diagnostic string }{
		{"off_batch", `{"documentAnalyses":[{"documentId":"a"},{"documentId":"x"}]}`, `documentAnalyses[1].documentId "x" is outside`},
		{"duplicate", `{"documentAnalyses":[{"documentId":"a"},{"documentId":"a"}]}`, `documentAnalyses[1].documentId "a" is duplicated`},
		{"missing", `{"documentAnalyses":[{"documentId":"a"}]}`, "missing current batch documents: b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSelectedEvidence(raw(tc.output), input); err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatal("rejected result lacks actionable correction context", err)
			}
		})
	}
}
