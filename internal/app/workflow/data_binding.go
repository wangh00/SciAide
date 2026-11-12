package workflow

import (
	"encoding/json"
	"path/filepath"
)

const dataBindingVersion = "data-binding-v1"

// A persisted start adopts its declared frozen files. Planning prose, private
// uploads and model-authored flags are not binding records. No saved prose or
// original input snapshot is rewritten by this projection.
type researchDataBinding struct {
	Version               string              `json:"version"`
	Status                string              `json:"status"`
	WorkflowRunID         string              `json:"workflowRunId"`
	ResearchTaskID        string              `json:"researchTaskId"`
	EffectiveInputsSHA256 string              `json:"effectiveInputsSha256"`
	Source                string              `json:"source"`
	Files                 []researchBoundFile `json:"files"`
}

type researchBoundFile struct {
	RelativePath string `json:"relativePath"`
	Name         string `json:"name"`
	SHA256       string `json:"sha256"`
}

func currentResearchDataBinding(d RunDetail) *researchDataBinding {
	if d.Run.ID == "" || d.Run.ResearchTaskID == "" || d.Run.WorkflowPurpose == PurposeResearchStarter || d.Run.InputsSHA256 == "" || hashJSON(d.Run.Inputs) != d.Run.InputsSHA256 {
		return nil
	}
	created := false
	for _, event := range d.Events {
		if event.WorkflowRunID != d.Run.ID || event.Type != "workflow.created" {
			continue
		}
		var start struct {
			InputsSHA256 string `json:"inputsSha256"`
		}
		created = json.Unmarshal(event.Payload, &start) == nil && start.InputsSHA256 == d.Run.InputsSHA256
		break
	}
	if !created {
		return nil
	}
	inputs, err := ResearchEffectiveInputs(d)
	if err != nil {
		return nil
	}
	paths, err := FrozenInputPaths(d.Run.Compilation, inputs)
	if err != nil || len(paths) == 0 {
		return nil
	}
	result := &researchDataBinding{Version: dataBindingVersion, Status: "bound", WorkflowRunID: d.Run.ID, ResearchTaskID: d.Run.ResearchTaskID, EffectiveInputsSHA256: hashJSON(inputs), Source: "workflow_start", Files: []researchBoundFile{}}
	if version, err := currentResearchDataVersion(d); err != nil {
		return nil
	} else if version != nil {
		result.Source = "confirmed_data_revision"
	}
	for _, path := range paths {
		sha, ok := FrozenInputSHA256(path)
		if !ok {
			return nil
		}
		ext := filepath.Ext(path)
		result.Files = append(result.Files, researchBoundFile{RelativePath: path, Name: workflowSnapshotBase(filepath.Base(path), ext) + ext, SHA256: sha})
	}
	return result
}

func bindResearchDataBinding(d RunDetail, node CompiledNode, arguments map[string]any) {
	if node.Kind != NodeAIAnalysis && node.Kind != NodeAgentStage {
		return
	}
	delete(arguments, "dataBinding")
	if binding := currentResearchDataBinding(d); binding != nil {
		arguments["dataBinding"] = binding
	}
}

const researchDataBindingInstruction = `
Current file adoption (data-binding-v1):
dataBinding is host-derived execution state, not scientific evidence. status=bound identifies the exact files already selected, frozen and adopted by a persisted formal workflow start or confirmed data revision. These files need no second binding confirmation. A planning request, consolidated goal, old method text or review may still say "not bound", "only prepare routes" or "wait for confirmation": that describes the earlier planning state, not the current file adoption. Preserve scientific requirements from that history, but use this current host record for file authorization. Mere library uploads, unsent attachments or a candidate route do not acquire this status.
Do not invent analysisInput.binding_confirmed=false or another default-false consent switch that prevents reading these already adopted files. Code may verify their declared identity; it must not ask the user to bind the same unchanged files again. File adoption proves neither scientific validity nor completed computation, and does not settle ethical permissions, genuinely unresolved research choices or new data adoption. Correct stale pending-binding explanations against this record without altering historical evidence or pretending earlier calculations ran.
`

func researchDataBindingPrompt(d RunDetail, step Step) string {
	binding := currentResearchDataBinding(d)
	if binding == nil {
		return ""
	}
	var frozen map[string]json.RawMessage
	if json.Unmarshal(step.Input, &frozen) == nil && rawJSONEqual(frozen["dataBinding"], mustJSON(binding)) {
		return researchDataBindingInstruction
	}
	// Old completed/queued attempts keep their original JSON/hash/schema.
	// A new execution prompt can explain the persisted current host state.
	return researchDataBindingInstruction + "\n<current_data_binding>\n" + string(mustJSON(binding)) + "\n</current_data_binding>\nThis record supplements a legacy stage input; do not invent /dataBinding pointers when that field is absent from the actual frozen stage_input.\n"
}
