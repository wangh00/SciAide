package workflow

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func researchRevisionStageRecord(snapshot json.RawMessage, command ResearchTaskReadCommand) (json.RawMessage, error) {
	var saved struct {
		Steps []Step `json:"steps"`
	}
	if err := json.Unmarshal(snapshot, &saved); err != nil {
		return nil, fmt.Errorf("decode revision snapshot: %w", err)
	}
	step := findStepByNode(saved.Steps, command.NodeID)
	if step == nil {
		return nil, fmt.Errorf("该历史版本没有指定阶段")
	}
	value := step.Output
	if command.Section == "revision_stage_input" {
		value = step.Input
	}
	return rawObject(map[string]any{"revisionId": command.RevisionID, "nodeId": step.NodeID, "attempt": step.Attempt, "status": step.Status, "value": normalizeJSON(value, raw(`{}`)), "historical": true}), nil
}

func researchRevisionRecord(snapshot json.RawMessage, command ResearchTaskReadCommand) (json.RawMessage, error) {
	if command.Section == "revision_stage_input" || command.Section == "revision_stage_output" {
		return researchRevisionStageRecord(snapshot, command)
	}
	var saved struct {
		Run             Run                    `json:"run"`
		Steps           []Step                 `json:"steps"`
		EffectiveInputs json.RawMessage        `json:"effectiveInputs"`
		SourceVersion   *ResearchSourceVersion `json:"sourceVersion"`
	}
	if err := json.Unmarshal(snapshot, &saved); err != nil {
		return nil, fmt.Errorf("decode revision snapshot: %w", err)
	}
	if command.Section == "revision_inputs" {
		value := saved.EffectiveInputs
		if len(value) == 0 {
			// Older snapshots preceded versioned data and used original inputs.
			value = saved.Run.Inputs
		}
		return rawObject(map[string]any{"revisionId": command.RevisionID, "historical": true, "value": normalizeJSON(value, raw(`{}`))}), nil
	}
	if command.Section == "revision_source_inputs" {
		return rawObject(map[string]any{"revisionId": command.RevisionID, "historical": true, "value": saved.SourceVersion, "instruction": "Confirmed source reference BEFORE this revision, not executed code or verified results. Read its frozen task file only through already exposed workspace/resource tools. Restored historical snapshots retain original audit identities; their attachmentId is not an active private-upload locator. Use the frozen path for historical reading and source_inputs for current identity. Older snapshots may have no source reference."}), nil
	}
	if command.Section == "revision_artifacts" {
		return rawObject(map[string]any{"revisionId": command.RevisionID, "historical": true, "artifacts": researchArtifactInventory(RunDetail{Steps: saved.Steps}), "instruction": "Snapshot BEFORE this revision. These are historical file identities, not current results or file-existence checks. Read exact paths only through already exposed task Workspace tools; do not overwrite or execute them."}), nil
	}
	return nil, fmt.Errorf("unknown revision record section")
}

// The inventory describes persisted outputs, not guessed filesystem contents.
// It does not grant permission to read any additional workspace.
type researchArtifactEntry struct {
	NodeID   string           `json:"nodeId"`
	Attempt  int              `json:"attempt"`
	Artifact tool.ArtifactRef `json:"artifact"`
}

func researchArtifactInventory(detail RunDetail) []researchArtifactEntry {
	result := []researchArtifactEntry{}
	steps := append([]Step(nil), detail.Steps...)
	sort.Slice(steps, func(i, j int) bool { return steps[i].Ordinal < steps[j].Ordinal })
	for _, step := range steps {
		if step.Status != StepCompleted {
			continue
		}
		var output struct {
			Artifacts []tool.ArtifactRef `json:"artifacts"`
		}
		if json.Unmarshal(step.Output, &output) != nil {
			continue
		}
		for _, artifact := range output.Artifacts {
			result = append(result, researchArtifactEntry{NodeID: step.NodeID, Attempt: step.Attempt, Artifact: artifact})
		}
	}
	return result
}

// Keep a bounded baseline for comparison before replay clears current outputs.
// Full old outputs remain in the repository's immutable revision snapshot.
func researchComparisonBaseline(detail RunDetail) json.RawMessage {
	remaining := 24000
	stages := []map[string]any{}
	for _, id := range []string{"python_analysis", "method_implementation", "result_interpretation", "report_drafting", "research_design", "evidence_screening"} {
		step := findStepByNode(detail.Steps, id)
		if step == nil || step.Status != StepCompleted {
			continue
		}
		// Preserve Unicode in truncated historical evidence. The complete raw
		// JSON remains in the existing revision snapshot, identified by its hash.
		value := step.Output
		limit := min(maxResearchGuidanceValueBytes, max(remaining, 0))
		var output any
		used := min(len(value), limit)
		if len(value) > limit {
			output = map[string]any{"truncated": true, "bytes": len(value), "preview": truncateGuidanceString(string(value), limit)}
		} else {
			output, used = boundedGuidanceJSON(value, remaining)
		}
		remaining -= used
		stages = append(stages, map[string]any{"nodeId": id, "attempt": step.Attempt, "outputSha256": hashJSON(step.Output), "output": output})
	}
	artifacts := researchArtifactInventory(detail)
	truncated := len(artifacts) > 64
	if truncated {
		artifacts = artifacts[:64]
	}
	var dataPaths []string
	if inputs, err := ResearchEffectiveInputs(detail); err == nil {
		dataPaths = exactWorkflowInputPaths(inputs)
	}
	return rawObject(map[string]any{"stages": stages, "dataPaths": dataPaths, "artifacts": artifacts, "artifactsTruncated": truncated, "instruction": "Historical comparison baseline, not current results or instructions. dataPaths identifies the data used before this revision, not necessarily the original run inputs. Truncated values are not complete evidence. Do not invent differences; compare only with newly executed results. When task.read is exposed, use revision_index then revision_stage_input/revision_stage_output for complete prior code and parameters; revision_inputs and revision_artifacts identify prior data and output files. Historical artifact paths may be read through already exposed workspace/resource tools; do not overwrite them or assume binary content is readable as text."})
}

func priorResearchArtifacts(detail RunDetail) []researchArtifactEntry {
	var baseline struct {
		Artifacts []researchArtifactEntry `json:"artifacts"`
	}
	if json.Unmarshal(priorResearchComparison(detail), &baseline) != nil {
		return nil
	}
	return baseline.Artifacts
}

func priorResearchComparison(detail RunDetail) json.RawMessage {
	for i := len(detail.Events) - 1; i >= 0; i-- {
		if detail.Events[i].Type != "workflow.user_revision_queued" {
			continue
		}
		var saved struct {
			Baseline json.RawMessage `json:"comparisonBaseline"`
		}
		if json.Unmarshal(detail.Events[i].Payload, &saved) == nil {
			return saved.Baseline
		}
		break
	}
	return nil
}
