package workflow

import (
	"encoding/json"
	"sort"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const (
	evidenceAnalysisCheckpointVersion = "document-analysis-v2"
	evidenceAnalysisCheckpointV1      = "document-analysis-v1"
)

// Cache identity is per document and scientific input, not the synthesis
// attempt or rotating excerpt window. A reused analysis is still a prior
// interpretation for the current reviewer to verify, never scientific approval.
func evidenceAnalysisKeys(d RunDetail, input json.RawMessage) map[string]string {
	return evidenceAnalysisKeysForVersion(d, input, evidenceAnalysisCheckpointVersion)
}

func evidenceAnalysisKeysForVersion(d RunDetail, input json.RawMessage, version string) map[string]string {
	args := decodeObject(input)
	for _, key := range []string{"_selectedEvidence", "_evidenceWorkPaging", "researchRecordFacts", "sourceKey", "indexedEvidenceWorkingSet", "indexedReadRanges", "localEvidenceSearchQueries", "localEvidenceSearchRoundsRemaining", "_reviewRevision", "_reviewCorrections"} {
		delete(args, key)
	}
	var refs []tool.CitationRef
	if s := findStepByNode(d.Steps, "evidence_search"); s != nil && s.Status == StepCompleted {
		var out struct {
			Citations []tool.CitationRef `json:"citations"`
		}
		_ = json.Unmarshal(s.Output, &out)
		refs = append(refs, out.Citations...)
	} else {
		_ = json.Unmarshal(mustJSON(args["candidates"]), &refs)
	}
	refs = append(refs, indexedEvidenceSnapshots(d)...)
	// Original snapshots, not provider-specific marker substitutions. Ignore
	// duplicate reads and their ordering, but retain any genuinely new content.
	unique := map[string]tool.CitationRef{}
	for _, ref := range refs {
		unique[string(mustJSON(ref))] = ref
	}
	ordered := make([]string, 0, len(unique))
	for key := range unique {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	refs = nil
	for _, key := range ordered {
		refs = append(refs, unique[key])
	}
	var documents []map[string]any
	if sync := findStepByNode(d.Steps, "evidence_sync"); sync != nil && sync.Status == StepCompleted {
		var out struct {
			Structured struct {
				Documents []map[string]any `json:"documents"`
			} `json:"structured"`
		}
		_ = json.Unmarshal(sync.Output, &out)
		documents = out.Structured.Documents
	}
	keys := map[string]string{}
	for _, id := range stringSliceValue(args["documentIds"]) {
		own := []tool.CitationRef{}
		for _, ref := range refs {
			if ref.DocumentID == id {
				own = append(own, ref)
			}
		}
		one := projectSelectedEvidenceBatch(d, args, []string{id}, own)
		for _, document := range documents {
			if document["id"] == id {
				one["indexedDocument"] = document
				break
			}
		}
		if version == evidenceAnalysisCheckpointVersion {
			// Query audit grows on every follow-up, even with identical evidence.
			// Only this host-defined string array is non-scientific identity;
			// preserve all coverage flags/counts, references and unknown shapes.
			// This is a private hash projection, never a model-input rewrite.
			if value, exists := one["documentCoverage"]; exists {
				var coverage []map[string]any
				if json.Unmarshal(mustJSON(value), &coverage) == nil {
					for _, row := range coverage {
						if audit, exists := row["queries"]; exists {
							queries, stringsOnly := audit.([]any)
							for _, value := range queries {
								if _, ok := value.(string); !ok {
									stringsOnly = false
								}
							}
							if stringsOnly {
								delete(row, "queries")
							}
						}
					}
					one["documentCoverage"] = coverage
				}
			}
		}
		one["analysisCheckpointVersion"] = version
		keys[id] = hashJSON(mustJSON(one))
	}
	return keys
}

func evidenceCheckpointPayload(d RunDetail, step Step, evidence any) map[string]any {
	if evidence != nil {
		d.Events = append(append([]RuntimeEvent{}, d.Events...), RuntimeEvent{Type: "workflow.indexed_evidence_read", Payload: mustJSON(evidence)})
	}
	return map[string]any{"version": evidenceAnalysisCheckpointVersion, "keys": evidenceAnalysisKeys(d, step.Input)}
}

func acceptedEvidenceAnalyses(d RunDetail, step Step, input json.RawMessage, sourceKey string) []json.RawMessage {
	keys := evidenceAnalysisKeys(d, input)
	type checkpoint struct {
		RunID       string `json:"-"`
		ExecutionID string `json:"executionId"`
		SourceKey   string `json:"sourceKey"`
		Analysis    struct {
			Version string            `json:"version"`
			Keys    map[string]string `json:"keys"`
		} `json:"analysisCheckpoint"`
	}
	checkpoints := map[string]checkpoint{}
	for _, event := range d.Events {
		if event.WorkflowRunID != "" && event.WorkflowRunID != d.Run.ID {
			continue
		}
		if event.Type != "workflow.selected_evidence_batch" && event.Type != "workflow.ai_completed" && event.Type != "workflow.completed" && event.Type != "workflow.local_evidence_requested" {
			continue
		}
		var cp checkpoint
		if json.Unmarshal(event.Payload, &cp) == nil && cp.ExecutionID != "" {
			// Historical follow-ups did not register document work. Never
			// retroactively treat those events as a legacy accepted checkpoint.
			if event.Type == "workflow.local_evidence_requested" && cp.Analysis.Version != evidenceAnalysisCheckpointVersion && cp.Analysis.Version != evidenceAnalysisCheckpointV1 {
				continue
			}
			cp.RunID = event.WorkflowRunID
			checkpoints[cp.ExecutionID] = cp
		}
	}
	executions := append([]AIExecution{}, d.AIExecutions...)
	sort.SliceStable(executions, func(i, j int) bool { return executions[i].Attempt > executions[j].Attempt })
	done := map[string]bool{}
	result := []json.RawMessage{}
	var v1Keys map[string]string
	for _, e := range executions {
		if e.WorkflowRunID != "" && e.WorkflowRunID != d.Run.ID || e.WorkflowStepID != step.ID || e.Status != "completed" {
			continue
		}
		cp, ok := checkpoints[e.ID]
		if !ok {
			continue
		}
		if cp.Analysis.Version != "" && cp.Analysis.Version != evidenceAnalysisCheckpointVersion && cp.Analysis.Version != evidenceAnalysisCheckpointV1 {
			continue
		}
		modern := cp.Analysis.Version != ""
		if modern && (cp.RunID != d.Run.ID || e.WorkflowRunID != d.Run.ID || e.OutputSHA256 == "" || hashJSON(e.Output) != e.OutputSHA256) {
			continue
		}
		var out struct {
			Documents []json.RawMessage `json:"documentAnalyses"`
		}
		if json.Unmarshal(e.Output, &out) != nil {
			continue
		}
		matchKeys := keys
		if cp.Analysis.Version == evidenceAnalysisCheckpointV1 {
			// A v1 record retains its original, stricter query-sensitive hash.
			// Do not relabel or grant new v2 identity rules to historical work.
			if v1Keys == nil {
				v1Keys = evidenceAnalysisKeysForVersion(d, input, evidenceAnalysisCheckpointV1)
			}
			matchKeys = v1Keys
		}
		for _, row := range out.Documents {
			var doc struct {
				ID string `json:"documentId"`
			}
			if json.Unmarshal(row, &doc) != nil || keys[doc.ID] == "" || done[doc.ID] {
				continue
			}
			if modern {
				if cp.Analysis.Keys[doc.ID] != matchKeys[doc.ID] {
					continue
				}
			} else if cp.SourceKey != sourceKey {
				continue
			}
			done[doc.ID] = true
			result = append(result, row)
		}
	}
	return result
}

// Coverage may replace only explicitly supplied document records. All other
// accepted records remain verbatim; the frozen input and past outputs stay put.
func mergedEvidenceAnalyses(baseline, replacements []json.RawMessage) []json.RawMessage {
	updates := map[string]json.RawMessage{}
	for _, row := range replacements {
		var doc struct {
			ID string `json:"documentId"`
		}
		_ = json.Unmarshal(row, &doc)
		updates[doc.ID] = row
	}
	result := append([]json.RawMessage{}, baseline...)
	for i, row := range result {
		var doc struct {
			ID string `json:"documentId"`
		}
		_ = json.Unmarshal(row, &doc)
		if update, ok := updates[doc.ID]; ok {
			result[i] = update
		}
	}
	return result
}
